package world

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/memory"
	"github.com/afternet/go-vibebot/internal/scene"
	"github.com/afternet/go-vibebot/internal/store"
)

// mockLLM records every Complete call and returns a deterministic reply
// derived from the prompt, so tests can assert fan-out and synthesis.
type mockLLM struct {
	calls atomic.Int64
}

func (m *mockLLM) Complete(_ context.Context, req llm.CompleteRequest) (string, error) {
	m.calls.Add(1)
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	return "REPLY[" + last + "]", nil
}

func (m *mockLLM) EmbedText(_ context.Context, _ string) ([]float32, error) {
	return []float32{0}, nil
}

type failSynthesisLLM struct {
	calls atomic.Int64
}

func (m *failSynthesisLLM) Complete(_ context.Context, req llm.CompleteRequest) (string, error) {
	call := m.calls.Add(1)
	if call == 3 {
		return "", errors.New("synthesis failed")
	}
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	return "REPLY[" + last + "]", nil
}

func (m *failSynthesisLLM) EmbedText(_ context.Context, _ string) ([]float32, error) {
	return []float32{0}, nil
}

func newTestWorld(t *testing.T) (*World, *mockLLM, *store.SQLiteStore) {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID, name string) *character.Character {
		return &character.Character{
			ID:      id,
			Name:    name,
			Persona: "test persona for " + name,
			Memory:  memory.NewInMem(50),
			Inbox:   make(chan character.Perception, 4),
		}
	}
	leader := mk("leader", "Leader")
	m1 := mk("m1", "Member One")
	m2 := mk("m2", "Member Two")

	sc := &scene.Scene{
		ID:      api.SceneID("scene-1"),
		Members: []*character.Character{leader, m1, m2},
		Leader:  leader,
	}

	ll := &mockLLM{}
	w := New(Config{TickInterval: time.Hour}, st, ll)
	w.RegisterScene(sc)
	return w, ll, st
}

func TestInjectPersistsSpeechWhenSynthesisFails(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID, name string) *character.Character {
		return &character.Character{
			ID:      id,
			Name:    name,
			Persona: "test persona for " + name,
			Memory:  memory.NewInMem(50),
			Inbox:   make(chan character.Perception, 4),
		}
	}
	leader := mk("leader", "Leader")
	sc := &scene.Scene{
		ID:      "scene-1",
		Members: []*character.Character{leader, mk("m1", "Member One"), mk("m2", "Member Two")},
		Leader:  leader,
	}

	ll := &failSynthesisLLM{}
	w := New(Config{TickInterval: time.Hour}, st, ll)
	w.RegisterScene(sc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	err = w.API().InjectEvent(ctx, "", "", "trigger synthesis failure")
	if err == nil || !strings.Contains(err.Error(), "synthesis failed") {
		t.Fatalf("want synthesis error, got %v", err)
	}

	evs, err := st.Query(ctx, store.Filter{Kind: store.KindSpeech})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 speech events persisted before failed synthesis, got %d", len(evs))
	}
}

func TestInjectAppendsAndDispatches(t *testing.T) {
	w, ll, st := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = w.Run(ctx)
	}()

	a := w.API()

	if err := a.InjectEvent(ctx, "", "stinky-sam", "found a suspicious sandwich"); err != nil {
		t.Fatalf("inject: %v", err)
	}

	if got := ll.calls.Load(); got < 3 {
		t.Fatalf("want >=3 LLM calls (2 members + 1 synthesis), got %d", got)
	}

	evs, err := st.Query(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var sawInject, sawSynth bool
	var injectText string
	for _, ev := range evs {
		switch ev.Kind {
		case store.KindInject:
			sawInject = true
			injectText = store.TextOf(ev)
		case store.KindSynthesized:
			sawSynth = true
		case store.KindSpeech, store.KindAction, store.KindPerception,
			store.KindSceneEnter, store.KindAmbient, store.KindSummon, store.KindNudge:
			// other kinds are unrelated to this assertion
		}
	}
	if !sawInject {
		t.Error("expected an inject event in store")
	}
	if !sawSynth {
		t.Error("expected a synthesized event in store")
	}
	if !strings.Contains(injectText, "suspicious sandwich") {
		t.Errorf("inject payload text mismatch: %q", injectText)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("world.Run did not return after cancel")
	}
}

func TestInjectAppendBeforeBroadcast(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close() // closed store — Append will fail.

	mk := func(id api.CharacterID) *character.Character {
		return &character.Character{
			ID: id, Name: string(id),
			Memory: memory.NewInMem(10),
			Inbox:  make(chan character.Perception, 1),
		}
	}
	leader := mk("leader")
	sc := &scene.Scene{ID: "s", Members: []*character.Character{leader, mk("m")}, Leader: leader}

	ll := &mockLLM{}
	w := New(Config{TickInterval: time.Hour}, st, ll)
	w.RegisterScene(sc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	a := w.API()
	if err := a.InjectEvent(ctx, "", "x", "should fail"); err == nil {
		t.Fatal("expected inject to fail with closed store")
	}
	if got := ll.calls.Load(); got != 0 {
		t.Errorf("expected 0 LLM calls on failed append, got %d", got)
	}
}

func TestWhereReturnsSceneSnapshot(t *testing.T) {
	w, _, _ := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	a := w.API()
	snap, err := a.Where(ctx, "m1")
	if err != nil {
		t.Fatalf("where: %v", err)
	}
	if snap.SceneID != "scene-1" {
		t.Errorf("want scene-1, got %q", snap.SceneID)
	}
	if snap.Leader != "leader" {
		t.Errorf("want leader=leader, got %q", snap.Leader)
	}
	if len(snap.Members) != 3 {
		t.Errorf("want 3 members, got %d", len(snap.Members))
	}
}

func TestRegisterSceneAfterRunPanics(t *testing.T) {
	w, _, _ := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Give Run a moment to flip the running flag.
	for i := 0; i < 100 && !w.running.Load(); i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if !w.running.Load() {
		t.Fatal("world did not enter running state")
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic, got none")
		}
	}()
	w.RegisterScene(&scene.Scene{ID: "late"})
}

func TestNudgeWritesEventForKnownCharacter(t *testing.T) {
	w, _, st := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	if err := w.API().Nudge(ctx, "m1"); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	evs, err := st.Query(ctx, store.Filter{Kind: store.KindNudge})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 nudge event, got %d", len(evs))
	}
	if evs[0].Actor != "m1" {
		t.Errorf("want actor=m1, got %q", evs[0].Actor)
	}
}

// trackingMem wraps a memory store with a mutex (so the test goroutine can
// read concurrently with the decide goroutine) and a one-shot signal that
// fires the first time a synthesized event is recorded.
type trackingMem struct {
	mu     sync.Mutex
	inner  *memory.InMem
	gotSyn chan struct{}
	once   sync.Once
}

func newTrackingMem(cap int) *trackingMem {
	return &trackingMem{inner: memory.NewInMem(cap), gotSyn: make(chan struct{})}
}

func (t *trackingMem) Record(ctx context.Context, ev store.Event) error {
	t.mu.Lock()
	err := t.inner.Record(ctx, ev)
	t.mu.Unlock()
	if ev.Kind == store.KindSynthesized {
		t.once.Do(func() { close(t.gotSyn) })
	}
	return err
}

func (t *trackingMem) Retrieve(ctx context.Context, q string, k int) ([]store.Event, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inner.Retrieve(ctx, q, k)
}

func (t *trackingMem) Summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inner.Summary()
}

// TestInjectBroadcastsSynthesizedToMemberMemory asserts every scene member
// records the synthesized round outcome in memory — characters remember
// what the group did, not each peer's utterance.
func TestInjectBroadcastsSynthesizedToMemberMemory(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID, name string) (*character.Character, *trackingMem) {
		mem := newTrackingMem(50)
		return &character.Character{
			ID: id, Name: name, Persona: "test " + name,
			Memory: mem,
			Inbox:  make(chan character.Perception, 4),
		}, mem
	}
	leader, _ := mk("leader", "Leader")
	m1, mem1 := mk("m1", "Member One")
	m2, mem2 := mk("m2", "Member Two")

	sc := &scene.Scene{
		ID:      "scene-1",
		Members: []*character.Character{leader, m1, m2},
		Leader:  leader,
	}
	w := New(Config{TickInterval: time.Hour}, st, &mockLLM{})
	w.RegisterScene(sc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() { defer close(runDone); _ = w.Run(ctx) }()

	if err := w.API().InjectEvent(ctx, "", "", "the cat knocks over the lamp"); err != nil {
		t.Fatalf("inject: %v", err)
	}

	// Both non-leader members must record the synthesized round.
	for name, mem := range map[string]*trackingMem{"m1": mem1, "m2": mem2} {
		select {
		case <-mem.gotSyn:
		case <-time.After(1 * time.Second):
			t.Errorf("member %s never recorded a synthesized event", name)
		}
		evs, _ := mem.Retrieve(ctx, "", 100)
		for _, ev := range evs {
			if ev.Kind == store.KindSpeech && ev.Actor != name {
				t.Errorf("member %s recorded peer speech from %s (should be outcome-only)",
					name, ev.Actor)
			}
		}
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("world.Run did not return after cancel")
	}
}

func TestNudgeUnknownCharacterErrors(t *testing.T) {
	w, _, _ := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	if err := w.API().Nudge(ctx, "nope"); err == nil {
		t.Fatal("expected error nudging unknown character")
	}
}

func TestInjectUnknownSceneIDErrors(t *testing.T) {
	w, _, _ := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	err := w.API().InjectEvent(ctx, "ghost-scene", "", "anything")
	if err == nil {
		t.Fatal("expected error for unknown scene id")
	}
	if !strings.Contains(err.Error(), "scene") {
		t.Fatalf("expected error to mention scene, got %v", err)
	}
}

func TestDefaultSceneIsFirstRegistered(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID) *character.Character {
		return &character.Character{
			ID: id, Name: string(id),
			Memory: memory.NewInMem(10),
			Inbox:  make(chan character.Perception, 1),
		}
	}
	first := &scene.Scene{ID: "first", Leader: mk("la"), Members: []*character.Character{mk("la")}}
	second := &scene.Scene{ID: "second", Leader: mk("lb"), Members: []*character.Character{mk("lb")}}

	w := New(Config{TickInterval: time.Hour}, st, &mockLLM{})
	w.RegisterScene(first)
	w.RegisterScene(second)

	// Call defaultScene 100 times; it must always return "first".
	for i := 0; i < 100; i++ {
		got := w.defaultScene()
		if got == nil || got.ID != "first" {
			t.Fatalf("iteration %d: want scene id 'first', got %v", i, got)
		}
	}
}

func TestSummonUnknownPlaceErrors(t *testing.T) {
	w, _, _ := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	err := w.API().Summon(ctx, "nowhere")
	if err == nil {
		t.Fatal("expected error summoning unknown place")
	}
	if !strings.Contains(err.Error(), "unknown place") {
		t.Fatalf("expected 'unknown place' in error, got %v", err)
	}
}

func TestSummonKnownPlaceWritesSummonEventScopedToPlaceScene(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID) *character.Character {
		return &character.Character{
			ID: id, Name: string(id),
			Memory: memory.NewInMem(10),
			Inbox:  make(chan character.Perception, 1),
		}
	}
	gangLeader := mk("g-leader")
	gang := &scene.Scene{ID: "gang", Leader: gangLeader, Members: []*character.Character{gangLeader}}

	npc := mk("npc")
	cathedral := &scene.Scene{
		ID:      "place:cathedral",
		PlaceID: "cathedral",
		Leader:  npc,
		Members: []*character.Character{npc},
	}

	w := New(Config{TickInterval: time.Hour}, st, &mockLLM{})
	w.RegisterScene(gang)
	w.RegisterScene(cathedral)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	if err := w.API().Summon(ctx, "cathedral"); err != nil {
		t.Fatalf("summon: %v", err)
	}

	evs, err := st.Query(ctx, store.Filter{Kind: store.KindSummon})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 summon event, got %d", len(evs))
	}
	if evs[0].SceneID != "place:cathedral" {
		t.Fatalf("want summon scoped to place:cathedral, got %q", evs[0].SceneID)
	}
}
