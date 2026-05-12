package scene

import (
	"context"
	"fmt"
	"strings"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/store"
)

// LLMRouter pre-filters candidates by tag/keyword overlap, then asks an
// LLM to choose which subset should be consulted this turn. Any failure
// (empty response, unparseable text, no recognised IDs) falls back to the
// full pre-filtered set; pre-filter never returns an empty list when given
// non-empty input, so the router never silently drops a turn.
type LLMRouter struct {
	Model      llm.LLM
	PreFilterK int // 0 = no cap on prefilter
	MaxConsult int // 0 = no cap on final consult list
}

// Select implements Router.
//
// LLM failures (transport error, garbage response, no recognised IDs) are
// intentionally swallowed: the caller relies on this method to keep the
// turn moving, and pre-filter already guarantees a non-empty fallback. The
// concrete strategy is "consult the pre-filtered set when the LLM can't be
// trusted to choose."
func (r LLMRouter) Select(
	ctx context.Context,
	ev store.Event,
	leader *character.Character,
	candidates []*character.Character,
) ([]*character.Character, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	prefiltered := PreFilter(store.TextOf(ev), candidates, r.PreFilterK)
	if len(prefiltered) <= 1 || r.Model == nil {
		return r.cap(prefiltered), nil
	}

	var picked []*character.Character
	resp, err := r.Model.Complete(ctx, llm.CompleteRequest{
		System:      routerSystem(leader),
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: routerPrompt(ev, prefiltered)}},
		MaxTokens:   80,
		Temperature: 0.2,
	})
	if err == nil && resp != "" {
		valid := make(map[api.CharacterID]*character.Character, len(prefiltered))
		for _, c := range prefiltered {
			valid[c.ID] = c
		}
		picked = pickByResponse(resp, valid, prefiltered)
	}

	if len(picked) == 0 {
		picked = prefiltered
	}
	return r.cap(picked), nil
}

func (r LLMRouter) cap(cs []*character.Character) []*character.Character {
	if r.MaxConsult > 0 && len(cs) > r.MaxConsult {
		return cs[:r.MaxConsult]
	}
	return cs
}

func routerSystem(leader *character.Character) string {
	if leader == nil {
		return "You are routing a group turn. Pick which members should react."
	}
	return fmt.Sprintf(
		"You are %s, the leader of a group. Pick which members should react this turn. Reply with ONLY a comma-separated list of member IDs from the candidate list. No commentary, no quotes.",
		leader.Name,
	)
}

func routerPrompt(ev store.Event, candidates []*character.Character) string {
	var b strings.Builder
	b.WriteString("Situation: ")
	if text := store.TextOf(ev); text != "" {
		b.WriteString(text)
	} else {
		fmt.Fprintf(&b, "[%s/%s by %s]", ev.Source, ev.Kind, ev.Actor)
	}
	b.WriteString("\n\nCandidates (id — blurb — tags):\n")
	for _, c := range candidates {
		fmt.Fprintf(&b, "- %s — %s — %s\n", c.ID, c.Blurb, strings.Join(c.Capabilities, ", "))
	}
	b.WriteString("\nReply with comma-separated IDs only.")
	return b.String()
}

// pickByResponse extracts IDs from an LLM response. It accepts comma-
// separated values, surrounding prose, and quoted IDs; it returns
// candidates in the order the LLM listed them (deduplicated), keeping only
// IDs in `valid`. order is preserved against `valid` membership.
func pickByResponse(
	resp string,
	valid map[api.CharacterID]*character.Character,
	prefiltered []*character.Character,
) []*character.Character {
	resp = strings.ReplaceAll(resp, "\n", ",")
	seen := make(map[api.CharacterID]struct{}, len(valid))
	out := make([]*character.Character, 0, len(valid))
	for _, part := range strings.Split(resp, ",") {
		id := api.CharacterID(strings.Trim(strings.TrimSpace(part), `"'`+"`"))
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		if c, ok := valid[id]; ok {
			seen[id] = struct{}{}
			out = append(out, c)
		}
	}
	// If response was unparseable but did contain IDs as substrings, we
	// could fall back to substring scan here; prefiltered is the caller's
	// fallback. Keep this function honest about what it found.
	_ = prefiltered
	return out
}
