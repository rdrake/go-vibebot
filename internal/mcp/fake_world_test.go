package mcp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
)

// fakeWorld records every WorldAPI call and serves canned reads. Tests
// configure it with Err / Characters / Places / Log fields and then
// assert on the recorded *Call slices.
type fakeWorld struct {
	mu sync.Mutex

	// Inputs to be returned by reads.
	CharactersReturn []api.CharacterRef
	PlacesReturn     []api.PlaceRef
	LogReturn        []api.LogEntry

	// Programmable errors per verb. Zero value = no error.
	InjectErr     error
	SummonErr     error
	NudgeErr      error
	LogErr        error
	CharactersErr error
	PlacesErr     error

	SummonNewErr   error
	SummonNewScene api.SceneID
	SummonNewCalls []SummonNewCall

	RecapReturn string
	RecapErr    error
	RecapCalls  []RecapCall

	// Recorded calls.
	InjectCalls []InjectCall
	SummonCalls []SummonCall
	NudgeCalls  []NudgeCall
	LogCalls    []LogCall
}

type RecapCall struct {
	CharacterID api.CharacterID
	Since       time.Duration
}

type InjectCall struct {
	SceneID     api.SceneID
	Target      string
	Description string
}

type SummonCall struct{ PlaceID api.PlaceID }

type SummonNewCall struct {
	PlaceID     api.PlaceID
	NPCs        []api.CharacterID
	Description string
}

type NudgeCall struct{ CharacterID api.CharacterID }
type LogCall struct{ Since time.Duration }

var _ api.WorldAPI = (*fakeWorld)(nil)

func (f *fakeWorld) InjectEvent(_ context.Context, sceneID api.SceneID, target, description string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.InjectCalls = append(f.InjectCalls, InjectCall{sceneID, target, description})
	return f.InjectErr
}

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

func (f *fakeWorld) Nudge(_ context.Context, characterID api.CharacterID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.NudgeCalls = append(f.NudgeCalls, NudgeCall{characterID})
	return f.NudgeErr
}

func (f *fakeWorld) Where(_ context.Context, _ api.CharacterID) (api.SceneSnapshot, error) {
	return api.SceneSnapshot{}, errors.New("not implemented in fake")
}

func (f *fakeWorld) Log(_ context.Context, since time.Duration) ([]api.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LogCalls = append(f.LogCalls, LogCall{since})
	return f.LogReturn, f.LogErr
}

func (f *fakeWorld) Who(_ context.Context, _ api.SceneID) ([]api.CharacterRef, error) {
	return nil, errors.New("not implemented in fake")
}

func (f *fakeWorld) Describe(_ context.Context, _ string) (string, error) {
	return "", errors.New("not implemented in fake")
}

func (f *fakeWorld) Recap(_ context.Context, characterID api.CharacterID, since time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RecapCalls = append(f.RecapCalls, RecapCall{characterID, since})
	return f.RecapReturn, f.RecapErr
}

func (f *fakeWorld) Characters(_ context.Context) ([]api.CharacterRef, error) {
	return f.CharactersReturn, f.CharactersErr
}

func (f *fakeWorld) Places(_ context.Context) ([]api.PlaceRef, error) {
	return f.PlacesReturn, f.PlacesErr
}
