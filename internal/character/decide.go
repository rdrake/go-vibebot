package character

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/store"
)

// retrievedContextK is how many prior events to surface in the per-turn
// prompt. Three keeps the prompt focused; the rest live in long-term
// memory and resurface only when relevant.
const retrievedContextK = 3

// Run is the character's decide loop. It owns the inbox; the goroutine exits
// when ctx is cancelled or the inbox closes.
func (c *Character) Run(ctx context.Context, model llm.LLM) {
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-c.Inbox:
			if !ok {
				return
			}
			if err := c.Memory.Record(ctx, p.Event); err != nil {
				slog.Default().Warn("memory record failed",
					"character", c.ID, "err", err)
			}
			if p.Reply == nil {
				continue
			}
			out, err := c.respond(ctx, model, p)
			if err != nil {
				out = fmt.Sprintf("[%s is silent]", c.Name)
			}
			select {
			case p.Reply <- out:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (c *Character) respond(ctx context.Context, model llm.LLM, p Perception) (string, error) {
	user := p.Prompt
	if ctxBlock := c.recallContext(ctx, p); ctxBlock != "" {
		user = ctxBlock + "\n\nNow: " + p.Prompt
	}
	req := llm.CompleteRequest{
		System: fmt.Sprintf("You are %s. %s\nRespond in character, one sentence.", c.Name, c.Persona),
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: user},
		},
		MaxTokens:   80,
		Temperature: 0.8,
	}
	return model.Complete(ctx, req)
}

// recallContext pulls a few similar past events and renders them as a
// short prelude. Returns "" when there's nothing relevant or retrieval
// fails — the character still answers, just without recalled context.
func (c *Character) recallContext(ctx context.Context, p Perception) string {
	events, err := c.Memory.Retrieve(ctx, p.Prompt, retrievedContextK)
	if err != nil {
		slog.Default().Warn("memory retrieve failed",
			"character", c.ID, "err", err)
		return ""
	}
	if len(events) == 0 {
		return ""
	}
	lines := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.ID == p.Event.ID {
			// The current event is already in memory (Record ran first);
			// don't echo it back as "context".
			continue
		}
		text := store.TextOf(ev)
		if text == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s/%s: %s", ev.Actor, ev.Kind, text))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Recent context:\n" + strings.Join(lines, "\n")
}
