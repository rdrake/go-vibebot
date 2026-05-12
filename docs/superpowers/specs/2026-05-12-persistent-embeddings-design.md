# Persistent embeddings with swappable backend

Date: 2026-05-12
Status: Approved (post-review revisions) — pending implementation plan
Backlog item: L1

## Problem

`memory.Embedded` holds per-character episodic memory (event + embedding + timestamp) in process memory only. A restart drops every embedding, forcing each character to "start over" socially even though the underlying events are already persisted in SQLite. With Gemini `text-embedding-004` costing fractions of a cent per call, paying once and re-using forever is the obvious win.

## Goal

Persist embeddings across restarts. Make the persistence layer easy to swap to another backend later without rewriting `Embedded`'s scoring logic or coupling it to the event store.

## Non-goals

- Automated migration between embedding models. The schema can hold multiple `model_id` generations side-by-side, but the operator runs `DELETE FROM character_memory WHERE model_id = ?` (or simply ignores stale rows) when retiring a model.
- Delegating retrieval (top-k similarity search) to a server-side vector database. That would replace `Embedded` itself, not this persistence layer; see "Future swap points" below.
- Per-character Hydrate timeouts. A stuck backend blocks boot today. Flagged as a follow-up.

## Architecture

A new `VectorStore` interface in `internal/memory/` decouples `Embedded` from any specific backend. Today's only implementation is SQLite, sharing the existing `*sql.DB` opened by `store.OpenSQLite`. Tomorrow's implementations could be filesystem blobs, S3, Redis — anything that can save and load `(owner, modelID, event_id, vector, timestamp)` tuples.

Critically, `VectorStore` is **only** responsible for persisting vectors keyed by event ID. It does **not** join to the event store. `Embedded.Hydrate` queries `EventStore` itself to materialize the events whose IDs the persister returns. This is what makes the abstraction actually portable: a filesystem-backed `VectorStore` does not have to reimplement event-store query semantics.

`Embedded` keeps doing in-process retrieval. It calls `VectorStore.Save` on every `Record` and `VectorStore.Load` once at boot via a new `Hydrate(ctx, events EventLookup)` method.

### Why the interface lives in `memory/`, not `store/`

`memory` is the consumer. Putting the interface there keeps the dependency arrow `memory → store` (the SQLite impl uses `*sql.DB` and knows nothing about `memory`) and avoids `store → memory`. The SQLite implementation in `store/` satisfies `memory.VectorStore` **structurally only** — its package does not import `memory`. Cross-package integration tests live in `internal/memory/` (or a new `internal/memory/sqlitevec/` package), never in `internal/store/`.

## Interface

```go
// internal/memory/persister.go

type VectorStore interface {
    Save(ctx context.Context, row EmbeddingRow) error
    Load(ctx context.Context, owner api.CharacterID, modelID string, limit int) ([]EmbeddingRow, error)
}

type EmbeddingRow struct {
    Owner     api.CharacterID
    ModelID   string         // e.g. "gemini:text-embedding-004"
    EventID   store.EventID  // foreign-key reference only; not a denormalized event
    Embedding []float32
    Recorded  time.Time
}

// EventLookup is the small slice of EventStore that Hydrate needs. EventStore
// satisfies it; tests can supply a fake without standing up a full store.
type EventLookup interface {
    LookupByIDs(ctx context.Context, ids []store.EventID) ([]store.Event, error)
}
```

`Load` returns rows in `Recorded` descending order, ties broken by `EventID` descending so ordering is deterministic across runs. Rows whose stored `model_id` does not equal the requested `modelID` are filtered server-side; the caller never sees them.

### Concurrency contract on `VectorStore`

`Save` and `Load` must be safe for concurrent calls from multiple goroutines. The SQLite impl gets this from `*sql.DB`. Future impls (filesystem, S3, Redis) must document and uphold the same property; otherwise they cannot be shared across the per-character goroutines that `Embedded` runs in.

## SQLite backend

New file `internal/store/vector_sqlite.go`. Schema is appended to the existing bootstrap string in `OpenSQLite` (so `CREATE TABLE IF NOT EXISTS` handles both fresh and existing `vibebot.db` files):

```sql
CREATE TABLE IF NOT EXISTS character_memory (
    character_id TEXT    NOT NULL,
    event_id     INTEGER NOT NULL,
    model_id     TEXT    NOT NULL,
    dim          INTEGER NOT NULL,
    embedding    BLOB    NOT NULL,   -- binary.LittleEndian float32 sequence, no header
    recorded_ns  INTEGER NOT NULL,
    PRIMARY KEY (character_id, event_id, model_id)
);
CREATE INDEX IF NOT EXISTS idx_character_memory_owner_model_ts
    ON character_memory(character_id, model_id, recorded_ns DESC, event_id DESC);
```

Notes:

- **PK includes `model_id`** so multiple model generations coexist without collision. Re-embedding under a new model writes new rows; old rows remain until the operator deletes them.
- **No `FOREIGN KEY`.** SQLite's modernc driver does not enable FK enforcement by default and we are not turning it on. The orphan case (vector row referencing a missing event) is handled at `Hydrate` time: the event lookup simply returns no event for that ID and the vector row is dropped with a warn log. The FK constraint would not buy us anything we don't already get.
- **Ordering**: `recorded_ns DESC, event_id DESC` for determinism.

### Encoding

Helpers `vecToBlob([]float32) []byte` and `blobToVec(b []byte, dim int) ([]float32, error)` live next to the SQLite impl. Encoding uses `binary.LittleEndian` and `math.Float32bits` / `math.Float32frombits` explicitly. `blobToVec` returns an error when `len(b) != dim*4`.

### Blob error handling (single rule)

When `Load` decodes rows, any row whose blob fails to decode is **skipped** (logged at `slog.Warn`, not returned in the result, not surfaced as an error to the caller). Other rows are returned normally. This is the only behavior; the failure-modes table below references this rule rather than restating it.

### Accessor

The SQLite store exposes its `*sql.DB` via a new `DB() *sql.DB` method on `SQLiteStore` so `NewSQLiteVectorStore(db)` can be constructed in `cmd/sim/main.go`. Pragmatic encapsulation break; acceptable given there is only one consumer.

## `Embedded` changes

Functional-option constructor so the original `NewEmbedded(model, cap)` signature stays usable and future persisters do not force a constructor explosion:

```go
type Option func(*Embedded)

func NewEmbedded(model llm.LLM, cap int, opts ...Option) *Embedded
func WithPersister(vs VectorStore, owner api.CharacterID, modelID string) Option
func (m *Embedded) Hydrate(ctx context.Context, events EventLookup) error
```

### Call-order contract for `Record`

`Memory.Record(ctx, ev)` requires `ev.ID != 0` when persistence is configured. Every callsite of `Memory.Record` in the codebase must follow a successful `EventStore.Append(ctx, &ev)` so that the autoincrement ID is populated before `Record` sees it. The implementation plan must include a grep-and-verify pass over every `Memory.Record(` callsite; sites that record events not destined for `EventStore` (if any) must not configure a persister.

If `ev.ID == 0` is encountered with persistence configured, `Record` logs at `slog.Debug` and skips the `Save` call. The event is still appended in-memory. This is the documented escape hatch, not a fallback to lean on.

### `Record` is one-shot, not retryable

`Record` appends to `m.entries` and then calls `vs.Save`. If `Save` fails:

- The in-memory append **stands**. We do not roll back the in-memory state.
- The `Save` error is returned (joined with any embedding error via `errors.Join`).
- The caller does not retry. `Record` is not idempotent across in-memory state; calling it twice for the same event produces two in-memory entries and, on the second attempt, a PK conflict in the SQLite backend.
- Practical implication: a failed `Save` means that one row is lost from persistence (it remains in-memory until the process exits). Acceptable: embeddings are derived data, and the event itself is still in the event store.

### `Hydrate`

`Hydrate(ctx, events)`:

1. If no persister is configured, return nil immediately.
2. Call `vs.Load(ctx, owner, modelID, m.cap)`. If this returns an error, return it. **Do not swallow.** Empty result + nil error means "fresh DB" — proceed; non-nil error means "something is wrong" — surface it to boot.
3. Collect the `EventID`s from the rows and call `events.LookupByIDs`. For any ID with no matching event, log a warn and drop that vector row.
4. Reverse the resulting slice so entries end up in oldest-first order (`Load` returns newest-first). Assign to `m.entries`.
5. Hydrate is **not** idempotent. A second call replaces `m.entries` wholesale; do not call it after `Record` has run.

## Model ID handling

Each LLM provider package exposes its embedding model identifier as an exported constant:

- `gemini.EmbeddingModelID = "gemini:text-embedding-004"` (or the actual model in use)
- A constant in `cmd/sim/` for the echo LLM: `echoEmbeddingModelID = "echo:none"`

`cmd/sim/llm_select.go` returns the model ID alongside the LLM impl when constructing one. The `llm.LLM` interface is unchanged — no `EmbeddingModelID()` method is added, so existing mocks (`fakeEmbedder` in `embedded_test.go`, etc.) keep working with no edits.

## Boot wiring

In `cmd/sim/main.go`, after opening the store and selecting the LLM, before constructing characters and before launching `w.Run`:

```go
vs := store.NewSQLiteVectorStore(st.DB())
llmImpl, modelID, err := selectLLM(opts.LLMProvider, opts.GeminiModel)
// ... existing error handling ...

for _, spec := range chars {
    id := api.CharacterID(spec.ID)
    mem := memory.NewEmbedded(llmImpl, 200,
        memory.WithPersister(vs, id, modelID))
    if err := mem.Hydrate(ctx, st); err != nil {
        return fmt.Errorf("hydrate memory for %s: %w", id, err)
    }
    byID[id] = &character.Character{
        ID: id, Name: spec.Name, /* ... */ Memory: mem,
        Inbox: make(chan character.Perception, 8),
    }
}

// All Hydrate calls have completed before this point.
go func() { worldErr <- w.Run(ctx) }()
```

Two guarantees the spec pins:

- All `Hydrate` calls complete before `w.Run` is launched. The world cannot tick (and therefore cannot dispatch events that trigger `Record`) before hydration is done.
- A non-nil `Hydrate` error aborts boot. We do not swallow it into a warn-and-continue. The operator sees the failure and decides whether to clear the DB or fix the backend.

## Failure modes

| Scenario | Behavior |
|---|---|
| Embedding API call fails | Existing behavior. Event appended in-memory with nil embedding; error returned. No persister call (nothing to save). |
| `vs.Save` fails | Logged at `slog.Warn`; error joined into `Record`'s return. In-memory append stands. Row lost from persistence. See "`Record` is one-shot, not retryable". |
| `vs.Load` fails at boot | Returned from `Hydrate`; boot aborts. Operator handles. |
| Empty `Load` result | Treated as fresh DB. `Hydrate` returns nil. Character starts with empty memory. |
| Model ID mismatch | `Load` filters server-side, returns only matching rows. No error. |
| Blob decode fails on a row | Row skipped, logged at warn. See "Blob error handling (single rule)". |
| Vector row references missing event (orphan) | `LookupByIDs` returns no event; row dropped at hydrate, logged at warn. |
| `ev.ID == 0` at `Record` time | Persistence skipped, logged at debug. In-memory append stands. |

## Concurrency

`Embedded` is single-goroutine (the owning character's). One shared `SQLiteVectorStore` is consumed by all character goroutines; `*sql.DB` is concurrent-safe so this is fine. The `VectorStore` interface's concurrency contract (above) requires future impls to provide the same guarantee.

`Hydrate` runs serially during boot, before any character goroutine starts. There is no Hydrate-vs-Record race because Record cannot be called before `w.Run` launches.

## Future swap points

This design has two distinct layers of swappability:

- **`VectorStore`** (this PR): vector persistence only — no join to events, no top-k. Backends: SQLite, files, S3, Redis. Retrieval stays in-process.
- **`memory.Store`** (future): the whole memory subsystem. If someone wants pgvector or Qdrant with server-side ANN, they write a new `memory.Store` implementation that delegates `Retrieve` to the remote service. `VectorStore` is not the right interface for that and must not grow a `SearchTopK` method.

Calling this out explicitly so we do not over-engineer `VectorStore` into a "real" vector DB interface it is not.

## Testing

Numbered for traceability; new cases added after review are marked **(new)**.

1. **Blob round-trip** (unit, no DB): random float32 slice → `vecToBlob` → `blobToVec` returns the same slice. Mismatched length returns an error. NaN values round-trip bit-exactly via `math.Float32bits`/`Float32frombits`.
2. **SQLite round-trip** (in-memory SQLite): append 3 events via `SQLiteStore.Append`, save embedding rows for them, then `Load` returns them with correct `EventID`, `Embedding`, `Recorded` values, ordered newest-first. `LookupByIDs` returns events with all fields populated identically to `SQLiteStore.Query` output (ID, Timestamp, Source, SceneID, Actor, Kind, Payload).
3. **Hydrate restart** (in-memory SQLite + echo LLM, shared `now` func): record 3 events into an `Embedded` with persister, drop it, build a fresh `Embedded` with the same persister and the same `now` injection, call `Hydrate`. Confirm `Retrieve` returns the same top-k as the original. The shared `now` is required — recency scoring is time-sensitive and an unsynced clock would produce divergent results.
4. **Model mismatch**: save rows with `modelID="A"` and `modelID="B"` for the same event. Hydrate with `modelID="A"` returns only A rows; with `modelID="B"` returns only B rows. No error in either direction.
5. **Save failure does not break Record**: inject a `VectorStore` whose `Save` always errors. `Record` still appends in-memory; returns a joined error containing the save failure.
6. **Hydrate failure aborts boot** (smoke test in `cmd/sim/`): inject a `VectorStore` whose `Load` errors; `cmd/sim` boot returns the error rather than logging-and-continuing.
7. **(new) Embed-nil skips Save**: configure a persister and a model whose `EmbedText` returns an error. `Record` returns the embed error and `vs.Save` is **not** called (assert via a counter in the test persister).
8. **(new) `ev.ID == 0` skips Save**: pass `Record` an event with zero ID. In-memory append happens; `vs.Save` is **not** called; `Record` returns nil.
9. **(new) Hydrate is not idempotent — second call replaces**: hydrate, then save a fourth row, then hydrate again. Assert `m.entries` reflects the fresh load (no duplication of the first three).
10. **(new) Timestamp round-trip**: a record stored with `Recorded = T` and hydrated back has `m.entries[i].recorded == T` (UTC-equal, not just within-a-second). Recency scoring depends on bit-stable timestamps.
11. **(new) Deterministic ordering on equal timestamps**: save two rows with identical `recorded_ns` and different `event_id`s. `Load` returns them in descending `event_id` order, repeatably.

## Files

- `internal/memory/persister.go` (new) — `VectorStore`, `EmbeddingRow`, `EventLookup`, `Option`, `WithPersister`.
- `internal/memory/embedded.go` — option-based constructor, `Hydrate(ctx, EventLookup)`, persister call in `Record`.
- `internal/memory/embedded_test.go` — new cases 3, 4, 5, 7, 8, 9, 10.
- `internal/store/sqlite.go` — add `character_memory` schema, add `DB() *sql.DB`, add `LookupByIDs(ctx, ids) ([]Event, error)` satisfying `memory.EventLookup` structurally.
- `internal/store/sqlite_test.go` — coverage for `LookupByIDs`.
- `internal/store/vector_sqlite.go` (new) — `SQLiteVectorStore`, `vecToBlob`, `blobToVec`.
- `internal/store/vector_sqlite_test.go` (new) — cases 1, 2, 11.
- `internal/llm/gemini/<provider>.go` — export `EmbeddingModelID` constant (exact filename: confirm during planning via `ls internal/llm/gemini/`).
- `cmd/sim/echo_llm.go` — export `echoEmbeddingModelID` constant.
- `cmd/sim/llm_select.go` — return model ID alongside LLM impl.
- `cmd/sim/main.go` — wire `SQLiteVectorStore`, call `Hydrate` per character before `w.Run`, propagate Hydrate errors.
- `cmd/sim/smoke_test.go` — case 6.

The `llm.LLM` interface itself is unchanged.

## Open follow-ups (not this PR)

- Document model migration runbook in README (one paragraph: `DELETE FROM character_memory WHERE model_id = ?` when retiring an embedding model).
- Per-character Hydrate timeout (currently blocks boot on a stuck backend).
- If `internal/llm/gemini/` does not yet exist or lives elsewhere, the file list above needs adjustment — confirm in the planning step.
