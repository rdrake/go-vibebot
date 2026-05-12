package world

import (
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/scene"
)

// snapshotOf flattens a scene's owned state into an immutable snapshot.
// Called only from the coordinator goroutine.
func snapshotOf(sc *scene.Scene) api.SceneSnapshot {
	members := make([]api.CharacterRef, 0, len(sc.Members))
	for _, m := range sc.Members {
		members = append(members, api.CharacterRef{ID: m.ID, Name: m.Name, Blurb: m.Blurb})
	}
	var leader api.CharacterID
	if sc.Leader != nil {
		leader = sc.Leader.ID
	}
	return api.SceneSnapshot{
		SceneID:  sc.ID,
		PlaceID:  sc.PlaceID,
		Leader:   leader,
		Members:  members,
		Captured: time.Now().UTC(),
	}
}
