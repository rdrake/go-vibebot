package irc

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
)

type SummonCall struct{ PlaceID api.PlaceID }

type SummonNewCall struct {
	PlaceID     api.PlaceID
	NPCs        []api.CharacterID
	Description string
}

type fakeWorld struct {
	mu sync.Mutex

	LogReturn        []api.LogEntry
	CharactersReturn []api.CharacterRef
	PlacesReturn     []api.PlaceRef

	LogErr        error
	CharactersErr error
	PlacesErr     error

	SummonErr      error
	SummonCalls    []SummonCall
	SummonNewErr   error
	SummonNewScene api.SceneID
	SummonNewCalls []SummonNewCall

	LogCalls []time.Duration
}

var _ api.WorldAPI = (*fakeWorld)(nil)

func (f *fakeWorld) InjectEvent(context.Context, api.SceneID, string, string) error { return nil }

func (f *fakeWorld) Summon(_ context.Context, placeID api.PlaceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SummonCalls = append(f.SummonCalls, SummonCall{placeID})
	return f.SummonErr
}

func (f *fakeWorld) SummonNew(_ context.Context, placeID api.PlaceID, npcs []api.CharacterID, description string) (api.SceneID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SummonNewCalls = append(f.SummonNewCalls, SummonNewCall{placeID, npcs, description})
	return f.SummonNewScene, f.SummonNewErr
}

func (f *fakeWorld) Nudge(context.Context, api.CharacterID) error { return nil }
func (f *fakeWorld) Where(context.Context, api.CharacterID) (api.SceneSnapshot, error) {
	return api.SceneSnapshot{}, errors.New("not implemented in fake")
}
func (f *fakeWorld) Who(context.Context, api.SceneID) ([]api.CharacterRef, error) {
	return nil, errors.New("not implemented in fake")
}
func (f *fakeWorld) Describe(context.Context, string) (string, error) {
	return "", errors.New("not implemented in fake")
}

func (f *fakeWorld) Log(_ context.Context, since time.Duration) ([]api.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LogCalls = append(f.LogCalls, since)
	return f.LogReturn, f.LogErr
}

func (f *fakeWorld) Characters(context.Context) ([]api.CharacterRef, error) {
	return f.CharactersReturn, f.CharactersErr
}

func (f *fakeWorld) Places(context.Context) ([]api.PlaceRef, error) {
	return f.PlacesReturn, f.PlacesErr
}
