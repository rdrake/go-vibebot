// Package scene owns the orchestration unit: a transient context the leader
// drives with fan-out + synthesize.
package scene

import (
	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
)

// Scene is the orchestration unit. Members are addressed via their Inbox
// channels; the leader is responsible for fan-out and synthesis.
//
// Scene state is owned by a single scene goroutine (started by World); other
// goroutines must not mutate Members, Leader, or Router.
//
// Router may be nil; Orchestrate falls back to AllRouter in that case.
type Scene struct {
	ID      api.SceneID
	PlaceID api.PlaceID
	Members []*character.Character
	Leader  *character.Character
	Router  Router
}
