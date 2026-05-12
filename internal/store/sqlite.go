package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/afternet/go-vibebot/internal/api"

	_ "modernc.org/sqlite"
)

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

// SQLiteStore is an EventStore backed by SQLite (pure-Go driver, no cgo).
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite opens or creates an event store at path. Use ":memory:" for tests.
func OpenSQLite(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

// Append persists ev and assigns ev.ID on success.
func (s *SQLiteStore) Append(ctx context.Context, ev *Event) error {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO events (ts_ns, source, scene_id, actor, kind, payload)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ev.Timestamp.UnixNano(),
		string(ev.Source), string(ev.SceneID), ev.Actor, string(ev.Kind),
		[]byte(ev.Payload),
	)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	ev.ID = EventID(id)
	return nil
}

// Query returns matching events in ascending timestamp order.
func (s *SQLiteStore) Query(ctx context.Context, f Filter) ([]Event, error) {
	q := `SELECT id, ts_ns, source, scene_id, actor, kind, payload FROM events WHERE 1=1`
	var args []any
	if !f.Since.IsZero() {
		q += ` AND ts_ns >= ?`
		args = append(args, f.Since.UnixNano())
	}
	if f.SceneID != "" {
		q += ` AND scene_id = ?`
		args = append(args, string(f.SceneID))
	}
	if f.Actor != "" {
		q += ` AND actor = ?`
		args = append(args, f.Actor)
	}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, string(f.Kind))
	}
	q += ` ORDER BY ts_ns ASC, id ASC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}

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

// Close releases the underlying database.
func (s *SQLiteStore) Close() error { return s.db.Close() }

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
