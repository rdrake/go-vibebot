package store

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
)

func TestSQLiteAppendQueryRoundtrip(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	ev := NewInjectEvent(api.SceneID("s1"), "stinky-sam", "hello world")
	if appendErr := st.Append(ctx, &ev); appendErr != nil {
		t.Fatalf("append: %v", appendErr)
	}
	if ev.ID == 0 {
		t.Fatal("expected ID to be assigned")
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("expected Timestamp to be set on append")
	}

	got, err := st.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 event, got %d", len(got))
	}
	if got[0].Actor != "stinky-sam" || got[0].Kind != KindInject {
		t.Errorf("unexpected event: %+v", got[0])
	}
	if got[0].Source != SourceIRC {
		t.Errorf("want SourceIRC, got %q", got[0].Source)
	}
	if got[0].SceneID != "s1" {
		t.Errorf("want scene s1, got %q", got[0].SceneID)
	}
	if text := TextOf(got[0]); text != "hello world" {
		t.Errorf("TextOf=%q", text)
	}
}

func TestSQLiteMemoryStoreUsesSingleConnection(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if got := st.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("want :memory: store pinned to 1 connection, got %d", got)
	}
}

func TestSQLiteFilePathWithQuestionMarkOpensIntendedFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "events?scene.db")
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ev := NewInjectEvent("scene-1", "alice", "hello")
	if err := st.Append(context.Background(), &ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("expected database at literal path %q: %v", dbPath, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), "events")); err == nil {
		t.Fatalf("database was created at truncated path")
	}
}

func TestSQLiteRelativeFilePathOpens(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	st, err := OpenSQLite("vibebot.db")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
}

func TestSQLiteFilterSince(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	old := NewAmbientEvent("s1", "ages ago")
	old.Timestamp = time.Now().Add(-2 * time.Hour)
	recent := NewAmbientEvent("s1", "just now")
	recent.Timestamp = time.Now().Add(-1 * time.Minute)
	if appendErr := st.Append(ctx, &old); appendErr != nil {
		t.Fatalf("append old: %v", appendErr)
	}
	if appendErr := st.Append(ctx, &recent); appendErr != nil {
		t.Fatalf("append recent: %v", appendErr)
	}

	got, err := st.Query(ctx, Filter{Since: time.Now().Add(-30 * time.Minute)})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].ID != recent.ID {
		t.Errorf("expected recent event, got id=%d", got[0].ID)
	}
}

func TestSQLiteFilterScene(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	for _, sid := range []api.SceneID{"s1", "s2", "s1"} {
		ev := NewSpeechEvent(sid, "x", "hi")
		if appendErr := st.Append(ctx, &ev); appendErr != nil {
			t.Fatalf("append: %v", appendErr)
		}
	}
	got, err := st.Query(ctx, Filter{SceneID: "s1"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
}

func TestSQLiteFilterKind(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	events := []Event{
		NewInjectEvent("s1", "x", "y"),
		NewAmbientEvent("s1", "z"),
	}
	for i := range events {
		if appendErr := st.Append(ctx, &events[i]); appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	got, err := st.Query(ctx, Filter{Kind: KindAmbient})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != KindAmbient {
		t.Errorf("want 1 ambient event, got %+v", got)
	}
}

func TestLookupByIDsReturnsEvents(t *testing.T) {
	t.Parallel()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	var ids []EventID
	for i := 0; i < 3; i++ {
		ev := NewInjectEvent("scene-1", "alice", "hello "+strconv.Itoa(i))
		if appendErr := st.Append(ctx, &ev); appendErr != nil {
			t.Fatalf("Append: %v", appendErr)
		}
		ids = append(ids, ev.ID)
	}

	got, err := st.LookupByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("LookupByIDs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	gotIDs := map[EventID]bool{}
	for _, e := range got {
		gotIDs[e.ID] = true
	}
	for _, id := range ids {
		if !gotIDs[id] {
			t.Errorf("missing event %d in result", id)
		}
	}
}

func TestLookupByIDsMissingOK(t *testing.T) {
	t.Parallel()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	got, err := st.LookupByIDs(context.Background(), []EventID{9999})
	if err != nil {
		t.Fatalf("LookupByIDs of missing id: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestLookupByIDsEmptyInput(t *testing.T) {
	t.Parallel()
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	got, err := st.LookupByIDs(context.Background(), nil)
	if err != nil {
		t.Fatalf("LookupByIDs(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestDBAccessorNonNil(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if st.DB() == nil {
		t.Fatal("DB() returned nil")
	}
}

func TestCharacterMemoryTableExists(t *testing.T) {
	st, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	row := st.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='character_memory'`)
	var name string
	if err := row.Scan(&name); err != nil {
		t.Fatalf("character_memory table missing: %v", err)
	}
	if name != "character_memory" {
		t.Fatalf("got %q, want character_memory", name)
	}
}
