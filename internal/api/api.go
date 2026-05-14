// Package api defines WorldAPI: the core read/write surface that every
// adapter (IRC, future LLM tools, CLI) talks to. Adapters never touch the
// coordinator, store, or scenes directly.
package api

import (
	"context"
	"time"
)

// CharacterRef is a lightweight handle to a character for read APIs.
// JSON tags are required because adapters serialize these refs directly
// to LLM consumers that parse by lowercase key.
type CharacterRef struct {
	ID    CharacterID `json:"id"`
	Name  string      `json:"name"`
	Blurb string      `json:"blurb"`
}

// PlaceRef is a lightweight handle to a registered place, suitable for
// listing in read APIs. SceneID is the synthetic id under which the place
// runs (today: "place:<PlaceID>"); Leader is the first NPC in the place's
// yaml; Members are all NPCs in the place's scene. JSON tags are required
// for the same reason as CharacterRef.
type PlaceRef struct {
	ID      PlaceID        `json:"id"`
	SceneID SceneID        `json:"scene_id"`
	Leader  CharacterID    `json:"leader"`
	Members []CharacterRef `json:"members"`
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
	// SummonNew registers a new ad-hoc place-scene at runtime using existing
	// characters and returns the new scene id. npcs must be non-empty and
	// reference ids returned by Characters(); the first id is the leader.
	// If description is non-empty it is recorded as an inject scoped to the
	// new scene after the summon event.
	SummonNew(ctx context.Context, placeID PlaceID, npcs []CharacterID, description string) (SceneID, error)
	Nudge(ctx context.Context, characterID CharacterID) error

	// Reads — narrative-rich, suitable for direct user/LLM consumption.
	// Where and Nudge resolve a character against the first scene that
	// registered them (boot-time wins). A character that has been added to
	// an ad-hoc scene via SummonNew still resolves to its boot-time scene
	// for these calls; to act inside an ad-hoc scene use InjectEvent with
	// the scene id returned by SummonNew.
	Where(ctx context.Context, characterID CharacterID) (SceneSnapshot, error)
	Log(ctx context.Context, since time.Duration) ([]LogEntry, error)
	Who(ctx context.Context, sceneID SceneID) ([]CharacterRef, error)
	Describe(ctx context.Context, id string) (string, error)
	Characters(ctx context.Context) ([]CharacterRef, error)
	Places(ctx context.Context) ([]PlaceRef, error)
}
