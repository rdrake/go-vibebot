package world

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/scene"
	"github.com/afternet/go-vibebot/internal/store"
)

// API returns a WorldAPI bound to this World. Adapters consume this surface
// and never touch the coordinator or store directly.
//
// The implementation lives inside the world package to avoid an import cycle
// (api defines the types that world.Where/Who return; world implements the
// surface in terms of its own coordinator channels).
func (w *World) API() api.WorldAPI { return apiImpl{w: w} }

type apiImpl struct{ w *World }

func (a apiImpl) InjectEvent(ctx context.Context, sceneID api.SceneID, target, description string) error {
	return a.send(ctx, func(r chan<- error) Command {
		return Inject{SceneID: sceneID, Target: target, Description: description, Reply: r}
	})
}

func (a apiImpl) Summon(ctx context.Context, placeID api.PlaceID) error {
	return a.send(ctx, func(r chan<- error) Command {
		return Summon{PlaceID: placeID, Reply: r}
	})
}

func (a apiImpl) SummonNew(
	ctx context.Context,
	placeID api.PlaceID,
	npcs []api.CharacterID,
	description string,
) (api.SceneID, error) {
	if placeID == "" {
		return "", errors.New("world: place id required")
	}
	if len(npcs) == 0 {
		return "", errors.New("world: at least one npc required")
	}

	chars, err := a.w.requestCharactersByID(ctx, npcs)
	if err != nil {
		return "", err
	}

	sceneID := api.SceneID("place:" + string(placeID))
	sc := &scene.Scene{
		ID:      sceneID,
		PlaceID: placeID,
		Members: chars,
		Leader:  chars[0],
		Router: scene.LLMRouter{
			Model: a.w.model, PreFilterK: 0, MaxConsult: 0,
		},
	}

	if err := a.send(ctx, func(r chan<- error) Command {
		return RegisterSceneCmd{Scene: sc, Reply: r}
	}); err != nil {
		return "", err
	}

	if err := a.Summon(ctx, placeID); err != nil {
		return sceneID, err
	}
	if description == "" {
		return sceneID, nil
	}
	return sceneID, a.InjectEvent(ctx, sceneID, "", description)
}

func (a apiImpl) Nudge(ctx context.Context, characterID api.CharacterID) error {
	return a.send(ctx, func(r chan<- error) Command {
		return Nudge{CharacterID: characterID, Reply: r}
	})
}

// send marshals a write command onto the coordinator's input channel and
// awaits its typed ack. mk wires the reply channel into the variant.
func (a apiImpl) send(ctx context.Context, mk func(chan<- error) Command) error {
	reply := make(chan error, 1)
	cmd := mk(reply)
	select {
	case a.w.commands <- cmd:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a apiImpl) Where(ctx context.Context, characterID api.CharacterID) (api.SceneSnapshot, error) {
	snap, ok, err := a.w.Where(ctx, characterID)
	if err != nil {
		return api.SceneSnapshot{}, err
	}
	if !ok {
		return api.SceneSnapshot{}, fmt.Errorf("character %q not found", characterID)
	}
	return snap, nil
}

func (a apiImpl) Who(ctx context.Context, sceneID api.SceneID) ([]api.CharacterRef, error) {
	return a.w.Who(ctx, sceneID)
}

func (a apiImpl) Characters(ctx context.Context) ([]api.CharacterRef, error) {
	return a.w.Characters(ctx)
}

func (a apiImpl) Places(ctx context.Context) ([]api.PlaceRef, error) {
	return a.w.Places(ctx)
}

func (a apiImpl) Log(ctx context.Context, since time.Duration) ([]api.LogEntry, error) {
	if since <= 0 {
		since = time.Hour
	}
	evs, err := a.w.store.Query(ctx, store.Filter{Since: time.Now().Add(-since)})
	if err != nil {
		return nil, err
	}
	out := make([]api.LogEntry, 0, len(evs))
	for _, ev := range evs {
		out = append(out, api.LogEntry{
			Timestamp: ev.Timestamp,
			SceneID:   ev.SceneID,
			Actor:     ev.Actor,
			Kind:      string(ev.Kind),
			Text:      store.TextOf(ev),
		})
	}
	return out, nil
}

// Recap rolls recent events into a short narrative. Narrator mode (empty
// characterID) walks the global event log; character mode pulls the named
// character's own Memory.Summary() so the recap reflects what *they*
// experienced, not the omniscient view.
func (a apiImpl) Recap(ctx context.Context, characterID api.CharacterID, since time.Duration) (string, error) {
	if since <= 0 {
		since = time.Hour
	}
	if characterID == "" {
		return a.recapNarrator(ctx, since)
	}
	return a.recapCharacter(ctx, characterID, since)
}

func (a apiImpl) recapNarrator(ctx context.Context, since time.Duration) (string, error) {
	entries, err := a.Log(ctx, since)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "(no events in window)", nil
	}
	var b strings.Builder
	for _, e := range entries {
		if e.Text == "" {
			fmt.Fprintf(&b, "%s/%s/%s\n", e.SceneID, e.Actor, e.Kind)
		} else {
			fmt.Fprintf(&b, "%s/%s/%s: %s\n", e.SceneID, e.Actor, e.Kind, e.Text)
		}
	}
	return a.w.model.Complete(ctx, llm.CompleteRequest{
		System:      "You are an omniscient narrator summarizing what happened in a chaotic IRC roleplay session. Be brief and vivid: 2-3 sentences, present tense.",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: fmt.Sprintf("Recap the last %s as a short paragraph:\n\n%s", since, b.String())}},
		MaxTokens:   200,
		Temperature: 0.7,
	})
}

func (a apiImpl) recapCharacter(ctx context.Context, characterID api.CharacterID, since time.Duration) (string, error) {
	chars, err := a.w.requestCharactersByID(ctx, []api.CharacterID{characterID})
	if err != nil {
		return "", err
	}
	ch := chars[0]
	memSummary := ch.Memory.Summary()
	if strings.TrimSpace(memSummary) == "" {
		return fmt.Sprintf("%s has no memories yet.", ch.Name), nil
	}
	system := fmt.Sprintf("You are %s. %s\nRecap recent events from your own perspective and in your own voice: 2-3 sentences, present tense.", ch.Name, ch.Persona)
	user := fmt.Sprintf("Your recent memories (oldest first):\n\n%s\nRecap the last %s in your voice.", memSummary, since)
	return a.w.model.Complete(ctx, llm.CompleteRequest{
		System:      system,
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: user}},
		MaxTokens:   200,
		Temperature: 0.7,
	})
}

func (a apiImpl) Describe(_ context.Context, id string) (string, error) {
	if id == "" {
		return "", errors.New("empty id")
	}
	return fmt.Sprintf("(no rich description for %q yet)", id), nil
}
