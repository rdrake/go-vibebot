package memory

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/store"
)

// fakeEmbedder maps prebound text -> vector and serves a default for the
// rest. err, if non-nil, is returned by every call.
type fakeEmbedder struct {
	vectors map[string][]float32
	def     []float32
	err     error
	calls   int
}

func (f *fakeEmbedder) Complete(context.Context, llm.CompleteRequest) (string, error) {
	return "", errors.New("not used")
}

func (f *fakeEmbedder) EmbedText(_ context.Context, text string) ([]float32, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.vectors[text]; ok {
		return v, nil
	}
	return f.def, nil
}

func textEvent(id int64, text string, ts time.Time) store.Event {
	return store.Event{
		ID:        store.EventID(id),
		Timestamp: ts,
		Kind:      store.KindSpeech,
		Actor:     "test",
		Payload:   store.MarshalText(text),
	}
}

func TestEmbedded_RecencyFallback_EmptyQuery(t *testing.T) {
	fake := &fakeEmbedder{def: []float32{1, 0}}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewEmbedded(fake, 0)
	m.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if err := m.Record(context.Background(), textEvent(int64(i), "x", now)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	got, err := m.Retrieve(context.Background(), "", 3)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	for i, want := range []int64{2, 3, 4} {
		if int64(got[i].ID) != want {
			t.Errorf("position %d: want ID %d, got %d", i, want, got[i].ID)
		}
	}
}

func TestEmbedded_SimilarityRanksRelevantFirst(t *testing.T) {
	fake := &fakeEmbedder{
		vectors: map[string][]float32{
			"sandwich":      {1, 0, 0},
			"oak tree":      {0, 1, 0},
			"violin":        {0, 0, 1},
			"food: a wrap":  {0.9, 0.1, 0}, // close to sandwich
		},
		def: []float32{0.5, 0.5, 0.5},
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewEmbedded(fake, 0)
	m.now = func() time.Time { return now }
	// Zero out recency so similarity wins cleanly.
	m.SetRecencyParams(0, time.Hour)

	mustRecord(t, m, 1, "sandwich", now)
	mustRecord(t, m, 2, "oak tree", now)
	mustRecord(t, m, 3, "violin", now)

	got, err := m.Retrieve(context.Background(), "food: a wrap", 2)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 {
		t.Fatalf("expected sandwich (ID=1) first; got IDs %v", ids(got))
	}
}

func TestEmbedded_RecencyBonusBeatsStaleSimilarity(t *testing.T) {
	fake := &fakeEmbedder{
		vectors: map[string][]float32{
			"match":   {1, 0},
			"unrelated": {0, 1},
		},
		def: []float32{1, 0},
	}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewEmbedded(fake, 0)
	m.now = func() time.Time { return now }
	// Lambda must be > 1 to dominate cosine=1.0 vs cosine=0.0 here.
	m.SetRecencyParams(2.0, time.Minute)

	// Old, similar event.
	mustRecord(t, m, 1, "match", now.Add(-2*time.Hour))
	// Fresh, dissimilar event.
	mustRecord(t, m, 2, "unrelated", now)

	got, err := m.Retrieve(context.Background(), "match", 1)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("expected fresh unrelated event to win on recency; got IDs %v", ids(got))
	}
}

func TestEmbedded_CapEvictsOldest(t *testing.T) {
	fake := &fakeEmbedder{def: []float32{1, 0}}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewEmbedded(fake, 2)
	m.now = func() time.Time { return now }

	for i := 1; i <= 4; i++ {
		mustRecord(t, m, int64(i), "x", now)
	}
	if len(m.entries) != 2 {
		t.Fatalf("want 2 entries after cap, got %d", len(m.entries))
	}
	if m.entries[0].event.ID != 3 || m.entries[1].event.ID != 4 {
		t.Fatalf("expected IDs [3,4] retained; got [%d,%d]",
			m.entries[0].event.ID, m.entries[1].event.ID)
	}
}

func TestEmbedded_RecordReturnsEmbedErrButStillStores(t *testing.T) {
	wantErr := errors.New("boom")
	fake := &fakeEmbedder{err: wantErr}
	m := NewEmbedded(fake, 0)
	m.now = time.Now

	err := m.Record(context.Background(), textEvent(1, "hello", time.Now()))
	if !errors.Is(err, wantErr) {
		t.Fatalf("want wrapped %v; got %v", wantErr, err)
	}
	if len(m.entries) != 1 {
		t.Fatalf("event should still be appended even on embed failure; got %d entries", len(m.entries))
	}
	if m.entries[0].embedding != nil {
		t.Fatalf("expected nil embedding on failure")
	}
}

func TestEmbedded_RetrieveFallsBackOnQueryEmbedError(t *testing.T) {
	fake := &fakeEmbedder{def: []float32{1, 0}}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := NewEmbedded(fake, 0)
	m.now = func() time.Time { return now }

	mustRecord(t, m, 1, "a", now)
	mustRecord(t, m, 2, "b", now)

	// Trip the query-side embed call.
	fake.err = errors.New("network down")

	got, err := m.Retrieve(context.Background(), "anything", 1)
	if err != nil {
		t.Fatalf("retrieve should swallow embed errors; got %v", err)
	}
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("expected recency tail (ID=2); got IDs %v", ids(got))
	}
}

func TestEmbedded_EmptyTextEventStoredWithoutEmbedCall(t *testing.T) {
	fake := &fakeEmbedder{def: []float32{1, 0}}
	m := NewEmbedded(fake, 0)
	m.now = time.Now

	// Event with no text payload (nudge/summon scaffolding).
	ev := store.Event{
		ID:   1,
		Kind: store.KindNudge,
	}
	if err := m.Record(context.Background(), ev); err != nil {
		t.Fatalf("record: %v", err)
	}
	if fake.calls != 0 {
		t.Fatalf("expected zero embed calls for empty-text event, got %d", fake.calls)
	}
}

func TestCosine(t *testing.T) {
	cases := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 0}, []float32{1, 0}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"dim mismatch", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"zero vector", []float32{0, 0}, []float32{1, 0}, 0},
		{"empty", nil, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cosine(tc.a, tc.b)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("cosine(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func mustRecord(t *testing.T, m *Embedded, id int64, text string, ts time.Time) {
	t.Helper()
	if err := m.Record(context.Background(), textEvent(id, text, ts)); err != nil {
		t.Fatalf("record %d: %v", id, err)
	}
}

func ids(evs []store.Event) []store.EventID {
	out := make([]store.EventID, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func TestEmbeddedHydratePopulatesEntries(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil { t.Fatalf("OpenSQLite: %v", err) }
	t.Cleanup(func() { _ = st.Close() })
	vs := store.NewSQLiteVectorStore(st.DB())

	owner := api.CharacterID("alice")
	model := "test:m"

	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	for i := 0; i < 3; i++ {
		ev := store.NewInjectEvent("scene-1", "alice", "hello")
		ev.Timestamp = now.Add(time.Duration(i) * time.Second)
		if err := st.Append(ctx, &ev); err != nil { t.Fatal(err) }
		if err := vs.Save(ctx, store.SaveArgs{
			Owner: owner, ModelID: model, EventID: ev.ID,
			Embedding: []float32{float32(i), 0, 0},
			Recorded:  ev.Timestamp,
		}); err != nil { t.Fatal(err) }
	}

	mem := NewEmbedded(&fakeEmbedder{}, 10, WithPersister(NewSQLiteVectorStoreAdapter(vs), owner, model))
	if err := mem.Hydrate(ctx, st); err != nil { t.Fatalf("Hydrate: %v", err) }

	if got := len(mem.entries); got != 3 {
		t.Fatalf("entries len = %d, want 3", got)
	}
	for i := 0; i < 3; i++ {
		wantTs := now.Add(time.Duration(i) * time.Second)
		if !mem.entries[i].recorded.Equal(wantTs) {
			t.Errorf("[%d] recorded = %v, want %v", i, mem.entries[i].recorded, wantTs)
		}
		if !mem.entries[i].event.Timestamp.Equal(wantTs) {
			t.Errorf("[%d] event.Timestamp = %v, want %v", i, mem.entries[i].event.Timestamp, wantTs)
		}
	}
}

func TestEmbeddedHydrateEmptyIsFreshDB(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	t.Cleanup(func() { _ = st.Close() })
	vs := store.NewSQLiteVectorStore(st.DB())

	mem := NewEmbedded(&fakeEmbedder{}, 10,
		WithPersister(NewSQLiteVectorStoreAdapter(vs), api.CharacterID("alice"), "m"))
	if err := mem.Hydrate(context.Background(), st); err != nil {
		t.Fatalf("Hydrate empty: %v", err)
	}
	if len(mem.entries) != 0 {
		t.Fatalf("entries len = %d, want 0", len(mem.entries))
	}
}

func TestEmbeddedHydrateReplacesOnSecondCall(t *testing.T) {
	st, _ := store.OpenSQLite(":memory:")
	t.Cleanup(func() { _ = st.Close() })
	vs := store.NewSQLiteVectorStore(st.DB())
	owner := api.CharacterID("alice")
	model := "m"
	ctx := context.Background()

	seed := func(n int) {
		for i := 0; i < n; i++ {
			ev := store.NewInjectEvent("scene-1", "alice", "msg")
			if err := st.Append(ctx, &ev); err != nil { t.Fatal(err) }
			if err := vs.Save(ctx, store.SaveArgs{
				Owner: owner, ModelID: model, EventID: ev.ID,
				Embedding: []float32{0}, Recorded: time.Now().UTC(),
			}); err != nil { t.Fatal(err) }
		}
	}
	seed(2)

	mem := NewEmbedded(&fakeEmbedder{}, 10, WithPersister(NewSQLiteVectorStoreAdapter(vs), owner, model))
	if err := mem.Hydrate(ctx, st); err != nil { t.Fatal(err) }
	if len(mem.entries) != 2 { t.Fatalf("first hydrate: len = %d", len(mem.entries)) }

	seed(2)
	if err := mem.Hydrate(ctx, st); err != nil { t.Fatal(err) }
	if len(mem.entries) != 4 { t.Fatalf("second hydrate: len = %d, want 4 (replace, not append)", len(mem.entries)) }
}

// vectorEmbedder returns a stable non-empty vector for every text. Used by
// Record-persistence tests so the assertions exercise the persister path,
// not the empty-embedding skip path.
type vectorEmbedder struct{}

func (vectorEmbedder) Complete(_ context.Context, _ llm.CompleteRequest) (string, error) {
	return "", errors.New("complete not supported")
}
func (vectorEmbedder) EmbedText(_ context.Context, _ string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

// errEmbedder fails on EmbedText so callers see the embed-nil skip path.
type errEmbedder struct{}

func (errEmbedder) Complete(_ context.Context, _ llm.CompleteRequest) (string, error) {
	return "", errors.New("complete not supported")
}
func (errEmbedder) EmbedText(_ context.Context, _ string) ([]float32, error) {
	return nil, errors.New("embed fail")
}

// failingPersister always returns an error on Save and Load.
type failingPersister struct{ called int }

func (p *failingPersister) Save(_ context.Context, _ EmbeddingRow) error {
	p.called++
	return errors.New("save fail")
}
func (p *failingPersister) Load(_ context.Context, _ api.CharacterID, _ string, _ int) ([]EmbeddingRow, error) {
	return nil, errors.New("load fail")
}

// countingPersister records Save invocations without failing.
type countingPersister struct {
	saved []EmbeddingRow
}

func (p *countingPersister) Save(_ context.Context, row EmbeddingRow) error {
	p.saved = append(p.saved, row)
	return nil
}
func (p *countingPersister) Load(_ context.Context, _ api.CharacterID, _ string, _ int) ([]EmbeddingRow, error) {
	return nil, nil
}

func TestRecordSavesWhenPersisterConfigured(t *testing.T) {
	cp := &countingPersister{}
	mem := NewEmbedded(vectorEmbedder{}, 10, WithPersister(cp, api.CharacterID("alice"), "m"))
	ev := store.NewInjectEvent("scene-1", "alice", "hi")
	ev.ID = 42
	if err := mem.Record(context.Background(), ev); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(cp.saved) != 1 {
		t.Fatalf("Save called %d times, want 1", len(cp.saved))
	}
	if cp.saved[0].EventID != 42 {
		t.Errorf("saved EventID = %d, want 42", cp.saved[0].EventID)
	}
	if len(cp.saved[0].Embedding) != 3 {
		t.Errorf("saved Embedding len = %d, want 3", len(cp.saved[0].Embedding))
	}
}

func TestRecordSaveFailureKeepsInMemoryEntry(t *testing.T) {
	fp := &failingPersister{}
	mem := NewEmbedded(vectorEmbedder{}, 10, WithPersister(fp, api.CharacterID("alice"), "m"))
	ev := store.NewInjectEvent("scene-1", "alice", "hi")
	ev.ID = 1
	err := mem.Record(context.Background(), ev)
	if err == nil {
		t.Fatalf("expected error from failing Save")
	}
	if len(mem.entries) != 1 {
		t.Errorf("entries len = %d, want 1 (in-memory append should stand)", len(mem.entries))
	}
}

func TestRecordSkipsSaveWhenEmbeddingNil(t *testing.T) {
	cp := &countingPersister{}
	mem := NewEmbedded(errEmbedder{}, 10, WithPersister(cp, api.CharacterID("alice"), "m"))
	ev := store.NewInjectEvent("scene-1", "alice", "hi")
	ev.ID = 1
	_ = mem.Record(context.Background(), ev) // returns embed error
	if len(cp.saved) != 0 {
		t.Fatalf("Save called %d times, want 0 (no embedding to save)", len(cp.saved))
	}
}

func TestRecordSkipsSaveWhenEventIDZero(t *testing.T) {
	cp := &countingPersister{}
	// Use vectorEmbedder so the embedding is NON-empty — this isolates the
	// zero-ID guard from the empty-embedding guard.
	mem := NewEmbedded(vectorEmbedder{}, 10, WithPersister(cp, api.CharacterID("alice"), "m"))
	ev := store.NewInjectEvent("scene-1", "alice", "hi")
	// ev.ID stays 0
	if err := mem.Record(context.Background(), ev); err != nil {
		t.Fatalf("Record with zero ID: %v", err)
	}
	if len(cp.saved) != 0 {
		t.Fatalf("Save called %d times, want 0 (zero ID = skip)", len(cp.saved))
	}
	if len(mem.entries) != 1 {
		t.Errorf("in-memory append should still happen: len = %d", len(mem.entries))
	}
}
