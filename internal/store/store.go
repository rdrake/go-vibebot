package store

import (
	"context"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
)

// Filter narrows a Query. Zero fields mean "no constraint".
type Filter struct {
	Since   time.Time
	SceneID api.SceneID
	Actor   string
	Kind    Kind
	Limit   int
}

// EventStore is the persistence boundary for world history.
// Implementations must be safe for concurrent use.
type EventStore interface {
	Append(ctx context.Context, ev *Event) error
	Query(ctx context.Context, f Filter) ([]Event, error)
	Close() error
}
