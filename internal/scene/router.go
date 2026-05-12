package scene

import (
	"context"

	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/store"
)

// Router decides which non-leader members of a scene should be consulted
// for a given event. Implementations must be deterministic given identical
// inputs (modulo any LLM call latency/variance they wrap).
//
// Routing affects who *speaks* this turn. Every member still receives the
// perception for memory ("selective perception, no belief modeling").
type Router interface {
	Select(
		ctx context.Context,
		ev store.Event,
		leader *character.Character,
		candidates []*character.Character,
	) ([]*character.Character, error)
}

// AllRouter consults every candidate every turn. Useful as a deterministic
// fallback and for tests; also the right behavior for very small groups.
type AllRouter struct{}

// Select returns all candidates unchanged.
func (AllRouter) Select(
	_ context.Context,
	_ store.Event,
	_ *character.Character,
	candidates []*character.Character,
) ([]*character.Character, error) {
	return candidates, nil
}
