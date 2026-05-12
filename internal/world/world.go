// Package world owns the single coordinator goroutine that holds all
// mutable world state. Adapters and scene goroutines communicate with it
// exclusively via channels.
package world

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/scene"
	"github.com/afternet/go-vibebot/internal/store"
)

// Config tunes the coordinator.
type Config struct {
	TickInterval time.Duration
	Logger       *slog.Logger
}

// World is the coordinator. Construct with New, register scenes, then Run.
// RegisterScene must not be called after Run starts.
type World struct {
	cfg    Config
	store  store.EventStore
	model  llm.LLM
	logger *slog.Logger

	commands     chan Command
	groupActions chan GroupAction
	whereReq     chan whereReq
	whoReq       chan whoReq

	// owned by coordinator goroutine after Run starts
	scenes     map[api.SceneID]*scene.Scene
	sceneOrder []api.SceneID
	characters map[api.CharacterID]*character.Character
	charScene  map[api.CharacterID]api.SceneID

	running    atomic.Bool
	memberWG   sync.WaitGroup
	memberCtx  context.Context
	memberStop context.CancelFunc
}

// New constructs a World. RegisterScene before Run.
func New(cfg Config, st store.EventStore, model llm.LLM) *World {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 2 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &World{
		cfg:          cfg,
		store:        st,
		model:        model,
		logger:       cfg.Logger,
		commands:     make(chan Command, 16),
		groupActions: make(chan GroupAction, 16),
		whereReq:     make(chan whereReq),
		whoReq:       make(chan whoReq),
		scenes:       make(map[api.SceneID]*scene.Scene),
		characters:   make(map[api.CharacterID]*character.Character),
		charScene:    make(map[api.CharacterID]api.SceneID),
	}
}

// RegisterScene records a scene and its members. It must be called before
// Run; calling it after Run starts panics — silent corruption is worse.
func (w *World) RegisterScene(s *scene.Scene) {
	if w.running.Load() {
		panic("world: RegisterScene called after Run")
	}
	if _, dup := w.scenes[s.ID]; dup {
		panic(fmt.Sprintf("world: duplicate scene id %q", s.ID))
	}
	w.scenes[s.ID] = s
	w.sceneOrder = append(w.sceneOrder, s.ID)
	for _, m := range s.Members {
		w.characters[m.ID] = m
		w.charScene[m.ID] = s.ID
	}
}

// Commands is the channel adapters write to. Callers never close it.
func (w *World) Commands() chan<- Command { return w.commands }

// Run blocks until ctx is cancelled. It starts member goroutines and
// dispatches commands to scenes serially (one scene turn at a time, in the
// skeleton). Returns nil on clean shutdown.
func (w *World) Run(ctx context.Context) error {
	if !w.running.CompareAndSwap(false, true) {
		return errors.New("world: Run called twice")
	}
	defer w.running.Store(false)

	w.memberCtx, w.memberStop = context.WithCancel(ctx)
	defer w.memberStop()

	for _, c := range w.characters {
		w.memberWG.Add(1)
		go func(c *character.Character) {
			defer w.memberWG.Done()
			c.Run(w.memberCtx, w.model)
		}(c)
	}

	ticker := time.NewTicker(w.cfg.TickInterval)
	defer ticker.Stop()

	w.logger.Info("world running",
		"scenes", len(w.scenes),
		"characters", len(w.characters),
		"tick", w.cfg.TickInterval,
	)

	for {
		select {
		case <-ctx.Done():
			w.memberStop()
			w.memberWG.Wait()
			return nil
		case cmd := <-w.commands:
			w.handleCommand(ctx, cmd)
		case act := <-w.groupActions:
			w.handleGroupAction(ctx, act)
		case t := <-ticker.C:
			w.handleTick(ctx, t)
		case req := <-w.whereReq:
			req.reply <- w.lookupWhere(req.charID)
		case req := <-w.whoReq:
			req.reply <- w.lookupWho(req.sceneID)
		}
	}
}

func (w *World) handleCommand(ctx context.Context, cmd Command) {
	switch c := cmd.(type) {
	case Inject:
		c.Reply <- w.dispatchInject(ctx, c.SceneID, c.Target, c.Description)
	case Summon:
		c.Reply <- w.dispatchSummon(ctx, c.PlaceID)
	case Nudge:
		c.Reply <- w.dispatchNudge(ctx, c.CharacterID)
	default:
		// Unreachable: Command is sealed and every variant is handled above.
		// Panicking forces failures here instead of silently swallowing.
		panic(fmt.Sprintf("world: unhandled command %T", cmd))
	}
}

func (w *World) dispatchInject(ctx context.Context, sceneID api.SceneID, target, desc string) error {
	sc := w.resolveScene(sceneID)
	if sc == nil {
		if sceneID == "" {
			return errors.New("world: no scene registered")
		}
		return fmt.Errorf("world: scene %q not found", sceneID)
	}
	ev := store.NewInjectEvent(sc.ID, target, desc)
	ev.Timestamp = time.Now().UTC()

	// Hard rule: append BEFORE broadcast.
	if err := w.store.Append(ctx, &ev); err != nil {
		return fmt.Errorf("append inject: %w", err)
	}

	result, err := sc.Orchestrate(ctx, w.model, ev)
	for _, u := range result.Utterances {
		speech := store.NewSpeechEvent(sc.ID, u.CharacterID, u.Text)
		if appendErr := w.store.Append(ctx, &speech); appendErr != nil {
			return fmt.Errorf("append speech: %w", appendErr)
		}
	}
	if err != nil {
		w.logger.Error("orchestrate", "err", err)
		return err
	}

	if result.Synthesized == "" || sc.Leader == nil {
		return nil
	}
	synthEv := store.NewSynthesizedEvent(sc.ID, sc.Leader.ID, result.Synthesized)
	if err := w.appendOnly(ctx, synthEv); err != nil {
		return err
	}
	return sc.BroadcastForMemory(ctx, synthEv)
}

// resolveScene returns the scene for the given id, or the default scene
// when the id is empty. Returns nil when the id is non-empty and no scene
// matches, or when no scenes are registered at all.
func (w *World) resolveScene(sceneID api.SceneID) *scene.Scene {
	if sceneID == "" {
		return w.defaultScene()
	}
	return w.scenes[sceneID]
}

func (w *World) dispatchSummon(ctx context.Context, placeID api.PlaceID) error {
	sceneID := api.SceneID("place:" + string(placeID))
	sc, ok := w.scenes[sceneID]
	if !ok {
		return fmt.Errorf("world: unknown place %q", placeID)
	}
	return w.appendOnly(ctx, store.NewSummonEvent(sc.ID, placeID))
}

func (w *World) dispatchNudge(ctx context.Context, charID api.CharacterID) error {
	sceneID, ok := w.charScene[charID]
	if !ok {
		return fmt.Errorf("world: character %q not in any scene", charID)
	}
	return w.appendOnly(ctx, store.NewNudgeEvent(sceneID, charID))
}

func (w *World) handleGroupAction(ctx context.Context, act GroupAction) {
	_ = w.appendOnly(ctx, store.Event{
		Source:  store.SourceGroup,
		SceneID: act.SceneID,
		Actor:   "group",
		Kind:    store.Kind(act.Kind),
		Payload: store.MarshalText(act.Text),
	})
}

func (w *World) handleTick(ctx context.Context, t time.Time) {
	sc := w.defaultScene()
	if sc == nil {
		return
	}
	ev := store.NewAmbientEvent(sc.ID, "time passes")
	ev.Timestamp = t.UTC()
	_ = w.appendOnly(ctx, ev)
}

func (w *World) appendOnly(ctx context.Context, ev store.Event) error {
	if err := w.store.Append(ctx, &ev); err != nil {
		w.logger.Error("append event", "kind", ev.Kind, "err", err)
		return err
	}
	return nil
}

// defaultScene returns the first registered scene, deterministic across
// runs (a bare map iteration would not be). The skeleton's single-scene
// case is unaffected; multi-scene routing in callers should use explicit
// scene IDs and treat the default as a back-compat fallback.
func (w *World) defaultScene() *scene.Scene {
	if len(w.sceneOrder) == 0 {
		return nil
	}
	return w.scenes[w.sceneOrder[0]]
}
