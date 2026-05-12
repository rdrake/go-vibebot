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
