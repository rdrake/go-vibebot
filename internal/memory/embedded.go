package memory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/store"
)

// Default recency parameters for the score = sim + lambda*exp(-age/tau)
// formula. Lambda balances similarity vs recency; tau is the time constant
// at which the recency bonus decays to 1/e. One hour matches the default
// !log window so the two surfaces are intuitively aligned.
const (
	DefaultLambda = 0.3
	DefaultTau    = time.Hour
)

// Embedded scores Retrieve results by cosine similarity to a query
// embedding plus an exponential recency bonus. It embeds each recorded
// event up front; Retrieve does one embedding for the query, then a cheap
// in-memory rank. Safe for concurrent use: the owning character goroutine
// writes (via Record) while the world goroutine reads during leader
// synthesis (via Retrieve / Summary).
type Embedded struct {
	model  llm.LLM
	cap    int
	lambda float64
	tau    time.Duration
	now    func() time.Time // injectable for tests
	mu     sync.RWMutex     // guards entries; persister calls happen outside the lock
	entries []memoryEntry
	// Persistence (optional; nil when WithPersister was not used).
	persister VectorStore
	owner     api.CharacterID
	modelID   string
}

type memoryEntry struct {
	event     store.Event
	embedding []float32
	recorded  time.Time
}

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

// SetRecencyParams overrides the default lambda and tau. Use before any
// recording so all entries are scored against a single setting.
func (m *Embedded) SetRecencyParams(lambda float64, tau time.Duration) {
	m.lambda = lambda
	m.tau = tau
}

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

	m.mu.Lock()
	m.entries = append(m.entries, entry)
	if m.cap > 0 && len(m.entries) > m.cap {
		m.entries = m.entries[len(m.entries)-m.cap:]
	}
	m.mu.Unlock()

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

// Retrieve returns up to k events ranked by similarity + recency. If
// query is empty (or no entries carry embeddings), the result is a
// straight recency-ordered tail.
func (m *Embedded) Retrieve(ctx context.Context, query string, k int) ([]store.Event, error) {
	if k <= 0 {
		return nil, nil
	}
	snap := m.snapshot()
	if len(snap) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(query) == "" || !entriesHaveEmbeddings(snap) {
		return recencyTailOf(snap, k), nil
	}

	qvec, err := m.model.EmbedText(ctx, query)
	if err != nil {
		slog.Default().Warn("memory retrieve embed failed; falling back to recency",
			"err", err)
		return recencyTailOf(snap, k), nil
	}

	type ranked struct {
		idx   int
		score float64
	}
	scored := make([]ranked, 0, len(snap))
	now := m.now()
	for i, e := range snap {
		if len(e.embedding) == 0 {
			continue
		}
		sim := cosine(qvec, e.embedding)
		recency := math.Exp(-now.Sub(e.recorded).Seconds() / m.tau.Seconds())
		scored = append(scored, ranked{idx: i, score: sim + m.lambda*recency})
	}
	if len(scored) == 0 {
		return recencyTailOf(snap, k), nil
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	if k > len(scored) {
		k = len(scored)
	}
	out := make([]store.Event, k)
	for i := 0; i < k; i++ {
		out[i] = snap[scored[i].idx].event
	}
	return out, nil
}

// snapshot returns a shallow copy of entries safe to iterate without the
// lock. Embedding backing arrays are immutable post-Record, so sharing
// them across goroutines is safe.
func (m *Embedded) snapshot() []memoryEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.entries) == 0 {
		return nil
	}
	out := make([]memoryEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Summary renders all recorded events as a flat newline-joined list.
func (m *Embedded) Summary() string {
	snap := m.snapshot()
	var b strings.Builder
	for _, e := range snap {
		fmt.Fprintf(&b, "- %s/%s: %s\n", e.event.Actor, e.event.Kind, store.TextOf(e.event))
	}
	return b.String()
}

func entriesHaveEmbeddings(entries []memoryEntry) bool {
	for _, e := range entries {
		if len(e.embedding) > 0 {
			return true
		}
	}
	return false
}

func recencyTailOf(entries []memoryEntry, k int) []store.Event {
	if k > len(entries) {
		k = len(entries)
	}
	out := make([]store.Event, k)
	for i, e := range entries[len(entries)-k:] {
		out[i] = e.event
	}
	return out
}

// timestamp prefers the event's own timestamp when set; otherwise it
// stamps now. Events freshly minted by adapters may have a zero time
// until persisted.
func (m *Embedded) timestamp(ev store.Event) time.Time {
	if !ev.Timestamp.IsZero() {
		return ev.Timestamp
	}
	return m.now()
}

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
	rows, err := m.persister.Load(ctx, m.owner, m.modelID, m.cap)
	if err != nil {
		return fmt.Errorf("hydrate load: %w", err)
	}
	if len(rows) == 0 {
		m.mu.Lock()
		m.entries = nil
		m.mu.Unlock()
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
	m.mu.Lock()
	m.entries = entries
	m.mu.Unlock()
	return nil
}

// cosine is the cosine similarity of two equal-length vectors. Returns 0
// for mismatched dimensions or zero-magnitude inputs, both of which mean
// "no information" rather than "definitely orthogonal."
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
