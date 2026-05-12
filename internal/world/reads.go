package world

import (
	"context"

	"github.com/afternet/go-vibebot/internal/api"
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
