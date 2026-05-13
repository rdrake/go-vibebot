package scene

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/store"
)

// synthRecallK caps how many of the leader's past events the synthesis
// prompt may pull in. Three matches the per-character respond loop;
// trim if MaxTokens (120) starts truncating the synthesized reply.
const synthRecallK = 3

// Utterance is one member's response captured during fan-out, returned to
// the caller so it can be persisted as a KindSpeech event before the
// leader's synthesis lands.
type Utterance struct {
	CharacterID api.CharacterID
	Text        string
}

// OrchestrationResult is the per-turn output: every member utterance the
// router solicited, plus the leader's synthesized statement (empty when
// no leader was set).
type OrchestrationResult struct {
	Utterances  []Utterance
	Synthesized string
}

// Orchestrate runs one leader-led turn over an inbound event.
//
// Every member (including the leader) receives the perception for memory;
// only the router-selected subset gets a Reply channel and contributes an
// utterance. The leader synthesizes the selected utterances into the
// group's response. This preserves "selective perception, no belief
// modeling": memory tracks what was witnessed, routing only governs voice.
//
// The returned Utterances are in router-selected order; the caller is
// expected to persist each as a KindSpeech event so the adventure log
// captures individual voices, not just the synthesis.
func (s *Scene) Orchestrate(ctx context.Context, model llm.LLM, ev store.Event) (OrchestrationResult, error) {
	if len(s.Members) == 0 {
		return OrchestrationResult{}, nil
	}
	prompt := renderPrompt(ev)

	candidates := nonLeader(s.Members, s.Leader)
	router := s.Router
	if router == nil {
		router = AllRouter{}
	}
	selected, err := router.Select(ctx, ev, s.Leader, candidates)
	if err != nil {
		return OrchestrationResult{}, fmt.Errorf("route: %w", err)
	}

	selectedSet := make(map[api.CharacterID]struct{}, len(selected))
	for _, c := range selected {
		selectedSet[c.ID] = struct{}{}
	}

	replyChans := make(map[api.CharacterID]chan string, len(selected))
	for _, m := range s.Members {
		var ch chan string
		if _, picked := selectedSet[m.ID]; picked {
			ch = make(chan string, 1)
			replyChans[m.ID] = ch
		}
		select {
		case m.Inbox <- character.Perception{Event: ev, Prompt: prompt, Reply: ch}:
		case <-ctx.Done():
			return OrchestrationResult{}, ctx.Err()
		}
	}

	utterances := make([]Utterance, 0, len(selected))
	replies := make([]string, 0, len(selected))
	for _, m := range selected {
		ch := replyChans[m.ID]
		select {
		case r := <-ch:
			utterances = append(utterances, Utterance{CharacterID: m.ID, Text: r})
			replies = append(replies, fmt.Sprintf("%s: %s", m.Name, r))
		case <-ctx.Done():
			return OrchestrationResult{}, ctx.Err()
		}
	}

	synth, err := s.synthesize(ctx, model, ev, prompt, replies)
	if err != nil {
		return OrchestrationResult{Utterances: utterances}, err
	}
	return OrchestrationResult{Utterances: utterances, Synthesized: synth}, nil
}

func nonLeader(members []*character.Character, leader *character.Character) []*character.Character {
	out := make([]*character.Character, 0, len(members))
	for _, m := range members {
		if m == leader {
			continue
		}
		out = append(out, m)
	}
	return out
}

func (s *Scene) synthesize(ctx context.Context, model llm.LLM, ev store.Event, prompt string, replies []string) (string, error) {
	if s.Leader == nil {
		return strings.Join(replies, " | "), nil
	}
	user := "Situation: " + prompt + "\n\nReactions:\n" + strings.Join(replies, "\n")
	if recall := s.recallForSynth(ctx, ev, prompt); recall != "" {
		user = recall + "\n\n" + user
	}
	req := llm.CompleteRequest{
		System: fmt.Sprintf(
			"You are %s, leader of this group. %s\nSynthesize the group's reactions into one short in-character statement.",
			s.Leader.Name, s.Leader.Persona,
		),
		Messages: []llm.Message{
			{Role: llm.RoleUser, Content: user},
		},
		MaxTokens:   120,
		Temperature: 0.7,
	}
	return model.Complete(ctx, req)
}

// recallForSynth pulls a few of the leader's past events similar to the
// current prompt and renders them as a prelude. Returns "" on retrieval
// failure or when only the current event surfaces — the leader still
// synthesizes, just without recalled context.
func (s *Scene) recallForSynth(ctx context.Context, ev store.Event, prompt string) string {
	if s.Leader == nil || s.Leader.Memory == nil {
		return ""
	}
	events, err := s.Leader.Memory.Retrieve(ctx, prompt, synthRecallK)
	if err != nil {
		slog.Default().Warn("leader memory retrieve failed",
			"leader", s.Leader.ID, "err", err)
		return ""
	}
	lines := make([]string, 0, len(events))
	for _, past := range events {
		if past.ID == ev.ID {
			continue
		}
		text := store.TextOf(past)
		if text == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s/%s: %s", past.Actor, past.Kind, text))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Group's recent history:\n" + strings.Join(lines, "\n")
}

func renderPrompt(ev store.Event) string {
	if text := store.TextOf(ev); text != "" {
		return text
	}
	return fmt.Sprintf("[%s/%s by %s]", ev.Source, ev.Kind, ev.Actor)
}
