package world

import (
	"context"
	"errors"
	"fmt"
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

// newCharactersPlacesWorld builds a world with one regular scene and one
// place-scene, characters carry blurbs so the assertions exercise real
// data. Returned context is already long enough for the read query.
func newCharactersPlacesWorld(t *testing.T) (*World, context.Context, context.CancelFunc) {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID, name, blurb string) *character.Character {
		return &character.Character{
			ID:     id,
			Name:   name,
			Blurb:  blurb,
			Memory: memory.NewInMem(10),
			Inbox:  make(chan character.Perception, 4),
		}
	}

	gangLeader := mk("stinky-sam", "Stinky Sam", "smells like a wet dog")
	gang := &scene.Scene{
		ID:      api.SceneID("the-gang"),
		Members: []*character.Character{gangLeader, mk("booger-bertha", "Booger Bertha", "picks her nose")},
		Leader:  gangLeader,
	}

	vicar := mk("vicar", "The Vicar", "worried about the draft")
	cathedral := &scene.Scene{
		ID:      api.SceneID("place:cathedral"),
		PlaceID: api.PlaceID("cathedral"),
		Members: []*character.Character{vicar, mk("caretaker", "The Caretaker", "mutters at a broom")},
		Leader:  vicar,
	}

	w := New(Config{TickInterval: time.Hour}, st, &mockLLM{})
	w.RegisterScene(gang)
	w.RegisterScene(cathedral)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	go func() { _ = w.Run(ctx) }()
	return w, ctx, cancel
}

func TestWorldCharactersReturnsSortedRefs(t *testing.T) {
	w, ctx, cancel := newCharactersPlacesWorld(t)
	defer cancel()

	refs, err := w.Characters(ctx)
	if err != nil {
		t.Fatalf("Characters: %v", err)
	}
	if len(refs) != 4 {
		t.Fatalf("Characters len: got %d, want 4", len(refs))
	}
	for i := 1; i < len(refs); i++ {
		if refs[i-1].ID >= refs[i].ID {
			t.Errorf("Characters not sorted: %q before %q", refs[i-1].ID, refs[i].ID)
		}
	}
	for _, r := range refs {
		if r.Name == "" {
			t.Errorf("CharacterRef %q has empty Name", r.ID)
		}
		if r.Blurb == "" {
			t.Errorf("CharacterRef %q has empty Blurb", r.ID)
		}
	}
}

func TestWorldPlacesOnlyIncludesPlaceScenes(t *testing.T) {
	w, ctx, cancel := newCharactersPlacesWorld(t)
	defer cancel()

	places, err := w.Places(ctx)
	if err != nil {
		t.Fatalf("Places: %v", err)
	}
	if len(places) != 1 {
		t.Fatalf("Places len: got %d, want 1 (the gang scene has no PlaceID)", len(places))
	}
	p := places[0]
	if p.ID != api.PlaceID("cathedral") {
		t.Errorf("place ID: got %q, want %q", p.ID, "cathedral")
	}
	if p.SceneID != api.SceneID("place:cathedral") {
		t.Errorf("scene id: got %q, want %q", p.SceneID, "place:cathedral")
	}
	if p.Leader != api.CharacterID("vicar") {
		t.Errorf("leader: got %q, want %q", p.Leader, "vicar")
	}
	if len(p.Members) != 2 {
		t.Errorf("members len: got %d, want 2", len(p.Members))
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

func TestRegisterSceneAfterRunDoesNotPanic(t *testing.T) {
	w, _, _ := newTestWorld(t)

	// Build a second scene reusing the boot members (already in w.characters).
	w2 := &scene.Scene{
		ID:      api.SceneID("ad-hoc-1"),
		Members: []*character.Character{},
	}
	// Pull a registered character out of the boot scene.
	for _, m := range w.scenes[api.SceneID("scene-1")].Members {
		w2.Members = append(w2.Members, m)
		break
	}
	w2.Leader = w2.Members[0]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	reply := make(chan error, 1)
	select {
	case w.commands <- RegisterSceneCmd{Scene: w2, Reply: reply}:
	case <-time.After(time.Second):
		t.Fatal("could not post RegisterSceneCmd")
	}
	select {
	case err := <-reply:
		if err != nil {
			t.Fatalf("runtime register: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no reply from coordinator")
	}

	// Verify the new scene is reachable via the existing Inject command.
	if err := w.API().InjectEvent(ctx, "ad-hoc-1", "", "hello"); err != nil {
		t.Fatalf("inject against new scene: %v", err)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("world.Run did not return after cancel")
	}
}

func TestRegisterSceneLockedRejectsUnknownMember(t *testing.T) {
	// Direct test of the locked helper. Spec test #2 (which targets the
	// SummonNew API surface) is implemented in Task 4 once SummonNew exists.
	w, _, _ := newTestWorld(t)
	bad := &scene.Scene{
		ID:      api.SceneID("ghost"),
		Members: []*character.Character{{ID: "nobody", Inbox: make(chan character.Perception, 1)}},
	}
	bad.Leader = bad.Members[0]
	if err := w.registerSceneLocked(bad); err == nil {
		t.Fatal("expected unknown-character error")
	}
	if _, dup := w.scenes["ghost"]; dup {
		t.Fatal("scene must not be registered when validation fails")
	}
}

func TestRegisterSceneLockedDuplicateRejected(t *testing.T) {
	w, _, _ := newTestWorld(t)
	dup := &scene.Scene{
		ID:      api.SceneID("scene-1"),
		Members: w.scenes["scene-1"].Members,
		Leader:  w.scenes["scene-1"].Leader,
	}
	if err := w.registerSceneLocked(dup); err == nil {
		t.Fatal("expected duplicate-scene-id error")
	}
}

func TestRequestCharactersByIDResolvesExisting(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	chars, err := w.requestCharactersByID(ctx, []api.CharacterID{"leader", "m1"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(chars) != 2 || chars[0].ID != "leader" || chars[1].ID != "m1" {
		t.Fatalf("unexpected chars: %+v", chars)
	}

	cancel()
	<-runDone
}

func TestRequestCharactersByIDReportsMissing(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	_, err := w.requestCharactersByID(ctx, []api.CharacterID{"leader", "ghost", "wisp"})
	if err == nil {
		t.Fatal("expected error for missing characters")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "wisp") {
		t.Fatalf("error should name missing ids, got: %v", err)
	}

	cancel()
	<-runDone
}

func TestSummonNewWithoutDescriptionEmitsOnlySummon(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	sceneID, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader", "m1"}, "")
	if err != nil {
		t.Fatalf("SummonNew: %v", err)
	}
	if sceneID != "place:spire" {
		t.Fatalf("want sceneID place:spire, got %q", sceneID)
	}

	evs, err := st.Query(ctx, store.Filter{SceneID: sceneID})
	if err != nil {
		t.Fatal(err)
	}
	var summonCount, injectCount int
	for _, e := range evs {
		switch e.Kind {
		case store.KindSummon:
			summonCount++
		case store.KindInject:
			injectCount++
		}
	}
	if summonCount != 1 {
		t.Errorf("want 1 KindSummon, got %d", summonCount)
	}
	if injectCount != 0 {
		t.Errorf("want 0 KindInject, got %d", injectCount)
	}

	cancel()
	<-runDone
}

func TestSummonNewWithDescriptionWritesInject(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	sceneID, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader", "m1"}, "A drafty steeple.")
	if err != nil {
		t.Fatalf("SummonNew: %v", err)
	}

	evs, err := st.Query(ctx, store.Filter{SceneID: sceneID})
	if err != nil {
		t.Fatal(err)
	}
	var summonCount, injectCount int
	var injectDesc string
	for _, e := range evs {
		switch e.Kind {
		case store.KindSummon:
			summonCount++
		case store.KindInject:
			injectCount++
			injectDesc = store.TextOf(e)
		}
	}
	if summonCount != 1 || injectCount != 1 {
		t.Fatalf("want 1 summon + 1 inject, got %d/%d", summonCount, injectCount)
	}
	if injectDesc != "A drafty steeple." {
		t.Errorf("inject text: want %q, got %q", "A drafty steeple.", injectDesc)
	}

	cancel()
	<-runDone
}

func TestSummonNewUnknownCharacterErrors(t *testing.T) {
	// Spec test #2: drives the full SummonNew API path, including the
	// charactersByIDReq round-trip, not just the locked helper.
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	sceneCountBefore := len(w.scenes)
	_, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader", "ghost"}, "")
	if err == nil {
		t.Fatal("expected unknown-character error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the missing id, got %v", err)
	}
	if got := len(w.scenes); got != sceneCountBefore {
		t.Errorf("scenes map changed: before=%d after=%d", sceneCountBefore, got)
	}

	cancel()
	<-runDone
}

func TestSummonNewRejectsEmptyInputs(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	if _, err := w.API().SummonNew(ctx, "", []api.CharacterID{"leader"}, ""); err == nil {
		t.Error("expected error for empty place id")
	}
	if _, err := w.API().SummonNew(ctx, "spire", nil, ""); err == nil {
		t.Error("expected error for nil npcs")
	}
	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{}, ""); err == nil {
		t.Error("expected error for empty npcs")
	}

	cancel()
	<-runDone
}

func TestSummonNewDuplicatePlaceErrors(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader"}, ""); err != nil {
		t.Fatalf("first SummonNew: %v", err)
	}
	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader"}, ""); err == nil {
		t.Fatal("second SummonNew of same place should error")
	} else if !strings.Contains(err.Error(), "duplicate scene id") {
		t.Errorf("want duplicate-scene-id error, got %v", err)
	}

	cancel()
	<-runDone
}

func TestSummonNewConcurrentDistinctPlacesSafe(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	const N = 8
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			_, err := w.API().SummonNew(ctx, api.PlaceID(fmt.Sprintf("p%d", i)), []api.CharacterID{"leader"}, "")
			errs <- err
		}()
	}
	for i := 0; i < N; i++ {
		if err := <-errs; err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}

	// Distinct places + the boot "scene-1" → N+1 scenes.
	if got, want := len(w.scenes), N+1; got != want {
		t.Errorf("scene count: want %d, got %d", want, got)
	}

	cancel()
	<-runDone
}

func TestSummonNewSamePlaceConcurrentCollision(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	const N = 8
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			_, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader"}, "")
			errs <- err
		}()
	}
	var nilCount, dupCount int
	for i := 0; i < N; i++ {
		err := <-errs
		switch {
		case err == nil:
			nilCount++
		case strings.Contains(err.Error(), "duplicate scene id"):
			dupCount++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if nilCount != 1 {
		t.Errorf("want exactly 1 successful register, got %d", nilCount)
	}
	if dupCount != N-1 {
		t.Errorf("want %d duplicate errors, got %d", N-1, dupCount)
	}

	// Exactly one KindSummon for the place.
	evs, err := st.Query(ctx, store.Filter{SceneID: "place:spire", Kind: store.KindSummon})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Errorf("want exactly 1 KindSummon, got %d", len(evs))
	}

	cancel()
	<-runDone
}

func TestSummonNewCtxCancelledBeforeSend(t *testing.T) {
	w, _, _ := newTestWorld(t)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(runCtx) }()

	// Cancel before the call begins.
	callCtx, callCancel := context.WithCancel(runCtx)
	callCancel()

	_, err := w.API().SummonNew(callCtx, "spire", []api.CharacterID{"leader"}, "")
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	if _, dup := w.scenes["place:spire"]; dup {
		t.Error("scene should not be registered when ctx was cancelled before send")
	}

	runCancel()
	<-runDone
}

func TestSummonNewCtxCancelledRacing(t *testing.T) {
	// Spec test #8 second variant: cancel from a goroutine racing the call.
	// Repeat many times under -race to surface interleavings between the
	// charactersByIDReq send, the RegisterSceneCmd send, and the cancel.
	for i := 0; i < 50; i++ {
		w, _, _ := newTestWorld(t)
		runCtx, runCancel := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() { runDone <- w.Run(runCtx) }()

		callCtx, callCancel := context.WithCancel(runCtx)
		go func() {
			// Cancel after a tiny random-ish delay (a busy loop yields).
			for j := 0; j < i; j++ {
				_ = j
			}
			callCancel()
		}()
		_, err := w.API().SummonNew(callCtx, api.PlaceID(fmt.Sprintf("r%d", i)),
			[]api.CharacterID{"leader"}, "")
		// Either succeeded (cancel came too late) or returned ctx error.
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("iteration %d: unexpected err %v", i, err)
		}

		runCancel()
		<-runDone
	}
}

func TestSummonNewKindSummonAppendFailure(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// Close the store mid-flight so the KindSummon append fails.
	_ = st.Close()

	_, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader"}, "")
	if err == nil {
		t.Fatal("expected append failure to surface")
	}

	// Scene stays registered (documented non-atomic state).
	if _, ok := w.scenes["place:spire"]; !ok {
		t.Error("scene should remain registered after summon-append failure")
	}

	cancel()
	<-runDone
}

func TestWhereAfterSummonNewReturnsBootScene(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// m1 starts in boot scene "scene-1".
	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"m1"}, ""); err != nil {
		t.Fatalf("SummonNew: %v", err)
	}

	snap, err := w.API().Where(ctx, "m1")
	if err != nil {
		t.Fatalf("Where: %v", err)
	}
	if snap.SceneID != "scene-1" {
		t.Errorf("Where should resolve to boot scene; want scene-1, got %q", snap.SceneID)
	}

	cancel()
	<-runDone
}

func TestNudgeAfterSummonNewTargetsBootScene(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"m1"}, ""); err != nil {
		t.Fatalf("SummonNew: %v", err)
	}
	if err := w.API().Nudge(ctx, "m1"); err != nil {
		t.Fatalf("Nudge: %v", err)
	}

	evs, err := st.Query(ctx, store.Filter{Kind: store.KindNudge})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 nudge event, got %d", len(evs))
	}
	if evs[0].SceneID != "scene-1" {
		t.Errorf("Nudge scene: want scene-1 (boot), got %q", evs[0].SceneID)
	}

	cancel()
	<-runDone
}
