package world

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
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

func (a apiImpl) SummonNew(_ context.Context, _ api.PlaceID, _ []api.CharacterID, _ string) (api.SceneID, error) {
	return "", errors.New("world: SummonNew not yet implemented")
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

func (a apiImpl) Describe(_ context.Context, id string) (string, error) {
	if id == "" {
		return "", errors.New("empty id")
	}
	return fmt.Sprintf("(no rich description for %q yet)", id), nil
}
