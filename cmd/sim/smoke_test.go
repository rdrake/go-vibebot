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
	if verr := config.Validate(chars, groups, nil); verr != nil {
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

	if injErr := api.InjectEvent(ctx, "", "", "Stinky Sam finds a suspicious sandwich"); injErr != nil {
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
		dbPath, seedDir, 100*time.Millisecond, nil, false, failingFactory)
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

// TestSummonCathedralInjectAndSpeak boots the full runtime wiring (real
// seed YAML, echoLLM, SQLite, place loader), summons the cathedral, and
// injects a scenario scoped to the cathedral scene. It asserts:
//   - !summon succeeds (no error)
//   - the inject produces speech from at least one cathedral NPC
//   - the synthesized event is attributed to the cathedral's leader (vicar)
//   - the gang scene is undisturbed: zero events with the gang scene id
func TestSummonCathedralInjectAndSpeak(t *testing.T) {
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
	places, err := config.LoadPlaces(filepath.Join(seedDir, "places"))
	if err != nil {
		t.Fatalf("load places: %v", err)
	}
	if verr := config.Validate(chars, groups, places); verr != nil {
		t.Fatalf("validate: %v", verr)
	}
	if len(places) == 0 {
		t.Fatal("no places loaded; seed/places empty?")
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

	w := world.New(world.Config{TickInterval: time.Hour}, st, llmImpl)

	// Gang scene first so it remains the default.
	g := groups[0]
	gang := &scene.Scene{ID: api.SceneID(g.ID), Router: scene.LLMRouter{Model: llmImpl}}
	for _, mid := range g.Members {
		gang.Members = append(gang.Members, byID[api.CharacterID(mid)])
	}
	gang.Leader = byID[api.CharacterID(g.Leader)]
	w.RegisterScene(gang)

	var cathedralSceneID api.SceneID
	var cathedralLeader api.CharacterID
	npcIDs := map[api.CharacterID]struct{}{}
	for _, p := range places {
		if p.ID != "cathedral" {
			continue
		}
		sc := &scene.Scene{
			ID:      api.SceneID("place:" + p.ID),
			PlaceID: api.PlaceID(p.ID),
			Router:  scene.LLMRouter{Model: llmImpl},
		}
		for _, nid := range p.NPCs {
			c, ok := byID[api.CharacterID(nid)]
			if !ok {
				t.Fatalf("place %s npc %s not in characters", p.ID, nid)
			}
			sc.Members = append(sc.Members, c)
			npcIDs[c.ID] = struct{}{}
		}
		sc.Leader = sc.Members[0]
		w.RegisterScene(sc)
		cathedralSceneID = sc.ID
		cathedralLeader = sc.Leader.ID
	}
	if cathedralSceneID == "" {
		t.Fatal("cathedral place not found in seed/places")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() { defer close(runDone); _ = w.Run(ctx) }()

	wapi := w.API()

	if err := wapi.Summon(ctx, "cathedral"); err != nil {
		t.Fatalf("summon: %v", err)
	}
	if err := wapi.InjectEvent(ctx, cathedralSceneID, "", "the flagstones smell of incense"); err != nil {
		t.Fatalf("inject: %v", err)
	}

	entries, err := wapi.Log(ctx, time.Hour)
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	var sawSummon, sawSynth, sawSpeech bool
	for _, e := range entries {
		if e.SceneID == api.SceneID(g.ID) && e.Kind != string(store.KindSceneEnter) {
			// Any event landing on the gang scene id from this test would
			// indicate cross-scene leakage. The only allowed gang-id event
			// is a scene_enter (which we don't emit) — strict zero events.
			t.Errorf("gang scene saw unexpected event: %+v", e)
		}
		if e.SceneID != cathedralSceneID {
			continue
		}
		switch store.Kind(e.Kind) {
		case store.KindSummon:
			sawSummon = true
		case store.KindSynthesized:
			sawSynth = true
			if e.Actor != string(cathedralLeader) {
				t.Errorf("synthesized actor: want %s, got %s", cathedralLeader, e.Actor)
			}
		case store.KindSpeech:
			if _, ok := npcIDs[api.CharacterID(e.Actor)]; !ok {
				t.Errorf("speech actor not an NPC: %s", e.Actor)
			}
			sawSpeech = true
		}
	}
	if !sawSummon {
		t.Error("no summon event on cathedral scene")
	}
	if !sawSpeech {
		t.Error("no NPC speech on cathedral scene — orchestrate did not fan out")
	}
	if !sawSynth {
		t.Error("no synthesized event on cathedral scene — leader did not synthesize")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("world.Run did not return after cancel")
	}
}
