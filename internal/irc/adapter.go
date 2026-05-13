// Package irc is a thin adapter from an IRC server to WorldAPI. It does no
// world manipulation itself; every command translates into one or more
// WorldAPI calls.
package irc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/store"
	"github.com/lrstanley/girc"
)

// IRCv3 capability names this adapter requests.
const (
	capBatch       = "batch"
	capMultiline   = "draft/multiline"
	capMessageTags = "message-tags"
)

// typingInterval is the re-send cadence for +typing=active TAGMSGs while a
// command handler is working. The IRCv3 typing spec mandates re-sending
// "at least every 6 seconds"; 2 seconds keeps the indicator solid even on
// laggy networks.
const typingInterval = 2 * time.Second

// Config configures the IRC connection.
type Config struct {
	Server   string
	Port     int
	TLS      bool
	Nick     string
	User     string
	Channel  string
	SASLUser string // when non-empty, authenticate via SASL PLAIN
	SASLPass string
	Logger   *slog.Logger
}

// Adapter wires a girc.Client to a WorldAPI.
type Adapter struct {
	cfg          Config
	api          api.WorldAPI
	logger       *slog.Logger
	batchCounter atomic.Uint64
}

// New constructs an IRC adapter. Call Run to connect.
func New(cfg Config, w api.WorldAPI) (*Adapter, error) {
	if cfg.Server == "" || cfg.Channel == "" || cfg.Nick == "" {
		return nil, errors.New("irc: server, channel, and nick are required")
	}
	if cfg.Port == 0 {
		cfg.Port = 6667
	}
	if cfg.User == "" {
		cfg.User = cfg.Nick
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Adapter{cfg: cfg, api: w, logger: cfg.Logger}, nil
}

// Run blocks, dialing the server and handling messages until ctx is done.
// Disconnects (PING timeout, transient network failure) trigger reconnect
// with exponential backoff capped at reconnectMax; a connection that
// stays up longer than reconnectStable resets backoff to the initial
// value so a single bad day doesn't permanently penalize cadence.
//
// The client requests IRCv3 `batch` and `draft/multiline` capabilities at
// CAP negotiation; when both are granted, long outbound messages are
// framed as a single multiline BATCH (preserving them as one logical
// message to capability-aware receivers). Otherwise the adapter falls
// back to chunked PRIVMSGs.
func (a *Adapter) Run(ctx context.Context) error {
	const (
		reconnectInit   = time.Second
		reconnectMax    = time.Minute
		reconnectStable = 30 * time.Second
	)
	backoff := reconnectInit
	for {
		start := time.Now()
		err := a.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if !retryableConnectError(err) {
			return err
		}
		stable := time.Since(start) > reconnectStable
		if stable {
			backoff = reconnectInit
		}
		a.logger.Warn("irc connection lost; reconnecting", "err", err, "in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		if !stable {
			backoff *= 2
			if backoff > reconnectMax {
				backoff = reconnectMax
			}
		}
	}
}

func retryableConnectError(err error) bool {
	var invalidConfig *girc.InvalidConfigError
	if errors.As(err, &invalidConfig) {
		return false
	}
	var eventErr *girc.EventError
	if errors.As(err, &eventErr) && strings.Contains(strings.ToLower(eventErr.Error()), "sasl") {
		return false
	}
	return true
}

// connectOnce blocks in Connect until disconnect or ctx cancellation,
// returning the connect error.
func (a *Adapter) connectOnce(ctx context.Context) error {
	gcfg := girc.Config{
		Server: a.cfg.Server,
		Port:   a.cfg.Port,
		Nick:   a.cfg.Nick,
		User:   a.cfg.User,
		SSL:    a.cfg.TLS,
		SupportedCaps: map[string][]string{
			capBatch:       nil,
			capMultiline:   nil,
			capMessageTags: nil,
		},
	}
	if a.cfg.SASLUser != "" {
		gcfg.SASL = &girc.SASLPlain{User: a.cfg.SASLUser, Pass: a.cfg.SASLPass}
	}
	client := girc.New(gcfg)

	client.Handlers.Add(girc.CONNECTED, func(c *girc.Client, _ girc.Event) {
		c.Cmd.Join(a.cfg.Channel)
		a.logger.Info("irc connected",
			"channel", a.cfg.Channel,
			"cap_batch", c.HasCapability(capBatch),
			"cap_multiline", c.HasCapability(capMultiline),
			"cap_message_tags", c.HasCapability(capMessageTags),
		)
	})
	client.Handlers.Add(girc.PRIVMSG, func(c *girc.Client, e girc.Event) {
		a.handleMessage(ctx, c, e)
	})

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-done:
		}
	}()
	err := client.Connect()
	close(done)
	return err
}

func (a *Adapter) handleMessage(ctx context.Context, c *girc.Client, e girc.Event) {
	if len(e.Params) < 2 {
		return
	}
	target := e.Params[0]
	text := e.Last()
	cmd, ok := ParseCommand(text)
	if !ok {
		return
	}

	dest := target
	if target == c.GetNick() {
		dest = e.Source.Name
	}
	reply := func(s string) { a.sendMessage(c, dest, s) }

	stopTyping := a.startTyping(c, dest)
	defer stopTyping()

	switch cmd.Verb {
	case "inject":
		a.cmdInject(ctx, cmd.Args, reply)
	case "log":
		a.cmdLog(ctx, cmd.Args, reply)
	case "nudge":
		a.cmdNudge(ctx, cmd.Args, reply)
	case "summon":
		a.cmdSummon(ctx, cmd.Args, reply)
	case "snapshot":
		a.cmdSnapshot(ctx, reply)
	default:
		// ignore unknown !commands
	}
}

// startTyping emits an immediate +typing=active TAGMSG and a goroutine
// re-emits it every typingInterval until the returned stop func is called.
// stop emits +typing=done so capability-aware clients can clear the
// indicator promptly. When `message-tags` was not negotiated, both are
// no-ops (typing is a client tag and depends on the cap).
func (a *Adapter) startTyping(c *girc.Client, target string) (stop func()) {
	if !c.HasCapability(capMessageTags) {
		return func() {}
	}
	send := func(state string) {
		_ = c.Cmd.SendRawf("@+typing=%s TAGMSG %s", state, target)
	}
	send("active")
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(typingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				send("active")
			}
		}
	}()
	return func() {
		close(stopCh)
		<-done
		send("done")
	}
}

// sendMessage delivers text to target as one logical message. If the
// connection negotiated `batch` + `draft/multiline`, long messages are
// emitted as a single BATCH with multiline-concat tags so capability-
// aware receivers reassemble the text into one continuous message.
// Otherwise the adapter chunks on word boundaries and sends individual
// PRIVMSGs (which survive the 512-byte frame limit but render as separate
// lines to legacy clients).
func (a *Adapter) sendMessage(c *girc.Client, target, text string) {
	parts := batchPayloadParts(text, maxPrivmsgPayload)
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 {
		c.Cmd.Message(target, parts[0].text)
		return
	}
	if c.HasCapability(capBatch) && c.HasCapability(capMultiline) {
		a.sendMultilineBatch(c, target, parts)
		return
	}
	for _, part := range parts {
		c.Cmd.Message(target, part.text)
	}
}

// sendMultilineBatch frames payload parts as a single draft/multiline BATCH.
// Wrapped continuations carry draft/multiline-concat so the receiver
// concatenates only chunks from the same logical line.
func (a *Adapter) sendMultilineBatch(c *girc.Client, target string, parts []batchPayloadPart) {
	id := fmt.Sprintf("vb%d", a.batchCounter.Add(1))
	if err := c.Cmd.SendRawf("BATCH +%s draft/multiline %s", id, target); err != nil {
		a.logger.Warn("batch start failed; falling back to plain chunks", "err", err)
		for _, part := range parts {
			c.Cmd.Message(target, part.text)
		}
		return
	}
	for _, part := range parts {
		tag := fmt.Sprintf("@batch=%s", id)
		if part.concat {
			tag += ";draft/multiline-concat"
		}
		if err := c.Cmd.SendRawf("%s PRIVMSG %s :%s", tag, target, part.text); err != nil {
			a.logger.Warn("batch line failed", "err", err)
		}
	}
	if err := c.Cmd.SendRawf("BATCH -%s", id); err != nil {
		a.logger.Warn("batch end failed", "err", err)
	}
}

// parseInjectArgs splits the !inject argument string into an optional scene
// id and a description. Forms:
//
//	"<desc>"                → ("", "<desc>", true)
//	"@<scene-id> <desc>"    → ("<scene-id>", "<desc>", true)
//
// Empty args, or @scene with no description, returns ok=false.
func parseInjectArgs(args string) (api.SceneID, string, bool) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", "", false
	}
	if !strings.HasPrefix(args, "@") {
		return "", args, true
	}
	rest := strings.TrimPrefix(args, "@")
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) < 2 {
		return "", "", false
	}
	sceneID := strings.TrimSpace(parts[0])
	desc := strings.TrimSpace(parts[1])
	if sceneID == "" || desc == "" {
		return "", "", false
	}
	return api.SceneID(sceneID), desc, true
}

func (a *Adapter) cmdInject(ctx context.Context, args string, reply func(string)) {
	sceneID, desc, ok := parseInjectArgs(args)
	if !ok {
		reply("usage: !inject [@<scene-id>] <description>")
		return
	}
	if err := a.api.InjectEvent(ctx, sceneID, "", desc); err != nil {
		reply("inject failed: " + err.Error())
		return
	}
	reply("injected.")
}

func (a *Adapter) cmdLog(ctx context.Context, args string, reply func(string)) {
	dur, includeAmbient := parseLogArgs(args)
	entries, err := a.api.Log(ctx, dur)
	if err != nil {
		reply("log failed: " + err.Error())
		return
	}
	entries = filterLogEntries(entries, includeAmbient)
	if len(entries) == 0 {
		reply("(no events in window)")
		return
	}
	// Bundle every line into one logical reply so the adapter emits a
	// single draft/multiline BATCH (or a paced PRIVMSG burst on the
	// fallback path). One reply call per entry trips excess-flood
	// disconnects on servers that count individual PRIVMSGs.
	lines := make([]string, 0, len(entries))
	for _, ent := range entries {
		lines = append(lines, formatLogEntry(ent))
	}
	reply(strings.Join(lines, "\n"))
}

func parseLogArgs(args string) (time.Duration, bool) {
	dur := time.Hour
	includeAmbient := false
	for _, tok := range strings.Fields(args) {
		switch tok {
		case "--ambient":
			includeAmbient = true
		default:
			if d, err := time.ParseDuration(tok); err == nil {
				dur = d
			}
		}
	}
	return dur, includeAmbient
}

func filterLogEntries(entries []api.LogEntry, includeAmbient bool) []api.LogEntry {
	if includeAmbient {
		return entries
	}
	out := make([]api.LogEntry, 0, len(entries))
	for _, ent := range entries {
		if ent.Actor == store.ActorWorld && ent.Kind == string(store.KindAmbient) {
			continue
		}
		out = append(out, ent)
	}
	return out
}

// formatLogEntry renders one log line for IRC output. Empty Text fields
// (e.g. nudge/summon scaffold events) drop the trailing colon rather than
// printing a dangling separator. Timestamps render in local time so log
// output lines up with the user's wall clock.
func formatLogEntry(ent api.LogEntry) string {
	stamp := ent.Timestamp.Local().Format(time.Kitchen)
	if ent.Text == "" {
		return fmt.Sprintf("[%s] %s/%s", stamp, ent.Actor, ent.Kind)
	}
	return fmt.Sprintf("[%s] %s/%s: %s", stamp, ent.Actor, ent.Kind, ent.Text)
}

func (a *Adapter) cmdNudge(ctx context.Context, args string, reply func(string)) {
	if args == "" {
		reply("usage: !nudge <character-id>")
		return
	}
	if err := a.api.Nudge(ctx, api.CharacterID(args)); err != nil {
		reply("nudge failed: " + err.Error())
		return
	}
	reply("nudged.")
}

func (a *Adapter) cmdSummon(ctx context.Context, args string, reply func(string)) {
	if args == "" {
		reply("usage: !summon <place-id>")
		return
	}
	if err := a.api.Summon(ctx, api.PlaceID(args)); err != nil {
		reply("summon failed: " + err.Error())
		return
	}
	reply("summoned.")
}

func (a *Adapter) cmdSnapshot(ctx context.Context, reply func(string)) {
	chars, err := a.api.Characters(ctx)
	if err != nil {
		reply("snapshot failed: " + err.Error())
		return
	}
	places, err := a.api.Places(ctx)
	if err != nil {
		reply("snapshot failed: " + err.Error())
		return
	}
	lines := make([]string, 0, len(places)+2)
	lines = append(lines, fmt.Sprintf("snapshot: characters: %d; places: %d", len(chars), len(places)))
	if len(places) == 0 {
		lines = append(lines, "places: none registered")
	} else {
		lines = append(lines, "places:")
		for _, p := range places {
			lines = append(lines, fmt.Sprintf("- %s scene=%s leader=%s members=%d",
				p.ID, p.SceneID, p.Leader, len(p.Members)))
		}
	}
	reply(strings.Join(lines, "\n"))
}
