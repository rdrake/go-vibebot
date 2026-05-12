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
//
// The client requests IRCv3 `batch` and `draft/multiline` capabilities at
// CAP negotiation; when both are granted, long outbound messages are
// framed as a single multiline BATCH (preserving them as one logical
// message to capability-aware receivers). Otherwise the adapter falls
// back to chunked PRIVMSGs.
func (a *Adapter) Run(ctx context.Context) error {
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

	go func() {
		<-ctx.Done()
		client.Close()
	}()

	return client.Connect()
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

func (a *Adapter) cmdInject(ctx context.Context, args string, reply func(string)) {
	if args == "" {
		reply("usage: !inject <description>")
		return
	}
	if err := a.api.InjectEvent(ctx, "", "", args); err != nil {
		reply("inject failed: " + err.Error())
		return
	}
	reply("injected.")
}

func (a *Adapter) cmdLog(ctx context.Context, args string, reply func(string)) {
	dur := time.Hour
	if args != "" {
		if d, err := time.ParseDuration(args); err == nil {
			dur = d
		}
	}
	entries, err := a.api.Log(ctx, dur)
	if err != nil {
		reply("log failed: " + err.Error())
		return
	}
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
