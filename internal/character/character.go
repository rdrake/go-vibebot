package character

import (
	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/memory"
	"github.com/afternet/go-vibebot/internal/store"
)

// Character is the public face of an active agent. The decide-loop goroutine
// owns the Inbox; everything else is read-only after construction.
type Character struct {
	ID           api.CharacterID
	Name         string
	Persona      string
	Capabilities []string
	Blurb        string

	Memory memory.Store
	Inbox  chan Perception
}

// Perception is what a scene leader sends to a member. If Reply is non-nil
// the member is expected to respond on it; nil means perception-only (the
// member should update memory but produce no externally-visible utterance).
type Perception struct {
	Event  store.Event
	Prompt string      // leader-rendered prompt summary
	Reply  chan string // nil for fire-and-forget
}
