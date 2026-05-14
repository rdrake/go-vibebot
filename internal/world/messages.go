package world

import (
	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/scene"
)

// Command is a sealed sum type of every externally-originated request the
// world coordinator handles. The marker method is unexported so only this
// package can extend the type; adding a new variant forces every type-switch
// in this package to be updated (the dispatcher's default arm panics on an
// unknown variant — combined with tests, that is the safety net).
type Command interface {
	isCommand()
}

// Inject is an IRC `!inject` style scenario push targeted at one scene.
// SceneID may be empty: empty resolves to the default scene (the first
// scene registered). Target is free-form (caller-provided noun, often "").
type Inject struct {
	SceneID     api.SceneID
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

// RegisterSceneCmd registers a fully-constructed scene on the live
// coordinator. The boot helper World.RegisterScene panics on error; this
// variant returns the error to the caller so runtime-registered scenes can
// fail cleanly (duplicate ids, unknown member characters).
type RegisterSceneCmd struct {
	Scene *scene.Scene
	Reply chan<- error
}

func (Inject) isCommand()          {}
func (Summon) isCommand()          {}
func (Nudge) isCommand()           {}
func (RegisterSceneCmd) isCommand() {}

// GroupAction is an internally-originated action a group/scene wants to
// take. Reserved for future use; the skeleton produces these only
// indirectly.
type GroupAction struct {
	SceneID api.SceneID
	Kind    string
	Text    string
}
