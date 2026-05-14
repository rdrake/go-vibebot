package world

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
)

type whereReq struct {
	charID api.CharacterID
	reply  chan whereResp
}

type whereResp struct {
	snap api.SceneSnapshot
	ok   bool
}

type whoReq struct {
	sceneID api.SceneID
	reply   chan []api.CharacterRef
}

// Where returns a scene snapshot for the character, marshalled via the
// coordinator goroutine so reads observe a consistent state.
func (w *World) Where(ctx context.Context, charID api.CharacterID) (api.SceneSnapshot, bool, error) {
	rep := make(chan whereResp, 1)
	select {
	case w.whereReq <- whereReq{charID: charID, reply: rep}:
	case <-ctx.Done():
		return api.SceneSnapshot{}, false, ctx.Err()
	}
	select {
	case r := <-rep:
		return r.snap, r.ok, nil
	case <-ctx.Done():
		return api.SceneSnapshot{}, false, ctx.Err()
	}
}

// Who returns the members of a scene by ID.
func (w *World) Who(ctx context.Context, sceneID api.SceneID) ([]api.CharacterRef, error) {
	rep := make(chan []api.CharacterRef, 1)
	select {
	case w.whoReq <- whoReq{sceneID: sceneID, reply: rep}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-rep:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *World) lookupWhere(charID api.CharacterID) whereResp {
	sceneID, ok := w.charScene[charID]
	if !ok {
		return whereResp{}
	}
	sc := w.scenes[sceneID]
	if sc == nil {
		return whereResp{}
	}
	return whereResp{snap: snapshotOf(sc), ok: true}
}

func (w *World) lookupWho(sceneID api.SceneID) []api.CharacterRef {
	sc := w.scenes[sceneID]
	if sc == nil {
		return nil
	}
	return snapshotOf(sc).Members
}

type charactersReq struct {
	reply chan []api.CharacterRef
}

type placesReq struct {
	reply chan []api.PlaceRef
}

// Characters lists every character registered with the world, marshalled
// via the coordinator goroutine so concurrent scene mutation cannot tear
// the result.
func (w *World) Characters(ctx context.Context) ([]api.CharacterRef, error) {
	rep := make(chan []api.CharacterRef, 1)
	select {
	case w.charactersReq <- charactersReq{reply: rep}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-rep:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Places lists every place-bound scene, in registration order.
func (w *World) Places(ctx context.Context) ([]api.PlaceRef, error) {
	rep := make(chan []api.PlaceRef, 1)
	select {
	case w.placesReq <- placesReq{reply: rep}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-rep:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *World) lookupCharacters() []api.CharacterRef {
	out := make([]api.CharacterRef, 0, len(w.characters))
	for id, c := range w.characters {
		out = append(out, api.CharacterRef{ID: id, Name: c.Name, Blurb: c.Blurb})
	}
	// Stable order — map iteration is not.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (w *World) lookupPlaces() []api.PlaceRef {
	out := make([]api.PlaceRef, 0)
	for _, sid := range w.sceneOrder {
		sc := w.scenes[sid]
		if sc == nil || sc.PlaceID == "" {
			continue
		}
		members := make([]api.CharacterRef, 0, len(sc.Members))
		for _, m := range sc.Members {
			members = append(members, api.CharacterRef{ID: m.ID, Name: m.Name, Blurb: m.Blurb})
		}
		ref := api.PlaceRef{
			ID:      sc.PlaceID,
			SceneID: sc.ID,
			Members: members,
		}
		if sc.Leader != nil {
			ref.Leader = sc.Leader.ID
		}
		out = append(out, ref)
	}
	return out
}

type charactersByIDReq struct {
	ids   []api.CharacterID
	reply chan charactersByIDResp
}

type charactersByIDResp struct {
	chars []*character.Character
	err   error
}

func (w *World) lookupCharactersByID(ids []api.CharacterID) charactersByIDResp {
	out := make([]*character.Character, 0, len(ids))
	var missing []string
	for _, id := range ids {
		c, ok := w.characters[id]
		if !ok {
			missing = append(missing, string(id))
			continue
		}
		out = append(out, c)
	}
	if len(missing) > 0 {
		return charactersByIDResp{
			err: fmt.Errorf("unknown character(s): %s", strings.Join(missing, ", ")),
		}
	}
	return charactersByIDResp{chars: out}
}

// requestCharactersByID posts to the coordinator and awaits the reply.
// Mirrors the where/who helpers.
func (w *World) requestCharactersByID(ctx context.Context, ids []api.CharacterID) ([]*character.Character, error) {
	reply := make(chan charactersByIDResp, 1)
	select {
	case w.charactersByIDReq <- charactersByIDReq{ids: ids, reply: reply}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case resp := <-reply:
		return resp.chars, resp.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
