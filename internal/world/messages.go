package world

import "github.com/afternet/go-vibebot/internal/api"

// Command is a sealed sum type of every externally-originated request the
// world coordinator handles. The marker method is unexported so only this
// package can extend the type; adding a new variant forces every type-switch
// in this package to be updated (the dispatcher's default arm panics on an
// unknown variant — combined with tests, that is the safety net).
type Command interface {
	isCommand()
}

// Inject is an IRC `!inject` style scenario push.
// Target is free-form (caller-provided noun, often "").
type Inject struct {
	Target      string
	Description string
	Reply       chan<- error
}

// Summon directs the active scene toward a place. Scaffolded for phase 2.
type Summon struct {
	PlaceID api.PlaceID
	Reply   chan<- error
}

// Nudge pokes a specific character without injecting a new scenario.
// Scaffolded for phase 2.
type Nudge struct {
	CharacterID api.CharacterID
	Reply       chan<- error
}

func (Inject) isCommand() {}
func (Summon) isCommand() {}
func (Nudge) isCommand()  {}

// GroupAction is an internally-originated action a group/scene wants to
// take. Reserved for future use; the skeleton produces these only
// indirectly.
type GroupAction struct {
	SceneID api.SceneID
	Kind    string
	Text    string
}
