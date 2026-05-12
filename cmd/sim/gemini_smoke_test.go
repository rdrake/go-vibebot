package main

import (
	"context"
	"os"
	"path/filepath"
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

// TestGeminiLiveSmoke is opt-in: it only runs when GEMINI_API_KEY is set,
// so CI and offline runs skip it. When run, it drives one inject through
// the same wiring cmd/sim uses with the real Gemini Flash model and prints
// the resulting log.
//
// Run with:
//
//	GEMINI_API_KEY=... go test -race -v ./cmd/sim -run TestGeminiLiveSmoke
func TestGeminiLiveSmoke(t *testing.T) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	modelName := os.Getenv("GEMINI_MODEL")
	if modelName == "" {
		modelName = "gemini-flash-lite-latest"
	}
	model, err := selectLLM("gemini", modelName)
	if err != nil {
		t.Fatalf("selectLLM: %v", err)
	}

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

	byID := make(map[api.CharacterID]*character.Character, len(chars))
	for _, spec := range chars {
		id := api.CharacterID(spec.ID)
		byID[id] = &character.Character{
			ID:           id,
			Name:         spec.Name,
			Persona:      spec.Persona,
			Capabilities: spec.Capabilities,
			Blurb:        spec.Blurb,
			Memory:       memory.NewInMem(200),
			Inbox:        make(chan character.Perception, 8),
		}
	}

	g := groups[0]
	sc := &scene.Scene{
		ID:     api.SceneID(g.ID),
		Router: scene.LLMRouter{Model: model},
	}
	for _, mid := range g.Members {
		sc.Members = append(sc.Members, byID[api.CharacterID(mid)])
	}
	sc.Leader = byID[api.CharacterID(g.Leader)]

	w := world.New(world.Config{TickInterval: time.Hour}, st, model)
	w.RegisterScene(sc)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = w.Run(ctx)
	}()

	if injErr := w.API().InjectEvent(ctx, "", "Stinky Sam finds a suspicious sandwich behind the cathedral."); injErr != nil {
		t.Fatalf("inject: %v", injErr)
	}

	entries, err := w.API().Log(ctx, time.Hour)
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	t.Logf("=== %d log entries ===", len(entries))
	for _, e := range entries {
		t.Logf("[%s] %s/%s :: %s",
			e.Timestamp.Format(time.RFC3339Nano), e.Actor, e.Kind, e.Text)
	}

	var sawSynth bool
	for _, e := range entries {
		if e.Kind == string(store.KindSynthesized) {
			sawSynth = true
			if e.Text == "" {
				t.Error("synthesized event has empty text")
			}
		}
	}
	if !sawSynth {
		t.Fatal("no synthesized event — group did not react via Gemini")
	}

	cancel()
	<-runDone
}
