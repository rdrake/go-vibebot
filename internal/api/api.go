// Package api defines WorldAPI: the core read/write surface that every
// adapter (IRC, future LLM tools, CLI) talks to. Adapters never touch the
// coordinator, store, or scenes directly.
package api

import (
	"context"
	"time"
)

// CharacterRef is a lightweight handle to a character for read APIs.
type CharacterRef struct {
	ID    CharacterID
	Name  string
	Blurb string
}

// SceneSnapshot is a point-in-time view of a scene, safe to hand to callers.
type SceneSnapshot struct {
	SceneID  SceneID
	PlaceID  PlaceID
	Leader   CharacterID
	Members  []CharacterRef
	Captured time.Time
}

// LogEntry is a flattened event suitable for log readers. Adapters do not
// need to know payload schemas — Text is pre-extracted from text-shaped
// payloads; Kind preserves the discriminator for structured consumers.
type LogEntry struct {
	Timestamp time.Time
	SceneID   SceneID
	Actor     string // polymorphic: "world", a CharacterID string, etc.
	Kind      string // store.Kind value as raw string
	Text      string
}

// WorldAPI is the core surface. Implementations marshal writes onto the
// world coordinator goroutine; reads may bypass the coordinator if the
// underlying data is immutable (e.g., the append-only event log).
type WorldAPI interface {
	// Writes — externally-driven scenarios and pokes.
	InjectEvent(ctx context.Context, sceneID SceneID, target, description string) error
	Summon(ctx context.Context, placeID PlaceID) error
	Nudge(ctx context.Context, characterID CharacterID) error

	// Reads — narrative-rich, suitable for direct user/LLM consumption.
	Where(ctx context.Context, characterID CharacterID) (SceneSnapshot, error)
	Log(ctx context.Context, since time.Duration) ([]LogEntry, error)
	Who(ctx context.Context, sceneID SceneID) ([]CharacterRef, error)
	Describe(ctx context.Context, id string) (string, error)
}
