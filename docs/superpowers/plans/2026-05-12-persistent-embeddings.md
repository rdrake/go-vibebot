# Persistent Embeddings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist per-character episodic embeddings across restarts via a backend-swappable interface, so characters retain memory after the binary cycles.

**Architecture:** A new `memory.VectorStore` interface decouples `memory.Embedded` from any specific backend. The interface persists only `(owner, modelID, eventID, vector, timestamp)` rows — never events themselves. At hydrate time, `Embedded` resolves event IDs against an `EventLookup` (satisfied by `*store.SQLiteStore`). The first backend is `store.SQLiteVectorStore`, sharing the existing `*sql.DB`.

**Tech Stack:** Go 1.24, `modernc.org/sqlite` (pure Go SQLite driver), `database/sql`, `encoding/binary`, `slog`.

**Repository note:** This repo has no commits yet. **Before starting Task 1, the executor must verify with the user how to commit the initial source tree** (or whether to commit each task on top of an initial squash). Tasks below assume a working `git commit` flow; each task ends with one commit on `main`.

**Spec:** `docs/superpowers/specs/2026-05-12-persistent-embeddings-design.md`

---

## File Structure

| Path | Status | Responsibility |
|---|---|---|
| `internal/store/vector_codec.go` | new | `vecToBlob`, `blobToVec` — pure encoding |
| `internal/store/vector_codec_test.go` | new | Round-trip + error tests for codec |
| `internal/store/sqlite.go` | modify | Add `character_memory` table to bootstrap; add `DB()` and `LookupByIDs` |
| `internal/store/sqlite_test.go` | modify | Add `LookupByIDs` tests |
| `internal/store/vector_sqlite.go` | new | `SQLiteVectorStore{Save, Load}` |
| `internal/store/vector_sqlite_test.go` | new | Round-trip, ordering, model-mismatch tests |
| `internal/memory/persister.go` | new | `VectorStore`, `EmbeddingRow`, `EventLookup`, `Option`, `WithPersister` |
| `internal/memory/embedded.go` | modify | Variadic `Option` constructor; `Hydrate`; `Save` call inside `Record` |
| `internal/memory/embedded_test.go` | modify | Add tests for hydrate, embed-nil-skip, zero-ID-skip, ordering |
| `internal/llm/gemini/gemini.go` | modify | Export `EmbeddingModelID` constant |
| `cmd/sim/echo_llm.go` | modify | Add `echoEmbeddingModelID` constant |
| `cmd/sim/llm_select.go` | modify | Return `(llm.LLM, modelID string, error)` |
| `cmd/sim/main.go` | modify | Construct `SQLiteVectorStore`, `Hydrate` each character before `w.Run`, propagate errors |
| `cmd/sim/smoke_test.go` | modify | Hydrate-failure aborts boot test |

---

## Pre-flight verification

- [ ] **Step 0.1: Confirm the only `Memory.Record` callsite obeys the call-order contract**

Run:
```bash
grep -rn "Memory.Record\|\.Memory\.Record" --include='*.go' .
```

Expected: a single hit at `internal/character/decide.go:29`. That callsite receives a `Perception` whose `Event` was already passed through `store.Append` in `internal/world/world.go` (search for `"Hard rule: append BEFORE broadcast"`). The ID is therefore populated before `Memory.Record` is called.

If a new callsite appears that does **not** go through `Append` first, document it in the PR description and either (a) route it through `Append` before this PR lands, or (b) construct that character's `Memory` without `WithPersister`.

- [ ] **Step 0.2: Confirm `internal/llm/gemini/gemini.go` is where the Gemini embedding model is named**

Run:
```bash
grep -n "text-embedding\|EmbedText\|embedModel\|Embedding" internal/llm/gemini/gemini.go
```

Expected: at least one mention of the embedding model name (e.g. `"text-embedding-004"`). Note the exact constant or string literal — Task 7 needs it.

---

## Task 1: Vector blob codec

**Files:**
- Create: `internal/store/vector_codec.go`
- Test: `internal/store/vector_codec_test.go`

- [ ] **Step 1.1: Write the failing tests**

Create `internal/store/vector_codec_test.go`:

```go
package store

import (
	"math"
	"testing"
)

func TestVecToBlobRoundTrip(t *testing.T) {
	vec := []float32{0, 1, -1, 3.14159, -2.71828, math.MaxFloat32, math.SmallestNonzeroFloat32}
	b := vecToBlob(vec)
	if got, want := len(b), len(vec)*4; got != want {
		t.Fatalf("blob length = %d, want %d", got, want)
	}
	got, err := blobToVec(b, len(vec))
	if err != nil {
		t.Fatalf("blobToVec: %v", err)
	}
	if len(got) != len(vec) {
		t.Fatalf("decoded len = %d, want %d", len(got), len(vec))
	}
	for i := range vec {
		if math.Float32bits(got[i]) != math.Float32bits(vec[i]) {
			t.Errorf("[%d] = %v, want %v", i, got[i], vec[i])
		}
	}
}

func TestVecToBlobRoundTripNaN(t *testing.T) {
	nan := float32(math.NaN())
	got, err := blobToVec(vecToBlob([]float32{nan}), 1)
	if err != nil {
		t.Fatalf("blobToVec: %v", err)
	}
	if !math.IsNaN(float64(got[0])) {
		t.Fatalf("expected NaN, got %v", got[0])
	}
}

func TestBlobToVecRejectsBadLength(t *testing.T) {
	_, err := blobToVec([]byte{0, 0, 0}, 1) // 3 bytes, dim=1 → expects 4
	if err == nil {
		t.Fatalf("expected error for short blob")
	}
}

func TestBlobToVecAcceptsZeroDim(t *testing.T) {
	got, err := blobToVec(nil, 0)
	if err != nil {
		t.Fatalf("blobToVec(nil, 0): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}
```

- [ ] **Step 1.2: Run tests, verify they fail**

Run:
```bash
go test ./internal/store/ -run 'TestVecToBlob|TestBlobToVec' -v
```

Expected: FAIL — undefined: `vecToBlob`, `blobToVec`.

- [ ] **Step 1.3: Implement the codec**

Create `internal/store/vector_codec.go`:

```go
package store

import (
	"encoding/binary"
	"fmt"
	"math"
)

// vecToBlob encodes a float32 vector as a binary.LittleEndian byte slice.
// No header is written; dim is stored in its own column on retrieval.
func vecToBlob(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// blobToVec decodes dim float32 values from b. Returns an error if the byte
// length is not exactly dim*4. NaN values round-trip bit-exactly.
func blobToVec(b []byte, dim int) ([]float32, error) {
	if len(b) != dim*4 {
		return nil, fmt.Errorf("vector blob length %d does not match dim %d (expected %d)", len(b), dim, dim*4)
	}
	out := make([]float32, dim)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}
```

- [ ] **Step 1.4: Run tests, verify they pass**

Run:
```bash
go test ./internal/store/ -run 'TestVecToBlob|TestBlobToVec' -v
```

Expected: PASS on all four tests (`TestVecToBlobRoundTrip`, `TestVecToBlobRoundTripNaN`, `TestBlobToVecRejectsBadLength`, `TestBlobToVecAcceptsZeroDim`).

- [ ] **Step 1.5: Commit**

```bash
git add internal/store/vector_codec.go internal/store/vector_codec_test.go
git commit -m "store: add little-endian float32 vector codec"
```

---

## Task 2: Schema bump + `DB()` + `LookupByIDs` on `SQLiteStore`

**Files:**
- Modify: `internal/store/sqlite.go`
- Modify: `internal/store/sqlite_test.go`

- [ ] **Step 2.1: Write the failing tests**

Append to `internal/store/sqlite_test.go`:

```go
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
		if err := st.Append(ctx, &ev); err != nil {
			t.Fatalf("Append: %v", err)
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
```

If `strconv` is not already imported in `sqlite_test.go`, add it to the import block.

- [ ] **Step 2.2: Run tests, verify they fail**

Run:
```bash
go test ./internal/store/ -run 'TestLookupByIDs|TestDBAccessor|TestCharacterMemoryTableExists' -v
```

Expected: FAIL — undefined methods `LookupByIDs`, `DB`, and missing table.

- [ ] **Step 2.3: Extend the schema and add the methods**

Edit `internal/store/sqlite.go`. Replace the `schema` constant:

```go
const schema = `
CREATE TABLE IF NOT EXISTS events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    ts_ns     INTEGER NOT NULL,
    source    TEXT    NOT NULL,
    scene_id  TEXT    NOT NULL,
    actor     TEXT    NOT NULL,
    kind      TEXT    NOT NULL,
    payload   BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts_ns);
CREATE INDEX IF NOT EXISTS idx_events_scene ON events(scene_id);

CREATE TABLE IF NOT EXISTS character_memory (
    character_id TEXT    NOT NULL,
    event_id     INTEGER NOT NULL,
    model_id     TEXT    NOT NULL,
    dim          INTEGER NOT NULL,
    embedding    BLOB    NOT NULL,
    recorded_ns  INTEGER NOT NULL,
    PRIMARY KEY (character_id, event_id, model_id)
);
CREATE INDEX IF NOT EXISTS idx_character_memory_owner_model_ts
    ON character_memory(character_id, model_id, recorded_ns DESC, event_id DESC);
`
```

Append two methods to `internal/store/sqlite.go` (after `Close`):

```go
// DB returns the underlying *sql.DB so secondary stores (e.g. SQLiteVectorStore)
// can share the same connection pool. Encapsulation break tolerated because
// there is exactly one in-process consumer.
func (s *SQLiteStore) DB() *sql.DB { return s.db }

// LookupByIDs returns the events whose IDs are listed. Missing IDs are
// silently omitted; the returned slice's length may be less than len(ids).
// Order is ascending event ID for deterministic test assertions.
func (s *SQLiteStore) LookupByIDs(ctx context.Context, ids []EventID) ([]Event, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]byte, 0, len(ids)*2)
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args = append(args, int64(id))
	}
	q := `SELECT id, ts_ns, source, scene_id, actor, kind, payload
	      FROM events WHERE id IN (` + string(placeholders) + `) ORDER BY id ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var (
			id             int64
			tsNs           int64
			src, sid, kind string
			actor          string
			pload          []byte
		)
		if scanErr := rows.Scan(&id, &tsNs, &src, &sid, &actor, &kind, &pload); scanErr != nil {
			return nil, scanErr
		}
		out = append(out, Event{
			ID:        EventID(id),
			Timestamp: time.Unix(0, tsNs).UTC(),
			Source:    Source(src),
			SceneID:   api.SceneID(sid),
			Actor:     actor,
			Kind:      Kind(kind),
			Payload:   append([]byte(nil), pload...),
		})
	}
	return out, rows.Err()
}
```

- [ ] **Step 2.4: Run tests, verify they pass**

Run:
```bash
go test ./internal/store/ -v
```

Expected: PASS on the new tests *and* all pre-existing tests in the package.

- [ ] **Step 2.5: Commit**

```bash
git add internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "store: add character_memory schema, DB() accessor, LookupByIDs"
```

---

## Task 3: `SQLiteVectorStore.Save` and `Load`

**Files:**
- Create: `internal/store/vector_sqlite.go`
- Create: `internal/store/vector_sqlite_test.go`

- [ ] **Step 3.1: Write the failing tests**

The production API uses `SaveArgs` / `LoadedRow` types defined in `internal/store/vector_sqlite.go` (Step 3.3). Plain-field types — not `memory.EmbeddingRow` — because `store/` must not import `memory/`. The adapter at the seam lives in `internal/memory/` (Task 5).

Create `internal/store/vector_sqlite_test.go`:

```go
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
```

- [ ] **Step 3.2: Run tests, verify they fail**

Run:
```bash
go test ./internal/store/ -run 'TestSQLiteVectorStore' -v
```

Expected: FAIL — undefined `NewSQLiteVectorStore`, `SaveArgs`, `LoadedRow`.

- [ ] **Step 3.3: Implement `SQLiteVectorStore`**

Create `internal/store/vector_sqlite.go`:

```go
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
)

// SaveArgs is the input to SQLiteVectorStore.Save. Plain fields, no
// dependency on internal/memory — the memory package adapts its own
// EmbeddingRow into this shape at the seam.
type SaveArgs struct {
	Owner     api.CharacterID
	ModelID   string
	EventID   EventID
	Embedding []float32
	Recorded  time.Time
}

// LoadedRow is the output of SQLiteVectorStore.Load. Same shape as SaveArgs.
type LoadedRow = SaveArgs

// SQLiteVectorStore persists embedding rows in a single SQLite database,
// sharing the *sql.DB of the host SQLiteStore. Safe for concurrent use.
type SQLiteVectorStore struct {
	db *sql.DB
}

// NewSQLiteVectorStore wraps an existing *sql.DB. The character_memory
// schema is bootstrapped by OpenSQLite.
func NewSQLiteVectorStore(db *sql.DB) *SQLiteVectorStore {
	return &SQLiteVectorStore{db: db}
}

// Save inserts one embedding row. Re-saving the same (owner, event_id,
// model_id) triple is an error (primary key conflict).
func (v *SQLiteVectorStore) Save(ctx context.Context, a SaveArgs) error {
	_, err := v.db.ExecContext(ctx,
		`INSERT INTO character_memory
		 (character_id, event_id, model_id, dim, embedding, recorded_ns)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		string(a.Owner), int64(a.EventID), a.ModelID, len(a.Embedding),
		vecToBlob(a.Embedding), a.Recorded.UnixNano(),
	)
	if err != nil {
		return fmt.Errorf("save embedding: %w", err)
	}
	return nil
}

// Load returns rows for (owner, modelID) ordered by recorded_ns DESC,
// event_id DESC. limit > 0 caps the result; limit <= 0 means unbounded.
// Rows whose blob fails to decode are skipped with a warn log; other rows
// are returned normally.
func (v *SQLiteVectorStore) Load(ctx context.Context, owner api.CharacterID, modelID string, limit int) ([]LoadedRow, error) {
	const base = `SELECT event_id, dim, embedding, recorded_ns FROM character_memory
		 WHERE character_id = ? AND model_id = ?
		 ORDER BY recorded_ns DESC, event_id DESC`
	args := []any{string(owner), modelID}
	q := base
	if limit > 0 {
		q = base + ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := v.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("load embeddings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []LoadedRow
	for rows.Next() {
		var (
			eventID int64
			dim     int
			blob    []byte
			tsNs    int64
		)
		if err := rows.Scan(&eventID, &dim, &blob, &tsNs); err != nil {
			return nil, err
		}
		vec, err := blobToVec(blob, dim)
		if err != nil {
			slog.Default().Warn("vector blob decode failed; row skipped",
				"character", owner, "event_id", eventID, "err", err)
			continue
		}
		out = append(out, LoadedRow{
			Owner:     owner,
			ModelID:   modelID,
			EventID:   EventID(eventID),
			Embedding: vec,
			Recorded:  time.Unix(0, tsNs).UTC(),
		})
	}
	return out, rows.Err()
}
```

- [ ] **Step 3.4: Run tests, verify they pass**

Run:
```bash
go test ./internal/store/ -v
```

Expected: PASS on all three new tests and all prior tests.

- [ ] **Step 3.5: Commit**

```bash
git add internal/store/vector_sqlite.go internal/store/vector_sqlite_test.go
git commit -m "store: add SQLiteVectorStore for embedding persistence"
```

---

## Task 4: `memory.VectorStore` interface, `EmbeddingRow`, `EventLookup`, options

**Files:**
- Create: `internal/memory/persister.go`

This task has no test of its own — it defines types that Tasks 5–6 exercise. Skipping straight to implementation is correct here; the next task's tests cover the surface.

- [ ] **Step 4.1: Create `internal/memory/persister.go`**

```go
package memory

import (
	"context"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/store"
)

// VectorStore persists per-character embeddings keyed by event ID.
// Implementations must be safe for concurrent use across character
// goroutines. The interface intentionally does not join to events;
// Embedded.Hydrate resolves event payloads via EventLookup.
type VectorStore interface {
	Save(ctx context.Context, row EmbeddingRow) error
	Load(ctx context.Context, owner api.CharacterID, modelID string, limit int) ([]EmbeddingRow, error)
}

// EmbeddingRow is the unit of persistence. EventID is a foreign-key
// reference into the event log; the event itself is not denormalized here.
type EmbeddingRow struct {
	Owner     api.CharacterID
	ModelID   string
	EventID   store.EventID
	Embedding []float32
	Recorded  time.Time
}

// EventLookup is the small slice of EventStore that Hydrate needs.
// *store.SQLiteStore satisfies it structurally.
type EventLookup interface {
	LookupByIDs(ctx context.Context, ids []store.EventID) ([]store.Event, error)
}

// Option configures Embedded at construction time.
type Option func(*Embedded)

// WithPersister wires a VectorStore for save-on-Record and load-at-Hydrate.
// owner identifies the character whose memory this is; modelID identifies
// the embedding model and is stored on every row so multiple model
// generations can coexist.
func WithPersister(vs VectorStore, owner api.CharacterID, modelID string) Option {
	return func(m *Embedded) {
		m.persister = vs
		m.owner = owner
		m.modelID = modelID
	}
}
```

- [ ] **Step 4.2: Verify it compiles**

Run:
```bash
go build ./internal/memory/
```

Expected: build error — `m.persister`, `m.owner`, `m.modelID` are not yet fields on `Embedded`. We add them in Task 5.

If the only errors are about those three fields, this is the expected intermediate state.

- [ ] **Step 4.3: DO NOT COMMIT — build is intentionally broken**

> **STOP.** Do not run `git commit` for this task. The fields `persister`, `owner`, `modelID` are referenced in `persister.go` but not yet declared on the `Embedded` struct. The single commit at Step 5.5 will land `persister.go`, the `Embedded` struct update, and the tests together. If you commit here, the next git bisect will pick a broken tree.

---

## Task 5: `Embedded` constructor options + `Hydrate`

**Files:**
- Modify: `internal/memory/embedded.go`
- Modify: `internal/memory/embedded_test.go`

- [ ] **Step 5.1: Write the failing test for Hydrate**

Append to `internal/memory/embedded_test.go`. (Read existing test file first to confirm imports and the `fakeEmbedder` shape; tests below assume `fakeEmbedder` returns deterministic vectors per text.)

```go
func TestEmbeddedHydratePopulatesEntries(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil { t.Fatalf("OpenSQLite: %v", err) }
	t.Cleanup(func() { _ = st.Close() })
	vs := store.NewSQLiteVectorStore(st.DB())

	owner := api.CharacterID("alice")
	model := "test:m"

	// Seed: append three events and corresponding vector rows directly.
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

	mem := NewEmbedded(&fakeEmbedder{}, 10, WithPersister(vectorStoreAdapter{vs}, owner, model))
	if err := mem.Hydrate(ctx, st); err != nil { t.Fatalf("Hydrate: %v", err) }

	// All three events should be present, in oldest-first order.
	if got := len(mem.entries); got != 3 {
		t.Fatalf("entries len = %d, want 3", got)
	}
	for i := 0; i < 3; i++ {
		wantTs := now.Add(time.Duration(i) * time.Second)
		// Spec test #10: bit-stable timestamp round-trip via time.Equal.
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
		WithPersister(vectorStoreAdapter{vs}, api.CharacterID("alice"), "m"))
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

	mem := NewEmbedded(&fakeEmbedder{}, 10, WithPersister(vectorStoreAdapter{vs}, owner, model))
	if err := mem.Hydrate(ctx, st); err != nil { t.Fatal(err) }
	if len(mem.entries) != 2 { t.Fatalf("first hydrate: len = %d", len(mem.entries)) }

	seed(2) // total 4 rows
	if err := mem.Hydrate(ctx, st); err != nil { t.Fatal(err) }
	if len(mem.entries) != 4 { t.Fatalf("second hydrate: len = %d, want 4 (replace, not append)", len(mem.entries)) }
}
```

The test references `vectorStoreAdapter` — a small bridge between `*store.SQLiteVectorStore` and the `memory.VectorStore` interface. Define it at the bottom of `embedded_test.go`:

```go
// vectorStoreAdapter wraps *store.SQLiteVectorStore (which speaks SaveArgs/
// LoadedRow) so it satisfies memory.VectorStore (which speaks EmbeddingRow).
type vectorStoreAdapter struct {
	inner *store.SQLiteVectorStore
}

func (a vectorStoreAdapter) Save(ctx context.Context, row EmbeddingRow) error {
	return a.inner.Save(ctx, store.SaveArgs{
		Owner: row.Owner, ModelID: row.ModelID, EventID: row.EventID,
		Embedding: row.Embedding, Recorded: row.Recorded,
	})
}

func (a vectorStoreAdapter) Load(ctx context.Context, owner api.CharacterID, modelID string, limit int) ([]EmbeddingRow, error) {
	rows, err := a.inner.Load(ctx, owner, modelID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]EmbeddingRow, len(rows))
	for i, r := range rows {
		out[i] = EmbeddingRow{
			Owner: r.Owner, ModelID: r.ModelID, EventID: r.EventID,
			Embedding: r.Embedding, Recorded: r.Recorded,
		}
	}
	return out, nil
}
```

The adapter is **also defined in production code** in this task — see step 5.3 — so the test imports it from `memory`. Move the adapter to `internal/memory/persister.go` and reference it from tests rather than defining it twice.

- [ ] **Step 5.2: Run tests, verify they fail**

Run:
```bash
go test ./internal/memory/ -run 'TestEmbeddedHydrate' -v
```

Expected: FAIL — undefined `Hydrate`, missing fields on `Embedded`, `vectorStoreAdapter` not found.

- [ ] **Step 5.3: Update `Embedded` and add the adapter**

Edit `internal/memory/embedded.go`. Add fields to the `Embedded` struct:

```go
type Embedded struct {
	model     llm.LLM
	cap       int
	lambda    float64
	tau       time.Duration
	now       func() time.Time
	entries   []memoryEntry
	// Persistence (optional; nil when WithPersister was not used).
	persister VectorStore
	owner     api.CharacterID
	modelID   string
}
```

Add `api` to the import block of `embedded.go`:

```go
import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/store"
)
```

Replace the `NewEmbedded` constructor:

```go
// NewEmbedded returns an Embedded store backed by the given LLM. cap <= 0
// disables the size cap. lambda/tau take defaults; override via SetRecencyParams.
// Pass options like WithPersister to configure persistence.
func NewEmbedded(model llm.LLM, cap int, opts ...Option) *Embedded {
	m := &Embedded{
		model:  model,
		cap:    cap,
		lambda: DefaultLambda,
		tau:    DefaultTau,
		now:    time.Now,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}
```

Append a `Hydrate` method:

```go
// Hydrate loads previously persisted embeddings for this character from the
// configured VectorStore (if any), resolves event payloads via the given
// EventLookup, and assigns them as the in-memory entries in oldest-first
// order. A second call replaces entries wholesale — do not call after Record.
//
// Returns the first error from Load or LookupByIDs. An empty Load result is
// treated as a fresh DB and yields nil.
func (m *Embedded) Hydrate(ctx context.Context, events EventLookup) error {
	if m.persister == nil {
		return nil
	}
	// m.cap is passed through verbatim. m.cap <= 0 means "unlimited," and
	// SQLiteVectorStore.Load treats that as "no LIMIT clause." No hidden cap.
	rows, err := m.persister.Load(ctx, m.owner, m.modelID, m.cap)
	if err != nil {
		return fmt.Errorf("hydrate load: %w", err)
	}
	if len(rows) == 0 {
		m.entries = nil
		return nil
	}
	ids := make([]store.EventID, len(rows))
	for i, r := range rows {
		ids[i] = r.EventID
	}
	evs, err := events.LookupByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("hydrate lookup: %w", err)
	}
	byID := make(map[store.EventID]store.Event, len(evs))
	for _, e := range evs {
		byID[e.ID] = e
	}

	// rows come newest-first; reverse into oldest-first.
	entries := make([]memoryEntry, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		ev, ok := byID[r.EventID]
		if !ok {
			slog.Default().Warn("hydrate: vector row references missing event",
				"character", m.owner, "event_id", r.EventID)
			continue
		}
		entries = append(entries, memoryEntry{
			event:     ev,
			embedding: r.Embedding,
			recorded:  r.Recorded,
		})
	}
	m.entries = entries
	return nil
}

```

Append `vectorStoreAdapter` to `internal/memory/persister.go` so tests and (future) production code share one definition. Add the import for `store` if not already present:

```go
// NewSQLiteVectorStoreAdapter wraps an existing *store.SQLiteVectorStore as a
// memory.VectorStore. The adapter exists because store/ cannot import memory/.
func NewSQLiteVectorStoreAdapter(inner *store.SQLiteVectorStore) VectorStore {
	return sqliteAdapter{inner: inner}
}

type sqliteAdapter struct {
	inner *store.SQLiteVectorStore
}

func (a sqliteAdapter) Save(ctx context.Context, row EmbeddingRow) error {
	return a.inner.Save(ctx, store.SaveArgs{
		Owner: row.Owner, ModelID: row.ModelID, EventID: row.EventID,
		Embedding: row.Embedding, Recorded: row.Recorded,
	})
}

func (a sqliteAdapter) Load(ctx context.Context, owner api.CharacterID, modelID string, limit int) ([]EmbeddingRow, error) {
	rows, err := a.inner.Load(ctx, owner, modelID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]EmbeddingRow, len(rows))
	for i, r := range rows {
		out[i] = EmbeddingRow{
			Owner: r.Owner, ModelID: r.ModelID, EventID: r.EventID,
			Embedding: r.Embedding, Recorded: r.Recorded,
		}
	}
	return out, nil
}
```

Now the test file references the production adapter:

```go
mem := NewEmbedded(&fakeEmbedder{}, 10, WithPersister(
    NewSQLiteVectorStoreAdapter(vs), owner, model))
```

Replace all `vectorStoreAdapter{vs}` calls in the new tests with `NewSQLiteVectorStoreAdapter(vs)` and **delete** the test-local `vectorStoreAdapter` definition. Add the needed imports to the test file:

```go
import (
	// ...existing imports...
	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/store"
)
```

- [ ] **Step 5.4: Run tests, verify they pass**

Run:
```bash
go test ./internal/memory/ -v
```

Expected: PASS on all new and existing tests.

- [ ] **Step 5.5: Commit**

```bash
git add internal/memory/persister.go internal/memory/embedded.go internal/memory/embedded_test.go
git commit -m "memory: add VectorStore, EmbeddingRow, Options, Hydrate"
```

---

## Task 6: `Embedded.Record` persistence integration

**Files:**
- Modify: `internal/memory/embedded.go`
- Modify: `internal/memory/embedded_test.go`

- [ ] **Step 6.1: Write the failing tests**

The existing `fakeEmbedder` in `embedded_test.go` returns nil vectors unless its `vectors` map is seeded — that would let the zero-embedding skip path mask the assertions under test. Use a dedicated `vectorEmbedder` that always returns a non-empty vector.

Append to `internal/memory/embedded_test.go`:

```go
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
```

If `errors` is not already imported in `embedded_test.go`, add it.

- [ ] **Step 6.2: Run tests, verify they fail**

Run:
```bash
go test ./internal/memory/ -run 'TestRecord' -v
```

Expected: FAIL — Save is never called by the current `Record` implementation.

- [ ] **Step 6.3: Update `Record` to persist**

Edit `internal/memory/embedded.go`. Replace the existing `Record` method with:

```go
// Record embeds the event's text (if any), appends it in-memory, and, if a
// persister is configured, also writes one row to the VectorStore.
//
// Failure semantics:
//   - Embedding failure: event appended with nil embedding; embed error
//     returned. Save is NOT called (no vector to save).
//   - Save failure: in-memory append stands; save error is joined with any
//     embed error via errors.Join and returned. Record is one-shot — callers
//     do not retry, since retrying would duplicate the in-memory entry and
//     hit a PK conflict on the second Save.
//   - ev.ID == 0 with persister configured: Save skipped, logged at debug.
//     Caller is expected to have Appended the event before calling Record.
func (m *Embedded) Record(ctx context.Context, ev store.Event) error {
	entry := memoryEntry{event: ev, recorded: m.timestamp(ev)}
	text := store.TextOf(ev)

	var embedErr error
	if strings.TrimSpace(text) != "" {
		vec, err := m.model.EmbedText(ctx, text)
		if err != nil {
			embedErr = fmt.Errorf("embed: %w", err)
		} else {
			entry.embedding = vec
		}
	}

	m.entries = append(m.entries, entry)
	if m.cap > 0 && len(m.entries) > m.cap {
		m.entries = m.entries[len(m.entries)-m.cap:]
	}

	var saveErr error
	if m.persister != nil && len(entry.embedding) > 0 {
		if ev.ID == 0 {
			slog.Default().Debug("memory: skipping Save for event with zero ID",
				"character", m.owner)
		} else {
			if err := m.persister.Save(ctx, EmbeddingRow{
				Owner: m.owner, ModelID: m.modelID, EventID: ev.ID,
				Embedding: entry.embedding, Recorded: entry.recorded,
			}); err != nil {
				slog.Default().Warn("memory: persister Save failed",
					"character", m.owner, "event_id", ev.ID, "err", err)
				saveErr = fmt.Errorf("persist: %w", err)
			}
		}
	}

	return errors.Join(embedErr, saveErr)
}
```

Add `errors` to the import block of `embedded.go`:

```go
import (
	"context"
	"errors"
	"fmt"
	// ...
)
```

- [ ] **Step 6.4: Run tests, verify they pass**

Run:
```bash
go test ./internal/memory/ -v
```

Expected: PASS on all `TestRecord*` and all previously passing tests in the package.

- [ ] **Step 6.5: Commit**

```bash
git add internal/memory/embedded.go internal/memory/embedded_test.go
git commit -m "memory: persist embeddings in Record when persister configured"
```

---

## Task 7: Model ID constants and `llm_select` returns the ID

**Files:**
- Modify: `internal/llm/gemini/gemini.go`
- Modify: `cmd/sim/echo_llm.go`
- Modify: `cmd/sim/llm_select.go`
- Modify: `cmd/sim/runtime_config_test.go` (if it asserts `selectLLM`'s signature)

- [ ] **Step 7.1: Inspect the Gemini embedding model constant**

Run:
```bash
grep -n 'embedding\|Embedding\|text-embedding' internal/llm/gemini/gemini.go
```

Note the exact model string used in `EmbedText` (e.g. `"text-embedding-004"`).

- [ ] **Step 7.2: Add the Gemini constant**

Edit `internal/llm/gemini/gemini.go`. Near the top, after the package declaration:

```go
// EmbeddingModelID is the stable namespaced identifier persisted alongside
// each embedding row. It is NOT the wire-level model string (that lives in
// DefaultEmbeddingModel) — the "gemini:" prefix exists so multiple providers
// can never collide on the same model name. Change this constant when the
// underlying embedding model changes; existing rows with stale IDs will be
// filtered out at hydrate time until the operator deletes them.
const EmbeddingModelID = "gemini:text-embedding-004"
```

If `DefaultEmbeddingModel` in `gemini.go` differs from `text-embedding-004`, update the suffix of `EmbeddingModelID` to match — but always keep the `gemini:` prefix.

- [ ] **Step 7.3: Add the echo constant**

Edit `cmd/sim/echo_llm.go`. After the package declaration:

```go
// echoEmbeddingModelID identifies the (non-)embedding emitted by echoLLM.
// Distinct from any real provider so test rows cannot be confused with
// production rows during local development.
const echoEmbeddingModelID = "echo:none"
```

- [ ] **Step 7.4: Update `selectLLM` signature**

Edit `cmd/sim/llm_select.go`. Change the function signature and bodies:

```go
// selectLLM returns the llm.LLM implementation requested by --llm along with
// its embedding model identifier (used to scope persisted vector rows).
func selectLLM(provider, geminiModel string) (llm.LLM, string, error) {
	switch provider {
	case "echo":
		return echoLLM{}, echoEmbeddingModelID, nil
	case "gemini":
		key := os.Getenv("GEMINI_API_KEY")
		if key == "" {
			return nil, "", errors.New("set GEMINI_API_KEY to use --llm=gemini")
		}
		g := gemini.New(key)
		if geminiModel != "" {
			g.Model = geminiModel
		}
		return g, gemini.EmbeddingModelID, nil
	default:
		return nil, "", fmt.Errorf("unknown --llm provider %q (want: echo, gemini)", provider)
	}
}
```

- [ ] **Step 7.5: Update `cmd/sim/gemini_smoke_test.go`**

This file has a direct `selectLLM` call (~line 36): `model, err := selectLLM("gemini", modelName)`. With Task 7.4's signature change it must become:

```go
model, _, err := selectLLM("gemini", modelName)
```

`runtime_config_test.go` does **not** call `selectLLM`; leave it alone.

- [ ] **Step 7.6: Build and confirm only main.go is broken**

Run:
```bash
go build ./...
```

Expected: a compile error **only** in `cmd/sim/main.go` (`selectLLM` returns three values; main captures two). Task 8 fixes this. If any other file errors, find it and update the callsite the same way as Step 7.5 before moving on.

- [ ] **Step 7.7: Commit**

```bash
git add internal/llm/gemini/gemini.go cmd/sim/echo_llm.go cmd/sim/llm_select.go cmd/sim/gemini_smoke_test.go
git commit -m "llm: expose per-provider EmbeddingModelID; selectLLM returns it"
```

Note: this commit leaves `cmd/sim/main.go` with a compile error. Task 8's first commit closes that gap. Two commits, one transient broken build — acceptable for two reasons: (a) the break is localized to one file you're about to rewrite, (b) splitting the LLM-interface change from the wiring keeps the diff readable. If you prefer no broken intermediate state, defer this commit and bundle Task 7+8 as one commit at the end of Task 8.

---

## Task 8: Wire `SQLiteVectorStore` and `Hydrate` into `cmd/sim/main.go`

**Files:**
- Modify: `cmd/sim/main.go`
- Modify: `cmd/sim/smoke_test.go`

The existing `run()` swallows context creation inside its own body (`signal.NotifyContext`). That makes it untestable from a goroutine that wants to control the lifetime. The refactor splits `run` into two functions: `runCtx` (does the work, takes a ctx) and `run` (the thin signal-aware wrapper). Tests call `runCtx` directly.

A seam parameter `vsFactory` is added so a test can inject a failing `VectorStore` to drive the hydrate-abort assertion.

- [ ] **Step 8.1: Refactor `cmd/sim/main.go` to split `runCtx` and `run`**

Edit `cmd/sim/main.go`. Add types and helpers near the top of the file (after imports):

```go
// vectorStoreFactory builds the memory.VectorStore used by run/runCtx.
// Tests inject a failing factory to drive the hydrate-abort assertion.
type vectorStoreFactory func(*store.SQLiteStore) memory.VectorStore

// defaultVectorStore is the production wiring: a SQLite-backed VectorStore
// sharing the SQLiteStore's *sql.DB.
func defaultVectorStore(st *store.SQLiteStore) memory.VectorStore {
	return memory.NewSQLiteVectorStoreAdapter(store.NewSQLiteVectorStore(st.DB()))
}
```

Replace the existing `run` function with this pair. The body of `runCtx` is the old `run`'s body minus the `signal.NotifyContext` line, plus character-construction changes:

```go
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
	if err := config.Validate(chars, groups); err != nil {
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
```

Update `main()` to capture the model ID and pass it through:

```go
model, modelID, err := selectLLM(opts.LLMProvider, opts.GeminiModel)
if err != nil {
	logger.Error("llm select", "err", err)
	os.Exit(1)
}

if err := run(logger, model, modelID, opts.DBPath, opts.SeedDir, opts.Tick,
	ircConfig(opts.IRC, logger)); err != nil {
	logger.Error("fatal", "err", err)
	os.Exit(1)
}
```

- [ ] **Step 8.2: Verify the build is restored**

Run:
```bash
go build ./...
```

Expected: clean build. The compile error from Task 7.7 is now closed.

- [ ] **Step 8.3: Write the failing hydrate-abort smoke test**

Append to `cmd/sim/smoke_test.go`:

```go
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
	seedDir := filepath.Join("..", "..", "seed") // same convention as existing smoke tests

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
```

Required imports in the test file (add any missing): `context`, `errors`, `io`, `log/slog`, `path/filepath`, `strings`, `testing`, `time`, plus the project's `api`, `memory`, `store` packages.

- [ ] **Step 8.4: Run the test, verify it passes (green directly)**

Because Step 8.1 already wired `runCtx` with the propagating `Hydrate` error, this test should pass on first run. The "failing test → green" cycle for this task is genuinely the refactor itself — Task 8 is more refactor than fresh feature. Acceptable.

Run:
```bash
go test ./cmd/sim/ -run TestRunCtxAbortsBootOnHydrateFailure -v
```

Expected: PASS. The test exercises the `Hydrate` failure path and asserts the error message contains "hydrate".

If it fails, the most likely cause is that `runCtx` is returning an earlier error (e.g. seed dir not found). Run with `-v` and inspect the error string.

- [ ] **Step 8.5: Write the hydrate-roundtrip integration test**

This is what was previously a flaky manual shell check. As an automated Go test it uses the real world tick path: inject one event via the world API, kill the context, then reopen the DB and assert `character_memory` is non-empty. Then re-open with a fresh `Embedded`, hydrate, and assert entries land.

Append to `cmd/sim/smoke_test.go`:

```go
func TestRunCtxPersistsAndHydratesRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbPath := filepath.Join(t.TempDir(), "rt.db")
	seedDir := filepath.Join("..", "..", "seed")

	// Phase 1: boot, inject, kill.
	{
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()

		// We need a handle to the WorldAPI from inside runCtx so we can inject.
		// runCtx does not expose one; instead, spin up the world inline using
		// the same building blocks runCtx uses. This is essentially what the
		// existing TestSmokeEndToEnd does — reuse that pattern.
		t.Fatal("Phase 1 inlined-world inject not yet written; see Step 8.5 note below.")
		_ = ctx
		_ = logger
		_ = dbPath
		_ = seedDir
	}
}
```

**Note on Step 8.5**: writing the round-trip integration test cleanly requires either (a) exposing a `WorldAPI` accessor from `runCtx` (intrusive) or (b) inlining the world build like `TestSmokeEndToEnd` already does. Option (b) is the pragmatic move — copy `TestSmokeEndToEnd`'s setup, add the `WithPersister` + `Hydrate` calls, inject one event via `worldAPI.InjectEvent`, sleep one tick, close the store, then open a second `Embedded` against the same DB and assert `len(mem.entries) > 0` after Hydrate.

If this becomes too large in this PR, ship Step 8.5 as a stubbed `t.Skip("TODO: round-trip integration test")` and file a follow-up. The unit-level coverage already verifies (a) Record persists (Task 6) and (b) Hydrate loads (Task 5). The integration test confirms they meet in the middle through `cmd/sim`'s wiring; valuable but not load-bearing for the spec.

**Decision flag**: when executing this task, decide before starting Step 8.5 whether to (i) write the full round-trip test, (ii) ship it stubbed with `t.Skip` and a TODO, or (iii) skip it entirely. Mention the choice in the commit message at Step 8.7.

- [ ] **Step 8.6: Full test suite + race**

Run:
```bash
go test ./... -v
```

Expected: PASS on all tests including the new smoke. Then:

```bash
go test ./... -race
```

Expected: PASS with `-race`. No data races — `Hydrate` runs serially before `w.Run` launches; one character goroutine per `Embedded`; `*sql.DB` is concurrent-safe.

- [ ] **Step 8.7: Commit**

```bash
git add cmd/sim/main.go cmd/sim/smoke_test.go
git commit -m "sim: split runCtx out of run; hydrate every character before world tick"
```

If you deferred the Task 7 commit (per the note at Step 7.7), include those files here in a single squashed commit instead and adjust the commit message accordingly.

---

## Final verification

- [ ] **Step F.1: Full test suite**

Run:
```bash
go test ./... -race
```

Expected: PASS with `-race`. No data races, since the documented contract is "Hydrate before Run" and one character goroutine per `Embedded`.

- [ ] **Step F.2: Lint**

Run:
```bash
golangci-lint run ./...
```

Expected: no findings, or only findings unrelated to this PR. (If `.golangci.yml` is strict about long files or comment style, address inline.)

- [ ] **Step F.3: Confirm spec coverage**

Walk the spec's "Files" section. For each entry, point to a task that touches it. Any gaps mean a missed requirement.

---

## Self-review notes (post-review revisions)

- **Spec coverage**: all file entries in the spec map to Tasks 1–8. Hydrate semantics, call-order contract, model-ID handling, blob codec, schema, ordering, and spec tests 1–11 are covered.
- **Test 10** (bit-stable timestamp round-trip): `TestEmbeddedHydratePopulatesEntries` now asserts `time.Equal` at full precision on both `recorded` and `event.Timestamp`. ✓
- **Test 11** (deterministic ordering on equal `recorded_ns`): `TestSQLiteVectorStoreDeterministicOrderOnTies`. ✓
- **Embedder fakes for Task 6**: `vectorEmbedder` (always-vector) and `errEmbedder` (always-fail). The persistence assertions exercise the persister path rather than the empty-embedding skip. The zero-ID test uses `vectorEmbedder` to isolate the zero-ID guard from the empty-embedding guard.
- **Unbounded cap**: `Embedded.cap <= 0` flows through to `SQLiteVectorStore.Load(limit=0)` which omits the `LIMIT` clause. No hidden cap. Verified by `TestSQLiteVectorStoreUnboundedLimit`.
- **Type consistency**:
  - `memory.EmbeddingRow` used throughout `memory/` and `cmd/sim/`.
  - `store.SaveArgs` (alias `LoadedRow = SaveArgs`) used inside `store/` only; never crosses into `memory/`.
  - `vectorStoreFactory` and `defaultVectorStore` are the seam for the smoke test.
  - `selectLLM` returns three values; both callers updated (`main` in Task 8, `gemini_smoke_test.go` in Task 7.5).
- **Build state across commits**:
  - Tasks 1, 2, 3, 5, 6: green at commit.
  - Task 4: explicit no-commit step.
  - Task 7.7: leaves `cmd/sim/main.go` with a known compile error, closed by Task 8.1. Plan flags this and offers the bundle-into-one-commit alternative.
- **Task 8.5 round-trip integration test**: scoped as optional with explicit decision points. Unit coverage on either side of the seam already proves Save and Hydrate work; the integration test is confidence-building, not gating.
- **Placeholders**: none in code blocks. The Step 8.5 stub is intentionally a `t.Fatal` placeholder so the executor decides — that's a design call, not a plan failure.

Plan complete (revised).
