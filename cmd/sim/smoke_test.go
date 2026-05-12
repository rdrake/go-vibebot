package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/config"
	"github.com/afternet/go-vibebot/internal/memory"
	"github.com/afternet/go-vibebot/internal/scene"
	"github.com/afternet/go-vibebot/internal/store"
	"github.com/afternet/go-vibebot/internal/world"
)

// TestSmokeEndToEnd exercises the same wiring cmd/sim uses (real seed
// YAML, echoLLM, LLMRouter, SQLite), drives one inject through the public
// WorldAPI, and prints the resulting log. This is the actual walking-
// skeleton acceptance criterion from the spec.
func TestSmokeEndToEnd(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	seedDir := filepath.Join("..", "..", "seed")
	chars, err := config.LoadCharacters(filepath.Join(seedDir, "characters.yaml"))
	if err != nil {
		t.Fatalf("load characters: %v", err)
	}
	groups, err := config.LoadGroups(filepath.Join(seedDir, "groups.yaml"))
	if err != nil {
		t.Fatalf("load groups: %v", err)
	}
	if verr := config.Validate(chars, groups); verr != nil {
		t.Fatalf("validate: %v", verr)
	}

	llmImpl := echoLLM{}

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
		Router: scene.LLMRouter{Model: llmImpl},
	}
	for _, mid := range g.Members {
		c, ok := byID[api.CharacterID(mid)]
		if !ok {
			t.Fatalf("unknown member %s", mid)
		}
		sc.Members = append(sc.Members, c)
	}
	sc.Leader = byID[api.CharacterID(g.Leader)]

	w := world.New(world.Config{TickInterval: time.Hour}, st, llmImpl)
	w.RegisterScene(sc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = w.Run(ctx)
	}()

	api := w.API()

	if injErr := api.InjectEvent(ctx, "", "Stinky Sam finds a suspicious sandwich"); injErr != nil {
		t.Fatalf("inject: %v", injErr)
	}

	entries, err := api.Log(ctx, time.Hour)
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	t.Logf("=== %d log entries ===", len(entries))
	for _, e := range entries {
		t.Logf("[%s] %s/%s :: %s",
			e.Timestamp.Format(time.RFC3339Nano), e.Actor, e.Kind, e.Text)
	}

	var sawInject, sawSynth bool
	for _, e := range entries {
		switch e.Kind {
		case string(store.KindInject):
			sawInject = true
		case string(store.KindSynthesized):
			sawSynth = true
		}
	}
	if !sawInject {
		t.Error("no inject event in log")
	}
	if !sawSynth {
		t.Error("no synthesized event in log — group did not react")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("world.Run did not return after cancel")
	}
}

// failingVectorStore makes Hydrate fail so we can assert run aborts cleanly.
type failingVectorStore struct{}

func (failingVectorStore) Save(_ context.Context, _ memory.EmbeddingRow) error { return nil }
func (failingVectorStore) Load(_ context.Context, _ api.CharacterID, _ string, _ int) ([]memory.EmbeddingRow, error) {
	return nil, errors.New("simulated load failure")
}

func TestRunCtxAbortsBootOnHydrateFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dbPath := filepath.Join(t.TempDir(), "v.db")
	seedDir := filepath.Join("..", "..", "seed")

	failingFactory := func(_ *store.SQLiteStore) memory.VectorStore {
		return failingVectorStore{}
	}

	err := runCtx(ctx, logger, echoLLM{}, echoEmbeddingModelID,
		dbPath, seedDir, 100*time.Millisecond, nil, failingFactory)
	if err == nil {
		t.Fatal("expected runCtx to return an error from Hydrate")
	}
	if !strings.Contains(err.Error(), "hydrate") {
		t.Errorf("error %q does not mention hydrate", err)
	}
}

func TestRunCtxPersistsAndHydratesRoundTrip(t *testing.T) {
	t.Skip("TODO: round-trip integration test (see plan Step 8.5)")
}
