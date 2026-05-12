package store

import (
	"encoding/json"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
)

// EventID is the monotonically-increasing primary key of an event.
type EventID int64

// Source is where an event originated. Newtype so a kind cannot be passed
// where a source is wanted (and vice versa).
type Source string

// Kind is the event discriminator. Newtype for the same reason as Source.
type Kind string

// Sources.
const (
	SourceIRC    Source = "irc"
	SourceTick   Source = "tick"
	SourceGroup  Source = "group"
	SourceSystem Source = "system"
)

// Kinds.
const (
	KindSpeech      Kind = "speech"
	KindAction      Kind = "action"
	KindPerception  Kind = "perception"
	KindSceneEnter  Kind = "scene_enter"
	KindInject      Kind = "inject"
	KindAmbient     Kind = "ambient"
	KindSynthesized Kind = "synthesized"
	KindSummon      Kind = "summon"
	KindNudge       Kind = "nudge"
)

// Event is the append-only unit of world history.
//
// Payload is opaque JSON. Use the typed New*Event constructors and the
// TextOf accessor to avoid kind/payload mismatches.
type Event struct {
	ID        EventID
	Timestamp time.Time
	Source    Source
	SceneID   api.SceneID
	Actor     string // polymorphic: a CharacterID string, "world", "group", ...
	Kind      Kind
	Payload   json.RawMessage
}
