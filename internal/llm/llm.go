// Package llm defines the only LLM contract the rest of the system knows about.
//
// Provider implementations live in sibling packages or in cmd/sim. Mocks live
// in *_test.go files alongside the consumers that need them. Nothing in this
// package stubs LLM behavior.
package llm

import "context"

// Role is the speaker of a message in an LLM conversation. Newtype so a
// freeform string cannot accidentally be passed as a role.
type Role string

// Roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single turn in an LLM conversation.
type Message struct {
	Role    Role
	Content string
}

// CompleteRequest is the input to LLM.Complete.
type CompleteRequest struct {
	System      string
	Messages    []Message
	MaxTokens   int
	Temperature float32
}

// LLM is the single abstraction over text completion + embeddings.
type LLM interface {
	Complete(ctx context.Context, req CompleteRequest) (string, error)
	EmbedText(ctx context.Context, text string) ([]float32, error)
}
