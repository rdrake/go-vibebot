// Command sim boots the walking skeleton: SQLite event store, one group
// loaded from YAML, a world coordinator with ticker, and an optional IRC
// adapter. The LLM is a local echo provider so the binary runs with no
// credentials.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/config"
	"github.com/afternet/go-vibebot/internal/irc"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/memory"
	"github.com/afternet/go-vibebot/internal/scene"
	"github.com/afternet/go-vibebot/internal/store"
	"github.com/afternet/go-vibebot/internal/world"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	opts, err := parseRuntimeOptions(os.Args[1:], ".")
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printRuntimeUsage(os.Stdout)
			return
		}
		flag.CommandLine.SetOutput(os.Stderr)
		logger.Error("parse flags/config", "err", err)
		os.Exit(2)
	}

	model, err := selectLLM(opts.LLMProvider, opts.GeminiModel)
	if err != nil {
		logger.Error("llm select", "err", err)
		os.Exit(1)
	}

	if err := run(logger, model, opts.DBPath, opts.SeedDir, opts.Tick, ircConfig(opts.IRC, logger)); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func ircConfig(opts ircOptions, logger *slog.Logger) *irc.Config {
	if opts.Server == "" {
		return nil
	}
	return &irc.Config{
		Server: opts.Server, Port: opts.Port, TLS: opts.TLS,
		Nick: opts.Nick, Channel: opts.Channel, Logger: logger,
	}
}

func run(logger *slog.Logger, llmImpl llm.LLM, dbPath, seedDir string, tick time.Duration, ircCfg *irc.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	chars, err := config.LoadCharacters(filepath.Join(seedDir, "characters.yaml"))
	if err != nil {
		return err
	}
	groups, err := config.LoadGroups(filepath.Join(seedDir, "groups.yaml"))
	if err != nil {
		return err
	}
	if err := config.Validate(chars, groups); err != nil {
		return err
	}
	if len(groups) == 0 {
		return fmt.Errorf("no groups defined in %s", seedDir)
	}

	byID := make(map[api.CharacterID]*character.Character, len(chars))
	for _, spec := range chars {
		id := api.CharacterID(spec.ID)
		byID[id] = &character.Character{
			ID:           id,
			Name:         spec.Name,
			Persona:      spec.Persona,
			Capabilities: spec.Capabilities,
			Blurb:        spec.Blurb,
			Memory:       memory.NewEmbedded(llmImpl, 200),
			Inbox:        make(chan character.Perception, 8),
		}
	}

	g := groups[0]
	sc := &scene.Scene{
		ID:     api.SceneID(g.ID),
		Router: scene.LLMRouter{Model: llmImpl, PreFilterK: 0, MaxConsult: 0},
	}
	for _, mid := range g.Members {
		c, ok := byID[api.CharacterID(mid)]
		if !ok {
			return fmt.Errorf("group %s references unknown character %s", g.ID, mid)
		}
		sc.Members = append(sc.Members, c)
	}
	leader, ok := byID[api.CharacterID(g.Leader)]
	if !ok {
		return fmt.Errorf("group %s leader %s not found", g.ID, g.Leader)
	}
	sc.Leader = leader

	w := world.New(world.Config{TickInterval: tick, Logger: logger}, st, llmImpl)
	w.RegisterScene(sc)

	worldAPI := w.API()

	worldErr := make(chan error, 1)
	go func() { worldErr <- w.Run(ctx) }()

	var ircErr chan error
	if ircCfg != nil {
		a, err := irc.New(*ircCfg, worldAPI)
		if err != nil {
			return err
		}
		ircErr = make(chan error, 1)
		go func() { ircErr <- a.Run(ctx) }()
		logger.Info("irc adapter dialing", "server", ircCfg.Server)
	} else {
		logger.Info("irc adapter disabled (no -irc-server provided)")
	}

	select {
	case <-ctx.Done():
		<-worldErr
		if ircErr != nil {
			<-ircErr
		}
		return nil
	case err := <-worldErr:
		return err
	case err := <-ircErr:
		return err
	}
}
