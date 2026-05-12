package memory

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

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
// in-memory rank. Not safe for concurrent use; the owning character
// goroutine is the sole accessor.
type Embedded struct {
	model   llm.LLM
	cap     int
	lambda  float64
	tau     time.Duration
	now     func() time.Time // injectable for tests
	entries []memoryEntry
}

type memoryEntry struct {
	event     store.Event
	embedding []float32
	recorded  time.Time
}

// NewEmbedded returns an Embedded store backed by the given LLM. cap <= 0
// disables the size cap. lambda/tau take defaults; override via setters
// before use if needed.
func NewEmbedded(model llm.LLM, cap int) *Embedded {
	return &Embedded{
		model:  model,
		cap:    cap,
		lambda: DefaultLambda,
		tau:    DefaultTau,
		now:    time.Now,
	}
}

// SetRecencyParams overrides the default lambda and tau. Use before any
// recording so all entries are scored against a single setting.
func (m *Embedded) SetRecencyParams(lambda float64, tau time.Duration) {
	m.lambda = lambda
	m.tau = tau
}

// Record embeds the event's text (if any) and appends it. Events with
// empty text are still appended for recency-only retrieval; they never
// compete on similarity.
//
// On embedding failure the event is still appended with a nil embedding
// and the error is returned, letting the caller decide whether to log or
// surface it. The store remains coherent either way.
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
	return embedErr
}

// Retrieve returns up to k events ranked by similarity + recency. If
// query is empty (or no entries carry embeddings), the result is a
// straight recency-ordered tail.
func (m *Embedded) Retrieve(ctx context.Context, query string, k int) ([]store.Event, error) {
	if k <= 0 {
		return nil, nil
	}
	if len(m.entries) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(query) == "" || !m.hasEmbeddings() {
		return m.recencyTail(k), nil
	}

	qvec, err := m.model.EmbedText(ctx, query)
	if err != nil {
		slog.Default().Warn("memory retrieve embed failed; falling back to recency",
			"err", err)
		return m.recencyTail(k), nil
	}

	type ranked struct {
		idx   int
		score float64
	}
	scored := make([]ranked, 0, len(m.entries))
	now := m.now()
	for i, e := range m.entries {
		if len(e.embedding) == 0 {
			continue
		}
		sim := cosine(qvec, e.embedding)
		recency := math.Exp(-now.Sub(e.recorded).Seconds() / m.tau.Seconds())
		scored = append(scored, ranked{idx: i, score: sim + m.lambda*recency})
	}
	if len(scored) == 0 {
		return m.recencyTail(k), nil
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	if k > len(scored) {
		k = len(scored)
	}
	out := make([]store.Event, k)
	for i := 0; i < k; i++ {
		out[i] = m.entries[scored[i].idx].event
	}
	return out, nil
}

// Summary renders all recorded events as a flat newline-joined list.
func (m *Embedded) Summary() string {
	var b strings.Builder
	for _, e := range m.entries {
		fmt.Fprintf(&b, "- %s/%s: %s\n", e.event.Actor, e.event.Kind, store.TextOf(e.event))
	}
	return b.String()
}

func (m *Embedded) hasEmbeddings() bool {
	for _, e := range m.entries {
		if len(e.embedding) > 0 {
			return true
		}
	}
	return false
}

func (m *Embedded) recencyTail(k int) []store.Event {
	if k > len(m.entries) {
		k = len(m.entries)
	}
	out := make([]store.Event, k)
	for i, e := range m.entries[len(m.entries)-k:] {
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
