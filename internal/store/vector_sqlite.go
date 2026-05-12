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
