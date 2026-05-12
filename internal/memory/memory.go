// Package memory is per-character episodic memory. The recency-only InMem
// impl is kept for tests; production runs use Embedded, which embeds each
// recorded event and retrieves by similarity + recency.
package memory

import (
	"context"

	"github.com/afternet/go-vibebot/internal/store"
)

// Store is per-character memory. Implementations must be safe for use by
// the owning character goroutine; no cross-character sharing is intended.
//
// Record and Retrieve take a context so embedding-backed implementations
// can honor cancellation on their LLM calls. Record returns an error so
// callers can decide whether to log-and-continue or surface the failure;
// in-process impls that cannot fail return nil.
type Store interface {
	Record(ctx context.Context, ev store.Event) error
	Retrieve(ctx context.Context, query string, k int) ([]store.Event, error)
	Summary() string
}
