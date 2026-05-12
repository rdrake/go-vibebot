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

	model, modelID, err := selectLLM(opts.LLMProvider, opts.GeminiModel, opts.GeminiAPIKey)
	if err != nil {
		logger.Error("llm select", "err", err)
		os.Exit(1)
	}

	if err := run(logger, model, modelID, opts.DBPath, opts.SeedDir, opts.Tick, ircConfig(opts.IRC, logger)); err != nil {
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
		Nick: opts.Nick, Channel: opts.Channel,
		SASLUser: opts.SASLUser, SASLPass: opts.SASLPass,
		Logger: logger,
	}
}

// vectorStoreFactory builds the memory.VectorStore used by run/runCtx.
// Tests inject a failing factory to drive the hydrate-abort assertion.
type vectorStoreFactory func(*store.SQLiteStore) memory.VectorStore

// defaultVectorStore is the production wiring: a SQLite-backed VectorStore
// sharing the SQLiteStore's *sql.DB.
func defaultVectorStore(st *store.SQLiteStore) memory.VectorStore {
	return memory.NewSQLiteVectorStoreAdapter(store.NewSQLiteVectorStore(st.DB()))
}

// run is the production entrypoint. It wires the signal-aware context and
// calls runCtx. The bulk of the implementation lives in runCtx so tests can
// supply their own context and seams.
func run(logger *slog.Logger, llmImpl llm.LLM, modelID, dbPath, seedDir string,
	tick time.Duration, ircCfg *irc.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCtx(ctx, logger, llmImpl, modelID, dbPath, seedDir, tick, ircCfg, defaultVectorStore)
}

// runCtx is the testable core. It takes an external context and a vector
// store factory so a test can drive it with a deadline and inject failures.
func runCtx(ctx context.Context, logger *slog.Logger, llmImpl llm.LLM,
	modelID, dbPath, seedDir string, tick time.Duration, ircCfg *irc.Config,
	vsFactory vectorStoreFactory) error {

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
	places, err := config.LoadPlaces(filepath.Join(seedDir, "places"))
	if err != nil {
		return err
	}
	if err := config.Validate(chars, groups, places); err != nil {
		return err
	}
	if len(groups) == 0 {
		return fmt.Errorf("no groups defined in %s", seedDir)
	}

	if vsFactory == nil {
		vsFactory = defaultVectorStore
	}
	vs := vsFactory(st)

	byID := make(map[api.CharacterID]*character.Character, len(chars))
	for _, spec := range chars {
		id := api.CharacterID(spec.ID)
		mem := memory.NewEmbedded(llmImpl, 200,
			memory.WithPersister(vs, id, modelID))
		if err := mem.Hydrate(ctx, st); err != nil {
			return fmt.Errorf("hydrate %s: %w", id, err)
		}
		byID[id] = &character.Character{
			ID:           id,
			Name:         spec.Name,
			Persona:      spec.Persona,
			Capabilities: spec.Capabilities,
			Blurb:        spec.Blurb,
			Memory:       mem,
			Inbox:        make(chan character.Perception, 8),
		}
	}

	// --- scene/group construction unchanged from the original run() ---
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

	for _, p := range places {
		if len(p.NPCs) == 0 {
			logger.Warn("place has no npcs; skipping", "place", p.ID)
			continue
		}
		placeScene := &scene.Scene{
			ID:      api.SceneID("place:" + p.ID),
			PlaceID: api.PlaceID(p.ID),
			Router:  scene.LLMRouter{Model: llmImpl, PreFilterK: 0, MaxConsult: 0},
		}
		for _, nid := range p.NPCs {
			npc, ok := byID[api.CharacterID(nid)]
			if !ok {
				// Validate ran earlier; reaching here would be a code bug.
				return fmt.Errorf("place %s references unknown character %s", p.ID, nid)
			}
			placeScene.Members = append(placeScene.Members, npc)
		}
		// First NPC in the yaml list is the leader.
		placeScene.Leader = placeScene.Members[0]
		w.RegisterScene(placeScene)
		logger.Info("registered place scene",
			"place", p.ID, "members", len(placeScene.Members), "leader", placeScene.Leader.ID)
	}

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
