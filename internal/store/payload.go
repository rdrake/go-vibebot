package store

import (
	"encoding/json"

	"github.com/afternet/go-vibebot/internal/api"
)

// TextPayload is the envelope for kinds that carry one free-form string.
// Several kinds (Inject, Speech, Ambient, Synthesized, Summon, Nudge)
// share this shape; that is encoded by the New*Event constructors.
type TextPayload struct {
	Text   string `json:"text"`
	Target string `json:"target,omitempty"`
}

// MarshalText encodes a string as a TextPayload JSON. Kept exported for
// adapters that need to construct event payloads directly (e.g., tests).
func MarshalText(s string) json.RawMessage {
	b, _ := json.Marshal(TextPayload{Text: s})
	return b
}

// TextOf extracts the Text field for events whose payload is a TextPayload.
// Returns "" if the payload is empty or malformed; callers checking Kind
// first will get well-defined behavior.
func TextOf(ev Event) string {
	if len(ev.Payload) == 0 {
		return ""
	}
	var p TextPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return ""
	}
	return p.Text
}

// NewInjectEvent constructs a KindInject event from an IRC-shaped inject.
func NewInjectEvent(scene api.SceneID, target, text string) Event {
	return Event{
		Source:  SourceIRC,
		SceneID: scene,
		Actor:   target,
		Kind:    KindInject,
		Payload: encodeText(text, ""),
	}
}

// NewAmbientEvent constructs a KindAmbient tick event.
func NewAmbientEvent(scene api.SceneID, text string) Event {
	return Event{
		Source:  SourceTick,
		SceneID: scene,
		Actor:   ActorWorld,
		Kind:    KindAmbient,
		Payload: encodeText(text, ""),
	}
}

// NewSynthesizedEvent constructs the leader-synthesized group utterance.
func NewSynthesizedEvent(scene api.SceneID, leader api.CharacterID, text string) Event {
	return Event{
		Source:  SourceGroup,
		SceneID: scene,
		Actor:   string(leader),
		Kind:    KindSynthesized,
		Payload: encodeText(text, ""),
	}
}

// NewSpeechEvent constructs a per-character utterance event.
func NewSpeechEvent(scene api.SceneID, actor api.CharacterID, text string) Event {
	return Event{
		Source:  SourceGroup,
		SceneID: scene,
		Actor:   string(actor),
		Kind:    KindSpeech,
		Payload: encodeText(text, ""),
	}
}

// NewSummonEvent records a (currently scaffolded) summon request.
func NewSummonEvent(scene api.SceneID, placeID api.PlaceID) Event {
	return Event{
		Source:  SourceIRC,
		SceneID: scene,
		Actor:   string(placeID),
		Kind:    KindSummon,
		Payload: encodeText("", string(placeID)),
	}
}

// NewNudgeEvent records a (currently scaffolded) nudge request.
func NewNudgeEvent(scene api.SceneID, characterID api.CharacterID) Event {
	return Event{
		Source:  SourceIRC,
		SceneID: scene,
		Actor:   string(characterID),
		Kind:    KindNudge,
		Payload: encodeText("", string(characterID)),
	}
}

func encodeText(text, target string) json.RawMessage {
	b, _ := json.Marshal(TextPayload{Text: text, Target: target})
	return b
}
