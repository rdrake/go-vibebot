package memory

import (
	"context"
	"fmt"
	"strings"

	"github.com/afternet/go-vibebot/internal/store"
)

// InMem is a recency-only slice-backed Store. Kept for tests and for
// runs that have no embedding provider available; production wiring uses
// Embedded. Not safe for concurrent use across goroutines.
type InMem struct {
	cap    int
	events []store.Event
}

// NewInMem caps memory at the most recent `cap` events. cap <= 0 disables the cap.
func NewInMem(cap int) *InMem {
	return &InMem{cap: cap}
}

// Record appends an event to memory, dropping oldest entries past the cap.
// Always returns nil; the signature matches Store for interface uniformity.
func (m *InMem) Record(_ context.Context, ev store.Event) error {
	m.events = append(m.events, ev)
	if m.cap > 0 && len(m.events) > m.cap {
		m.events = m.events[len(m.events)-m.cap:]
	}
	return nil
}

// Retrieve returns the most recent k events. Ignores query; for similarity
// retrieval use Embedded.
func (m *InMem) Retrieve(_ context.Context, _ string, k int) ([]store.Event, error) {
	if k <= 0 || k > len(m.events) {
		k = len(m.events)
	}
	out := make([]store.Event, k)
	copy(out, m.events[len(m.events)-k:])
	return out, nil
}

// Summary renders all recorded events as a flat newline-joined list.
func (m *InMem) Summary() string {
	var b strings.Builder
	for _, ev := range m.events {
		fmt.Fprintf(&b, "- %s/%s: %s\n", ev.Actor, ev.Kind, store.TextOf(ev))
	}
	return b.String()
}
