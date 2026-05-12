package store

import (
	"context"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
)

func TestSQLiteVectorStoreRoundTrip(t *testing.T) {
	t.Parallel()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	vs := NewSQLiteVectorStore(st.DB())

	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0).UTC()
	owner := api.CharacterID("alice")

	for i := 0; i < 3; i++ {
		ev := NewInjectEvent("scene-1", "alice", "msg")
		if err := st.Append(ctx, &ev); err != nil {
			t.Fatalf("Append: %v", err)
		}
		if err := vs.Save(ctx, SaveArgs{
			Owner: owner, ModelID: "test:m1", EventID: ev.ID,
			Embedding: []float32{float32(i), float32(i + 1), float32(i + 2)},
			Recorded:  base.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	got, err := vs.Load(ctx, owner, "test:m1", 10)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, want := range []time.Time{base.Add(2 * time.Second), base.Add(time.Second), base} {
		if !got[i].Recorded.Equal(want) {
			t.Errorf("[%d] Recorded = %v, want %v", i, got[i].Recorded, want)
		}
		if len(got[i].Embedding) != 3 {
			t.Errorf("[%d] embedding len = %d", i, len(got[i].Embedding))
		}
	}
}

func TestSQLiteVectorStoreModelFilter(t *testing.T) {
	t.Parallel()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	vs := NewSQLiteVectorStore(st.DB())
	ctx := context.Background()
	owner := api.CharacterID("alice")

	ev := NewInjectEvent("scene-1", "alice", "msg")
	if err := st.Append(ctx, &ev); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	for _, m := range []string{"A", "B"} {
		if err := vs.Save(ctx, SaveArgs{
			Owner: owner, ModelID: m, EventID: ev.ID,
			Embedding: []float32{1, 2}, Recorded: now,
		}); err != nil {
			t.Fatalf("Save %s: %v", m, err)
		}
	}
	for _, want := range []string{"A", "B"} {
		got, err := vs.Load(ctx, owner, want, 10)
		if err != nil {
			t.Fatalf("Load %s: %v", want, err)
		}
		if len(got) != 1 || got[0].ModelID != want {
			t.Errorf("Load %s = %+v", want, got)
		}
	}
	got, err := vs.Load(ctx, owner, "C", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 rows for unknown model, got %d", len(got))
	}
}

func TestSQLiteVectorStoreDeterministicOrderOnTies(t *testing.T) {
	t.Parallel()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	vs := NewSQLiteVectorStore(st.DB())
	ctx := context.Background()
	owner := api.CharacterID("alice")
	now := time.Unix(1_700_000_000, 0).UTC()

	for i := 0; i < 2; i++ {
		ev := NewInjectEvent("scene-1", "alice", "msg")
		if err := st.Append(ctx, &ev); err != nil {
			t.Fatal(err)
		}
		if err := vs.Save(ctx, SaveArgs{
			Owner: owner, ModelID: "m", EventID: ev.ID,
			Embedding: []float32{0}, Recorded: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := vs.Load(ctx, owner, "m", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].EventID <= got[1].EventID {
		t.Errorf("expected descending EventID on tie, got %d then %d", got[0].EventID, got[1].EventID)
	}
}

func TestSQLiteVectorStoreUnboundedLimit(t *testing.T) {
	t.Parallel()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	vs := NewSQLiteVectorStore(st.DB())
	ctx := context.Background()
	owner := api.CharacterID("alice")

	for i := 0; i < 5; i++ {
		ev := NewInjectEvent("scene-1", "alice", "msg")
		if err := st.Append(ctx, &ev); err != nil {
			t.Fatal(err)
		}
		if err := vs.Save(ctx, SaveArgs{
			Owner: owner, ModelID: "m", EventID: ev.ID,
			Embedding: []float32{float32(i)},
			Recorded:  time.Unix(1_700_000_000+int64(i), 0).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := vs.Load(ctx, owner, "m", 0) // limit <= 0 = unbounded
	if err != nil {
		t.Fatalf("Load(limit=0): %v", err)
	}
	if len(got) != 5 {
		t.Errorf("unbounded Load returned %d rows, want 5", len(got))
	}
}
