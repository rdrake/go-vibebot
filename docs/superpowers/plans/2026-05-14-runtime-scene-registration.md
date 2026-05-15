# Runtime Scene Registration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lift the post-`Run` panic on `World.RegisterScene` and ship `WorldAPI.SummonNew(placeID, npcs, description)` so IRC `!summon <id> n=...` and the MCP `summon` tool can spin up a new place-scene at runtime against the live coordinator, using characters already loaded from `seed/characters.yaml`.

**Architecture:** Add a new `RegisterSceneCmd` variant to the existing sealed `Command` interface drained by the coordinator goroutine. The boot helper and the runtime command both call a shared `registerSceneLocked`. `apiImpl.SummonNew` routes the register round-trip through the existing `send` helper, then chains the existing `Summon` and (optionally) `Inject` commands. NPC ids are resolved to `*character.Character` pointers via a new `charactersByIDReq` request channel that mirrors `whereReq`/`whoReq`.

**Tech Stack:** Go 1.24, the existing `internal/world` channels, no new dependencies. Tests use the existing `newTestWorld(t)` helper and `mockLLM`/`echoLLM`.

**Spec:** `docs/superpowers/specs/2026-05-14-runtime-scene-registration-design.md`

---

## File Structure

- Modify: `internal/api/api.go` — add `SummonNew` to the `WorldAPI` interface; add a comment on `Where`/`Nudge` documenting boot-scene asymmetry.
- Modify: `internal/world/messages.go` — add `RegisterSceneCmd` struct + `isCommand()` marker.
- Modify: `internal/world/world.go` — extract `registerSceneLocked`; turn `RegisterScene` into a panic-on-error boot wrapper; add `charactersByIDReq` field; init in `New`; add `select` arm in `Run`; add `lookupCharactersByID`; add `requestCharactersByID` helper; add `case RegisterSceneCmd:` in `handleCommand`.
- Modify: `internal/world/api.go` — add `apiImpl.SummonNew` body that uses the existing `send` helper.
- Modify: `internal/world/world_test.go` — tests 1–11 from the spec.
- Modify: `internal/irc/adapter.go` — rewrite `cmdSummon`; add `parseSummonArgs` next to `parseInjectArgs`.
- Modify: `internal/irc/inject_parse_test.go` — append parser tests 12–19 (or create `summon_parse_test.go` if the maintainer prefers).
- Modify: `internal/irc/adapter_test.go` — append `TestCmdSummonNewRoutesToSummonNew` (test 20).
- Modify: `internal/irc/fake_world_test.go` — add `SummonNew` method recording `SummonNewCall`.
- Modify: `internal/mcp/tools.go` — extend `SummonInput`, add `SceneID` to `SummonOutput`, rewrite `summonHandler`.
- Modify: `internal/mcp/tools_test.go` and/or `internal/mcp/adapter_test.go` — tests 21–23.
- Modify: `internal/mcp/fake_world_test.go` — add `SummonNew` method recording `SummonNewCall`.
- Modify: `cmd/sim/smoke_test.go` — append `TestRuntimeAdHocPlaceSummonViaIRC` (test 24).
- Modify: `README.md` — one line under IRC commands documenting the new syntax.
- Modify: `BACKLOG.md` — mark "Runtime scene registration" deferred follow-up as shipped, strikethrough convention.

---

## Pre-flight verification

- [ ] **Step 0.1: Confirm the repo compiles and tests pass before any changes**

Run: `go test ./...`
Expected: PASS across all packages.

If this fails, stop and report — the plan assumes a green starting state.

- [ ] **Step 0.2: Re-read the spec sections "Architecture", "Components", and "Tests"**

The plan code blocks intentionally mirror the spec sketches. Any divergence is a plan bug; treat the spec as authoritative.

---

## Task 1: Interface scaffolding

Add the new method to `WorldAPI` and grow both fake worlds so the rest of the codebase keeps compiling while later tasks fill in real bodies. No behaviour changes yet.

> **Atomicity warning:** Steps 1.1 through 1.4 must all be applied before running `go build`/`go test` or committing. Step 1.1 alone leaves both `fake_world_test.go` files unable to satisfy `var _ api.WorldAPI = (*fakeWorld)(nil)`, which breaks the entire test build. Do not commit until Step 1.5 passes.

**Files:**
- Modify: `internal/api/api.go`
- Modify: `internal/world/api.go`
- Modify: `internal/irc/fake_world_test.go`
- Modify: `internal/mcp/fake_world_test.go`

- [ ] **Step 1.1: Add `SummonNew` to the `WorldAPI` interface**

Edit `internal/api/api.go`. Inside the `WorldAPI` interface, locate the writes block and add `SummonNew` immediately after `Summon`. Also add a paragraph-level comment on the `Where`/`Nudge` declarations documenting the boot-scene asymmetry.

The full writes block, after edit:

```go
// Writes — externally-driven scenarios and pokes.
InjectEvent(ctx context.Context, sceneID SceneID, target, description string) error
Summon(ctx context.Context, placeID PlaceID) error
// SummonNew registers a new ad-hoc place-scene at runtime using existing
// characters and returns the new scene id. npcs must be non-empty and
// reference ids returned by Characters(); the first id is the leader.
// If description is non-empty it is recorded as an inject scoped to the
// new scene after the summon event.
SummonNew(ctx context.Context, placeID PlaceID, npcs []CharacterID, description string) (SceneID, error)
Nudge(ctx context.Context, characterID CharacterID) error
```

And on the reads block, prepend an interface-level note above `Where`:

```go
// Where and Nudge resolve a character against the first scene that
// registered them (boot-time wins). A character that has been added to
// an ad-hoc scene via SummonNew still resolves to its boot-time scene
// for these calls; to act inside an ad-hoc scene use InjectEvent with
// the scene id returned by SummonNew.
Where(ctx context.Context, characterID CharacterID) (SceneSnapshot, error)
```

- [ ] **Step 1.2: Stub `apiImpl.SummonNew`**

Edit `internal/world/api.go`. Add this method immediately after `apiImpl.Summon`:

```go
func (a apiImpl) SummonNew(_ context.Context, _ api.PlaceID, _ []api.CharacterID, _ string) (api.SceneID, error) {
	return "", errors.New("world: SummonNew not yet implemented")
}
```

(The real body lands in Task 4.)

- [ ] **Step 1.3: Add a `SummonNew` stub to the IRC fake world (and harden `Summon` recording)**

Edit `internal/irc/fake_world_test.go`. The existing `Summon` method returns `nil` without recording — add a `SummonCalls` slice while we're here so Task 7's legacy-path assertion can actually verify the call happened.

Add the call types:

```go
type SummonCall struct{ PlaceID api.PlaceID }

type SummonNewCall struct {
	PlaceID     api.PlaceID
	NPCs        []api.CharacterID
	Description string
}
```

Inside the `fakeWorld` struct, add:

```go
SummonErr      error
SummonCalls    []SummonCall
SummonNewErr   error
SummonNewScene api.SceneID
SummonNewCalls []SummonNewCall
```

Replace the existing one-line `Summon` method with:

```go
func (f *fakeWorld) Summon(_ context.Context, placeID api.PlaceID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SummonCalls = append(f.SummonCalls, SummonCall{placeID})
	return f.SummonErr
}
```

Add the new `SummonNew` method (anywhere among the other satisfiers):

```go
func (f *fakeWorld) SummonNew(_ context.Context, placeID api.PlaceID, npcs []api.CharacterID, description string) (api.SceneID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SummonNewCalls = append(f.SummonNewCalls, SummonNewCall{placeID, npcs, description})
	return f.SummonNewScene, f.SummonNewErr
}
```

- [ ] **Step 1.4: Add a `SummonNew` stub to the MCP fake world**

Edit `internal/mcp/fake_world_test.go`. Same shape:

```go
type SummonNewCall struct {
	PlaceID     api.PlaceID
	NPCs        []api.CharacterID
	Description string
}
```

Struct additions:

```go
SummonNewErr   error
SummonNewScene api.SceneID
SummonNewCalls []SummonNewCall
```

Method:

```go
func (f *fakeWorld) SummonNew(_ context.Context, placeID api.PlaceID, npcs []api.CharacterID, description string) (api.SceneID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SummonNewCalls = append(f.SummonNewCalls, SummonNewCall{placeID, npcs, description})
	return f.SummonNewScene, f.SummonNewErr
}
```

- [ ] **Step 1.5: Compile-check**

Run: `go build ./...`
Expected: PASS. The interface assertions `var _ api.WorldAPI = (*fakeWorld)(nil)` in both fake-world files verify the new method satisfies the interface.

- [ ] **Step 1.6: Commit**

```bash
git add internal/api/api.go internal/world/api.go internal/irc/fake_world_test.go internal/mcp/fake_world_test.go
git commit -m "api: scaffold SummonNew on WorldAPI and fake worlds"
```

---

## Task 2: Extract `registerSceneLocked` and add `RegisterSceneCmd`

The boot helper currently mutates `w.scenes` directly and panics post-`Run`. Refactor the body into `registerSceneLocked` (returns error), keep the boot helper as a panic-on-error wrapper, and add the runtime command path.

**Files:**
- Modify: `internal/world/messages.go`
- Modify: `internal/world/world.go`
- Modify: `internal/world/world_test.go`

- [ ] **Step 2.1: Write the failing test**

Append to `internal/world/world_test.go`:

```go
func TestRegisterSceneAfterRunDoesNotPanic(t *testing.T) {
	w, _, _ := newTestWorld(t)

	// Build a second scene reusing the boot members (already in w.characters).
	w2 := &scene.Scene{
		ID:      api.SceneID("ad-hoc-1"),
		Members: []*character.Character{},
	}
	// Pull a registered character out of the boot scene.
	for _, m := range w.scenes[api.SceneID("scene-1")].Members {
		w2.Members = append(w2.Members, m)
		break
	}
	w2.Leader = w2.Members[0]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	reply := make(chan error, 1)
	select {
	case w.commands <- RegisterSceneCmd{Scene: w2, Reply: reply}:
	case <-time.After(time.Second):
		t.Fatal("could not post RegisterSceneCmd")
	}
	select {
	case err := <-reply:
		if err != nil {
			t.Fatalf("runtime register: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no reply from coordinator")
	}

	// Verify the new scene is reachable via the existing Inject command.
	if err := w.API().InjectEvent(ctx, "ad-hoc-1", "", "hello"); err != nil {
		t.Fatalf("inject against new scene: %v", err)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("world.Run did not return after cancel")
	}
}

func TestRegisterSceneLockedRejectsUnknownMember(t *testing.T) {
	// Direct test of the locked helper. Spec test #2 (which targets the
	// SummonNew API surface) is implemented in Task 4 once SummonNew exists.
	w, _, _ := newTestWorld(t)
	bad := &scene.Scene{
		ID:      api.SceneID("ghost"),
		Members: []*character.Character{{ID: "nobody", Inbox: make(chan character.Perception, 1)}},
	}
	bad.Leader = bad.Members[0]
	if err := w.registerSceneLocked(bad); err == nil {
		t.Fatal("expected unknown-character error")
	}
	if _, dup := w.scenes["ghost"]; dup {
		t.Fatal("scene must not be registered when validation fails")
	}
}

func TestRegisterSceneLockedDuplicateRejected(t *testing.T) {
	w, _, _ := newTestWorld(t)
	dup := &scene.Scene{
		ID:      api.SceneID("scene-1"),
		Members: w.scenes["scene-1"].Members,
		Leader:  w.scenes["scene-1"].Leader,
	}
	if err := w.registerSceneLocked(dup); err == nil {
		t.Fatal("expected duplicate-scene-id error")
	}
}
```

- [ ] **Step 2.2: Run the tests; verify they fail**

Run: `go test ./internal/world/ -run "TestRegisterSceneAfterRunDoesNotPanic|TestRegisterSceneLockedRejectsUnknownMember|TestRegisterSceneLockedDuplicateRejected" -v`
Expected: FAIL — `RegisterSceneCmd` and `registerSceneLocked` are undefined.

- [ ] **Step 2.3: Add the `RegisterSceneCmd` variant**

First, update the imports in `internal/world/messages.go`. The file currently has only `"github.com/afternet/go-vibebot/internal/api"`. Replace the import block with:

```go
import (
	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/scene"
)
```

(No import cycle: `internal/scene` imports `api`, `character`, `store` — not `world`.)

Then append after the existing `Nudge` declaration:

```go
// RegisterSceneCmd registers a fully-constructed scene on the live
// coordinator. The boot helper World.RegisterScene panics on error; this
// variant returns the error to the caller so runtime-registered scenes can
// fail cleanly (duplicate ids, unknown member characters).
type RegisterSceneCmd struct {
	Scene *scene.Scene
	Reply chan<- error
}
```

Add the marker beneath the existing `(Inject)`/`(Summon)`/`(Nudge)` block:

```go
func (RegisterSceneCmd) isCommand() {}
```

- [ ] **Step 2.4: Extract `registerSceneLocked` and rewrite the boot helper**

Edit `internal/world/world.go`. Locate the current `RegisterScene` method (it starts with the `w.running.Load()` panic guard). Replace its body with a thin wrapper, and add `registerSceneLocked` immediately below:

```go
// RegisterScene records a scene and its members. Must be called before
// Run. Panics on error — boot-time misconfiguration should fail loudly.
// For runtime registration, send RegisterSceneCmd on World.Commands().
//
// Boot semantics preserved: members of registered scenes are inserted
// into w.characters here, before registerSceneLocked validates that all
// members already exist. This keeps cmd/sim/main.go's existing wiring
// (which calls RegisterScene with fresh *character.Character values that
// have not yet been seen by World) working unchanged. The runtime path
// (RegisterSceneCmd from the coordinator) does NOT populate characters
// — it relies on this boot pre-population.
func (w *World) RegisterScene(s *scene.Scene) {
	if w.running.Load() {
		panic("world: RegisterScene called after Run — use WorldAPI.SummonNew")
	}
	// Pre-populate the character map so registerSceneLocked's existence
	// check passes for boot-time scenes whose members are fresh values.
	for _, m := range s.Members {
		if m == nil {
			continue
		}
		w.characters[m.ID] = m
	}
	if err := w.registerSceneLocked(s); err != nil {
		panic("world: " + err.Error())
	}
}

// registerSceneLocked is the single mutation point for w.scenes,
// w.sceneOrder, and w.charScene. It is read-only against w.characters —
// members must already be registered. Only the coordinator goroutine
// (or the pre-Run boot helper, which pre-populates w.characters above)
// may invoke it.
func (w *World) registerSceneLocked(s *scene.Scene) error {
	if s == nil || s.ID == "" {
		return errors.New("world: scene must have an id")
	}
	if _, dup := w.scenes[s.ID]; dup {
		return fmt.Errorf("world: duplicate scene id %q", s.ID)
	}
	for _, m := range s.Members {
		if _, ok := w.characters[m.ID]; !ok {
			return fmt.Errorf("world: scene %q references unknown character %q", s.ID, m.ID)
		}
	}
	w.scenes[s.ID] = s
	w.sceneOrder = append(w.sceneOrder, s.ID)
	for _, m := range s.Members {
		if _, has := w.charScene[m.ID]; !has {
			w.charScene[m.ID] = s.ID
		}
	}
	return nil
}
```

Why split the character-map write out of `registerSceneLocked`: the spec's helper is read-only against `w.characters` so the runtime path can fail fast on unknown ids. Boot is allowed to introduce characters because no coordinator goroutine has read the map yet, and `cmd/sim/main.go` constructs `*character.Character` values inside scene construction. Centralising the boot pre-population in the wrapper keeps the helper spec-compliant.

- [ ] **Step 2.5: Dispatch the new command**

Edit `internal/world/world.go`. In `handleCommand`, add the new case immediately before the `default` arm:

```go
case RegisterSceneCmd:
	c.Reply <- w.registerSceneLocked(c.Scene)
```

Update the `default:` comment to list the new variant:

```go
default:
	// Unreachable: Command is sealed and every variant is handled above
	// (Inject, Summon, Nudge, RegisterSceneCmd). Panicking forces failures
	// here instead of silently swallowing.
	panic(fmt.Sprintf("world: unhandled command %T", cmd))
```

- [ ] **Step 2.6: Run the tests; verify they pass**

Run: `go test ./internal/world/ -run "TestRegisterSceneAfterRunDoesNotPanic|TestRegisterSceneLockedRejectsUnknownMember|TestRegisterSceneLockedDuplicateRejected" -v`
Expected: PASS.

Then run the full world package: `go test ./internal/world/ -v`
Expected: PASS.

- [ ] **Step 2.7: Commit**

```bash
git add internal/world/messages.go internal/world/world.go internal/world/world_test.go
git commit -m "world: RegisterSceneCmd for runtime scene registration"
```

---

## Task 3: `charactersByIDReq` channel plumbing

Add a coordinator-side lookup that resolves a slice of `CharacterID` to `*character.Character` pointers. This is the first round-trip inside `SummonNew`.

**Files:**
- Modify: `internal/world/world.go`
- Modify: `internal/world/reads.go`
- Modify: `internal/world/world_test.go`

- [ ] **Step 3.1: Write the failing test**

Append to `internal/world/world_test.go`:

```go
func TestRequestCharactersByIDResolvesExisting(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	chars, err := w.requestCharactersByID(ctx, []api.CharacterID{"leader", "m1"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(chars) != 2 || chars[0].ID != "leader" || chars[1].ID != "m1" {
		t.Fatalf("unexpected chars: %+v", chars)
	}

	cancel()
	<-runDone
}

func TestRequestCharactersByIDReportsMissing(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	_, err := w.requestCharactersByID(ctx, []api.CharacterID{"leader", "ghost", "wisp"})
	if err == nil {
		t.Fatal("expected error for missing characters")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "wisp") {
		t.Fatalf("error should name missing ids, got: %v", err)
	}

	cancel()
	<-runDone
}
```

If `strings` isn't already imported in the test file, add it.

- [ ] **Step 3.2: Run; verify it fails**

Run: `go test ./internal/world/ -run "TestRequestCharactersByID" -v`
Expected: FAIL — `requestCharactersByID` undefined.

- [ ] **Step 3.3: Add the channel field and init**

Edit `internal/world/world.go`. In the `World` struct, after `placesReq chan placesReq`, add:

```go
charactersByIDReq chan charactersByIDReq
```

In `New`, after `placesReq: make(chan placesReq)`, add:

```go
charactersByIDReq: make(chan charactersByIDReq),
```

- [ ] **Step 3.4: Add the request/response types and helpers**

Edit `internal/world/reads.go`. Append after the existing read-helper definitions:

```go
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
```

Ensure `reads.go` imports include `"context"`, `"fmt"`, `"strings"`, `"github.com/afternet/go-vibebot/internal/api"`, and `"github.com/afternet/go-vibebot/internal/character"`.

- [ ] **Step 3.5: Add the `select` arm in `Run`**

Edit `internal/world/world.go`. In the main `select` inside `Run`, add an arm next to the other read-request arms:

```go
case req := <-w.charactersByIDReq:
	req.reply <- w.lookupCharactersByID(req.ids)
```

- [ ] **Step 3.6: Run the tests; verify they pass**

Run: `go test ./internal/world/ -run "TestRequestCharactersByID" -v`
Expected: PASS.

Then run the full world package: `go test ./internal/world/ -v`
Expected: PASS.

- [ ] **Step 3.7: Commit**

```bash
git add internal/world/world.go internal/world/reads.go internal/world/world_test.go
git commit -m "world: charactersByIDReq round-trip for runtime scene registration"
```

---

## Task 4: Implement `apiImpl.SummonNew`

Replace the Task 1 stub with the real body. Use the existing `send` helper for the register round-trip, then chain `Summon` and optional `Inject`.

**Files:**
- Modify: `internal/world/api.go`
- Modify: `internal/world/world_test.go`

- [ ] **Step 4.1: Write the failing tests**

Append to `internal/world/world_test.go`:

```go
func TestSummonNewWithoutDescriptionEmitsOnlySummon(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	sceneID, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader", "m1"}, "")
	if err != nil {
		t.Fatalf("SummonNew: %v", err)
	}
	if sceneID != "place:spire" {
		t.Fatalf("want sceneID place:spire, got %q", sceneID)
	}

	evs, err := st.Query(ctx, store.Filter{SceneID: sceneID})
	if err != nil {
		t.Fatal(err)
	}
	var summonCount, injectCount int
	for _, e := range evs {
		switch e.Kind {
		case store.KindSummon:
			summonCount++
		case store.KindInject:
			injectCount++
		}
	}
	if summonCount != 1 {
		t.Errorf("want 1 KindSummon, got %d", summonCount)
	}
	if injectCount != 0 {
		t.Errorf("want 0 KindInject, got %d", injectCount)
	}

	cancel()
	<-runDone
}

func TestSummonNewWithDescriptionWritesInject(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	sceneID, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader", "m1"}, "A drafty steeple.")
	if err != nil {
		t.Fatalf("SummonNew: %v", err)
	}

	evs, err := st.Query(ctx, store.Filter{SceneID: sceneID})
	if err != nil {
		t.Fatal(err)
	}
	var summonCount, injectCount int
	var injectDesc string
	for _, e := range evs {
		switch e.Kind {
		case store.KindSummon:
			summonCount++
		case store.KindInject:
			injectCount++
			injectDesc = store.TextOf(e)
		}
	}
	if summonCount != 1 || injectCount != 1 {
		t.Fatalf("want 1 summon + 1 inject, got %d/%d", summonCount, injectCount)
	}
	if injectDesc != "A drafty steeple." {
		t.Errorf("inject text: want %q, got %q", "A drafty steeple.", injectDesc)
	}

	cancel()
	<-runDone
}

func TestSummonNewUnknownCharacterErrors(t *testing.T) {
	// Spec test #2: drives the full SummonNew API path, including the
	// charactersByIDReq round-trip, not just the locked helper.
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	sceneCountBefore := len(w.scenes)
	_, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader", "ghost"}, "")
	if err == nil {
		t.Fatal("expected unknown-character error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should name the missing id, got %v", err)
	}
	if got := len(w.scenes); got != sceneCountBefore {
		t.Errorf("scenes map changed: before=%d after=%d", sceneCountBefore, got)
	}

	cancel()
	<-runDone
}

func TestSummonNewRejectsEmptyInputs(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	if _, err := w.API().SummonNew(ctx, "", []api.CharacterID{"leader"}, ""); err == nil {
		t.Error("expected error for empty place id")
	}
	if _, err := w.API().SummonNew(ctx, "spire", nil, ""); err == nil {
		t.Error("expected error for nil npcs")
	}
	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{}, ""); err == nil {
		t.Error("expected error for empty npcs")
	}

	cancel()
	<-runDone
}

func TestSummonNewDuplicatePlaceErrors(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader"}, ""); err != nil {
		t.Fatalf("first SummonNew: %v", err)
	}
	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader"}, ""); err == nil {
		t.Fatal("second SummonNew of same place should error")
	} else if !strings.Contains(err.Error(), "duplicate scene id") {
		t.Errorf("want duplicate-scene-id error, got %v", err)
	}

	cancel()
	<-runDone
}
```

- [ ] **Step 4.2: Run; verify it fails**

Run: `go test ./internal/world/ -run "TestSummonNew" -v`
Expected: FAIL — `SummonNew` returns "not yet implemented".

- [ ] **Step 4.3: Implement `apiImpl.SummonNew`**

Edit `internal/world/api.go`. Replace the Task 1 stub:

```go
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
```

Add `"github.com/afternet/go-vibebot/internal/scene"` to the imports.

- [ ] **Step 4.4: Run the tests; verify they pass**

Run: `go test ./internal/world/ -run "TestSummonNew" -v`
Expected: PASS.

Then the full world package: `go test ./internal/world/ -v`
Expected: PASS.

- [ ] **Step 4.5: Commit**

```bash
git add internal/world/api.go internal/world/world_test.go
git commit -m "world: implement apiImpl.SummonNew via send helper"
```

---

## Task 5: Concurrency, cancellation, and append-failure tests

Pin the invariants documented in the spec's error-semantics table and concurrency section.

**Files:**
- Modify: `internal/world/world_test.go`

- [ ] **Step 5.1: Write the concurrent-collision and concurrent-distinct tests**

Append to `internal/world/world_test.go`:

```go
func TestSummonNewConcurrentDistinctPlacesSafe(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	const N = 8
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			_, err := w.API().SummonNew(ctx, api.PlaceID(fmt.Sprintf("p%d", i)), []api.CharacterID{"leader"}, "")
			errs <- err
		}()
	}
	for i := 0; i < N; i++ {
		if err := <-errs; err != nil {
			t.Errorf("call %d: %v", i, err)
		}
	}

	// Distinct places + the boot "scene-1" → N+1 scenes.
	if got, want := len(w.scenes), N+1; got != want {
		t.Errorf("scene count: want %d, got %d", want, got)
	}

	cancel()
	<-runDone
}

func TestSummonNewSamePlaceConcurrentCollision(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	const N = 8
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			_, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader"}, "")
			errs <- err
		}()
	}
	var nilCount, dupCount int
	for i := 0; i < N; i++ {
		err := <-errs
		switch {
		case err == nil:
			nilCount++
		case strings.Contains(err.Error(), "duplicate scene id"):
			dupCount++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if nilCount != 1 {
		t.Errorf("want exactly 1 successful register, got %d", nilCount)
	}
	if dupCount != N-1 {
		t.Errorf("want %d duplicate errors, got %d", N-1, dupCount)
	}

	// Exactly one KindSummon for the place.
	evs, err := st.Query(ctx, store.Filter{SceneID: "place:spire", Kind: store.KindSummon})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Errorf("want exactly 1 KindSummon, got %d", len(evs))
	}

	cancel()
	<-runDone
}
```

- [ ] **Step 5.2: Write the cancellation test**

Append:

```go
func TestSummonNewCtxCancelledBeforeSend(t *testing.T) {
	w, _, _ := newTestWorld(t)

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(runCtx) }()

	// Cancel before the call begins.
	callCtx, callCancel := context.WithCancel(runCtx)
	callCancel()

	_, err := w.API().SummonNew(callCtx, "spire", []api.CharacterID{"leader"}, "")
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	if _, dup := w.scenes["place:spire"]; dup {
		t.Error("scene should not be registered when ctx was cancelled before send")
	}

	runCancel()
	<-runDone
}

func TestSummonNewCtxCancelledRacing(t *testing.T) {
	// Spec test #8 second variant: cancel from a goroutine racing the call.
	// Repeat many times under -race to surface interleavings between the
	// charactersByIDReq send, the RegisterSceneCmd send, and the cancel.
	for i := 0; i < 50; i++ {
		w, _, _ := newTestWorld(t)
		runCtx, runCancel := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() { runDone <- w.Run(runCtx) }()

		callCtx, callCancel := context.WithCancel(runCtx)
		go func() {
			// Cancel after a tiny random-ish delay (a busy loop yields).
			for j := 0; j < i; j++ {
				_ = j
			}
			callCancel()
		}()
		_, err := w.API().SummonNew(callCtx, api.PlaceID(fmt.Sprintf("r%d", i)),
			[]api.CharacterID{"leader"}, "")
		// Either succeeded (cancel came too late) or returned ctx error.
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("iteration %d: unexpected err %v", i, err)
		}

		runCancel()
		<-runDone
	}
}
```

Make sure `"errors"` is in the test file imports.

- [ ] **Step 5.3: Write the append-failure test**

Append:

```go
func TestSummonNewKindSummonAppendFailure(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// Close the store mid-flight so the KindSummon append fails.
	_ = st.Close()

	_, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"leader"}, "")
	if err == nil {
		t.Fatal("expected append failure to surface")
	}

	// Scene stays registered (documented non-atomic state).
	if _, ok := w.scenes["place:spire"]; !ok {
		t.Error("scene should remain registered after summon-append failure")
	}

	cancel()
	<-runDone
}
```

- [ ] **Step 5.4: Run; verify they pass under `-race`**

Run: `go test -race ./internal/world/ -run "TestSummonNewConcurrent|TestSummonNewSamePlaceConcurrentCollision|TestSummonNewCtxCancelledBeforeSend|TestSummonNewCtxCancelledRacing|TestSummonNewKindSummonAppendFailure" -v`
Expected: PASS.

- [ ] **Step 5.5: Commit**

```bash
git add internal/world/world_test.go
git commit -m "world: race + cancellation + append-fail tests for SummonNew"
```

---

## Task 6: Where/Nudge boot-scene asymmetry tests

The spec documents that `Where`/`Nudge` resolve to the boot-time scene even after `SummonNew`. Pin that.

**Files:**
- Modify: `internal/world/world_test.go`

- [ ] **Step 6.1: Write the failing tests**

Append:

```go
func TestWhereAfterSummonNewReturnsBootScene(t *testing.T) {
	w, _, _ := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// m1 starts in boot scene "scene-1".
	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"m1"}, ""); err != nil {
		t.Fatalf("SummonNew: %v", err)
	}

	snap, err := w.API().Where(ctx, "m1")
	if err != nil {
		t.Fatalf("Where: %v", err)
	}
	if snap.SceneID != "scene-1" {
		t.Errorf("Where should resolve to boot scene; want scene-1, got %q", snap.SceneID)
	}

	cancel()
	<-runDone
}

func TestNudgeAfterSummonNewTargetsBootScene(t *testing.T) {
	w, _, st := newTestWorld(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	if _, err := w.API().SummonNew(ctx, "spire", []api.CharacterID{"m1"}, ""); err != nil {
		t.Fatalf("SummonNew: %v", err)
	}
	if err := w.API().Nudge(ctx, "m1"); err != nil {
		t.Fatalf("Nudge: %v", err)
	}

	evs, err := st.Query(ctx, store.Filter{Kind: store.KindNudge})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 nudge event, got %d", len(evs))
	}
	if evs[0].SceneID != "scene-1" {
		t.Errorf("Nudge scene: want scene-1 (boot), got %q", evs[0].SceneID)
	}

	cancel()
	<-runDone
}
```

- [ ] **Step 6.2: Run; verify they pass**

Run: `go test ./internal/world/ -run "TestWhereAfterSummonNew|TestNudgeAfterSummonNew" -v`
Expected: PASS — the behaviour was already implemented by `registerSceneLocked`'s first-write-wins on `charScene`. These tests pin the contract so future refactors can't break it silently.

- [ ] **Step 6.3: Commit**

```bash
git add internal/world/world_test.go
git commit -m "world: pin Where/Nudge boot-scene asymmetry for runtime scenes"
```

---

## Task 7: IRC `!summon` extension

Add `parseSummonArgs`, rewrite `cmdSummon`, and pin all the parser edge cases.

**Files:**
- Modify: `internal/irc/adapter.go`
- Modify: `internal/irc/inject_parse_test.go` (or new `summon_parse_test.go`)
- Modify: `internal/irc/adapter_test.go`

- [ ] **Step 7.1: Write the failing parser tests**

Edit `internal/irc/inject_parse_test.go` (or create `internal/irc/summon_parse_test.go`). Add:

```go
func TestParseSummonArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		wantPlace string
		wantNPCs  []string
		wantDesc  string
		wantErr   bool
	}{
		{"legacy", "cathedral", "cathedral", nil, "", false},
		{"adhoc full", "spire n=vicar,booger-bertha A drafty steeple.", "spire", []string{"vicar", "booger-bertha"}, "A drafty steeple.", false},
		{"adhoc no desc", "spire n=vicar", "spire", []string{"vicar"}, "", false},
		{"empty npc entry", "spire n=vicar,,bertha", "", nil, "", true},
		{"empty npc list", "spire n=", "", nil, "", true},
		{"legacy with trailing", "cathedral some description", "", nil, "", true},
		{"n= after second token", "tavern A dark night n=bertha", "", nil, "", true},
		{"whitespace", "  spire  n=vicar  desc  ", "spire", []string{"vicar"}, "desc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			placeID, npcs, desc, err := parseSummonArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err: want %v, got %v", tt.wantErr, err)
			}
			if tt.wantErr {
				return
			}
			if string(placeID) != tt.wantPlace {
				t.Errorf("placeID: want %q, got %q", tt.wantPlace, placeID)
			}
			gotIDs := make([]string, len(npcs))
			for i, n := range npcs {
				gotIDs[i] = string(n)
			}
			if !equalStringSlices(gotIDs, tt.wantNPCs) {
				t.Errorf("npcs: want %v, got %v", tt.wantNPCs, gotIDs)
			}
			if desc != tt.wantDesc {
				t.Errorf("desc: want %q, got %q", tt.wantDesc, desc)
			}
		})
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 7.2: Run; verify the tests fail**

Run: `go test ./internal/irc/ -run TestParseSummonArgs -v`
Expected: FAIL — `parseSummonArgs` undefined.

- [ ] **Step 7.3: Implement `parseSummonArgs`**

Edit `internal/irc/adapter.go`. Add the function after `parseInjectArgs` (around the same location):

```go
// parseSummonArgs parses the !summon argument string.
//
//   !summon <place-id>                                  → legacy form
//   !summon <place-id> n=<id1>,<id2>,... [description]  → ad-hoc form
//
// The n= flag must be the SECOND whitespace-delimited token. Any other
// position is rejected so a user does not accidentally bury an npc list
// inside a free-form description. Empty npc entries and empty n= lists
// are both errors. Legacy form with trailing text is rejected to avoid
// silently discarding what looked like a scene description.
func parseSummonArgs(args string) (api.PlaceID, []api.CharacterID, string, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", nil, "", errors.New("place id required")
	}
	first, rest, _ := strings.Cut(args, " ")
	placeID := api.PlaceID(strings.TrimSpace(first))
	if placeID == "" {
		return "", nil, "", errors.New("place id required")
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return placeID, nil, "", nil
	}

	second, tail, _ := strings.Cut(rest, " ")
	if !strings.HasPrefix(second, "n=") {
		// Legacy form with trailing text is a hard error.
		if pos := tokenPositionWithPrefix(rest, "n="); pos >= 0 {
			return "", nil, "", fmt.Errorf("n= must be the second token; got it at position %d", pos+2)
		}
		return "", nil, "", errors.New("description without n=...; use !summon <id> n=<id1>,<id2>,... <description> to create a new scene")
	}
	npcList := strings.TrimPrefix(second, "n=")
	if npcList == "" {
		return "", nil, "", errors.New("n= requires at least one character id")
	}
	parts := strings.Split(npcList, ",")
	npcs := make([]api.CharacterID, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return "", nil, "", errors.New("empty npc id in n= list")
		}
		npcs = append(npcs, api.CharacterID(p))
	}
	desc := strings.TrimSpace(tail)
	if strings.Contains(desc, "n=") {
		// Catch a second n= in the description text.
		return "", nil, "", errors.New("multiple n= tokens not supported")
	}
	return placeID, npcs, desc, nil
}
```

If `"errors"` and `"fmt"` aren't imported in `adapter.go`, add them.

Also add the position helper near `parseSummonArgs`:

```go
// tokenPositionWithPrefix returns the 1-based index of the first
// whitespace-delimited token in s that starts with prefix, or -1 if
// none. The caller adds 1 if the place id was already consumed.
func tokenPositionWithPrefix(s, prefix string) int {
	idx := 0
	for _, tok := range strings.Fields(s) {
		if strings.HasPrefix(tok, prefix) {
			return idx
		}
		idx++
	}
	return -1
}
```

- [ ] **Step 7.4: Rewrite `cmdSummon`**

Replace the existing `cmdSummon` in `internal/irc/adapter.go`:

```go
func (a *Adapter) cmdSummon(ctx context.Context, args string, reply func(string)) {
	if args == "" {
		reply("usage: !summon <place-id> [n=id1,id2,...] [description...]")
		return
	}
	placeID, npcs, desc, err := parseSummonArgs(args)
	if err != nil {
		reply("summon: " + err.Error())
		return
	}
	if len(npcs) == 0 {
		if err := a.api.Summon(ctx, placeID); err != nil {
			reply("summon failed: " + err.Error())
			return
		}
		reply("summoned.")
		return
	}
	sceneID, err := a.api.SummonNew(ctx, placeID, npcs, desc)
	if err != nil {
		reply("summon failed: " + err.Error())
		return
	}
	reply("summoned " + string(sceneID) + ".")
}
```

- [ ] **Step 7.5: Run; verify parser tests pass**

Run: `go test ./internal/irc/ -run TestParseSummonArgs -v`
Expected: PASS.

- [ ] **Step 7.6: Write the adapter-routing test**

Append to `internal/irc/adapter_test.go`:

Use the inline adapter-construction pattern established by `TestCmdLogFiltersAmbientByDefault` at `internal/irc/adapter_test.go:33` — there is no `newTestAdapter` helper.

```go
func TestCmdSummonNewRoutesToSummonNew(t *testing.T) {
	fw := &fakeWorld{SummonNewScene: "place:spire"}
	a, err := New(Config{Server: "irc.example", Channel: "#c", Nick: "bot"}, fw)
	if err != nil {
		t.Fatal(err)
	}

	var replies []string
	a.cmdSummon(context.Background(), "spire n=vicar,booger-bertha A drafty steeple.", func(s string) {
		replies = append(replies, s)
	})

	if len(fw.SummonNewCalls) != 1 {
		t.Fatalf("want 1 SummonNew call, got %d", len(fw.SummonNewCalls))
	}
	got := fw.SummonNewCalls[0]
	if got.PlaceID != "spire" || len(got.NPCs) != 2 || got.NPCs[0] != "vicar" || got.NPCs[1] != "booger-bertha" {
		t.Errorf("unexpected SummonNew args: %+v", got)
	}
	if got.Description != "A drafty steeple." {
		t.Errorf("description: want %q, got %q", "A drafty steeple.", got.Description)
	}
	if len(replies) != 1 || !strings.Contains(replies[0], "place:spire") {
		t.Errorf("reply should contain scene id, got %v", replies)
	}
}

func TestCmdSummonLegacyStillRoutesToSummon(t *testing.T) {
	fw := &fakeWorld{}
	a, err := New(Config{Server: "irc.example", Channel: "#c", Nick: "bot"}, fw)
	if err != nil {
		t.Fatal(err)
	}
	a.cmdSummon(context.Background(), "cathedral", func(string) {})

	if len(fw.SummonCalls) != 1 || fw.SummonCalls[0].PlaceID != "cathedral" {
		t.Errorf("legacy path should call Summon(cathedral); got %+v", fw.SummonCalls)
	}
	if len(fw.SummonNewCalls) != 0 {
		t.Errorf("legacy path must not call SummonNew, got %d", len(fw.SummonNewCalls))
	}
}
```

- [ ] **Step 7.7: Run; verify tests pass**

Run: `go test ./internal/irc/ -v`
Expected: PASS.

- [ ] **Step 7.8: Commit**

```bash
git add internal/irc/adapter.go internal/irc/inject_parse_test.go internal/irc/adapter_test.go
git commit -m "irc: !summon n=...; route to WorldAPI.SummonNew"
```

(`internal/irc/fake_world_test.go` was already committed at Task 1.6 with the `SummonNewCall` recording shape. If you also added `SummonCalls` recording to it during Step 7.6 to harden the legacy-path assertion, include it here — otherwise omit.)

---

## Task 8: MCP `summon` tool extension

Update `SummonInput`, add `SceneID` to `SummonOutput`, and rewrite the handler.

**Files:**
- Modify: `internal/mcp/tools.go`
- Modify: `internal/mcp/tools_test.go` and/or `internal/mcp/adapter_test.go`

- [ ] **Step 8.1: Write the failing tests**

Append to `internal/mcp/adapter_test.go`. Use the existing `runAdapter` helper (defined at `internal/mcp/adapter_test.go:38`) plus an in-memory transport pair — this is the same pattern as `TestE2EInjectViaInMemoryTransport` and `TestE2ESummonUnknownPlaceReturnsToolError`:

```go
func TestE2ESummonNewAdHoc(t *testing.T) {
	fw := &fakeWorld{SummonNewScene: "place:spire"}
	adapter, err := New(Config{}, fw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	serverT, clientT := mcpsdk.NewInMemoryTransports()
	ctx, stop := runAdapter(t, adapter, serverT)
	t.Cleanup(stop)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "summon",
		Arguments: map[string]any{
			"place_id":    "spire",
			"npcs":        []any{"vicar", "booger-bertha"},
			"description": "A drafty steeple.",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got IsError; content=%v", res.Content)
	}
	if len(fw.SummonNewCalls) != 1 {
		t.Fatalf("want 1 SummonNew call, got %d", len(fw.SummonNewCalls))
	}
	got := fw.SummonNewCalls[0]
	if got.PlaceID != "spire" || got.Description != "A drafty steeple." || len(got.NPCs) != 2 {
		t.Errorf("unexpected SummonNew args: %+v", got)
	}
	if len(fw.SummonCalls) != 0 {
		t.Errorf("legacy Summon must not be called for ad-hoc path")
	}
}

func TestE2ESummonLegacyStillWorks(t *testing.T) {
	fw := &fakeWorld{}
	adapter, err := New(Config{}, fw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	serverT, clientT := mcpsdk.NewInMemoryTransports()
	ctx, stop := runAdapter(t, adapter, serverT)
	t.Cleanup(stop)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name:      "summon",
		Arguments: map[string]any{"place_id": "cathedral"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected success, got %v", res.Content)
	}
	if len(fw.SummonCalls) != 1 {
		t.Errorf("want 1 legacy Summon, got %d", len(fw.SummonCalls))
	}
	if len(fw.SummonNewCalls) != 0 {
		t.Errorf("legacy path must not call SummonNew")
	}
}

func TestE2ESummonNewErrorSurfacesAsToolError(t *testing.T) {
	fw := &fakeWorld{SummonNewErr: errors.New(`unknown character "ghost"`)}
	adapter, err := New(Config{}, fw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	serverT, clientT := mcpsdk.NewInMemoryTransports()
	ctx, stop := runAdapter(t, adapter, serverT)
	t.Cleanup(stop)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: "summon",
		Arguments: map[string]any{
			"place_id": "spire",
			"npcs":     []any{"ghost"},
		},
	})
	if err != nil {
		t.Fatalf("CallTool returned protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true on SummonNew failure")
	}
}
```

- [ ] **Step 8.2: Run; verify failure**

Run: `go test ./internal/mcp/ -run "TestE2ESummonNewAdHoc|TestE2ESummonLegacyStillWorks|TestE2ESummonNewErrorSurfacesAsToolError" -v`
Expected: FAIL — fields don't exist on `SummonInput`/`SummonOutput`.

- [ ] **Step 8.3: Extend `SummonInput` and `SummonOutput`**

Edit `internal/mcp/tools.go`. Replace the existing `SummonInput`/`SummonOutput`:

```go
type SummonInput struct {
	PlaceID     string   `json:"place_id" jsonschema:"the place id to summon"`
	NPCs        []string `json:"npcs,omitempty" jsonschema:"optional list of character ids (from Characters()) for an ad-hoc place; first id is the leader; omit entirely to summon a pre-configured place from seed/places/, do not pass an empty array"`
	Description string   `json:"description,omitempty" jsonschema:"optional scene-setting text (recorded as an inject after summon); only meaningful when npcs is provided"`
}

type SummonOutput struct {
	OK      bool   `json:"ok"`
	SceneID string `json:"scene_id"`
	Message string `json:"message"`
}
```

- [ ] **Step 8.4: Rewrite the handler**

Replace `summonHandler` in `internal/mcp/tools.go`:

```go
func (a *Adapter) summonHandler(
	ctx context.Context,
	_ *mcpsdk.CallToolRequest,
	in SummonInput,
) (*mcpsdk.CallToolResult, SummonOutput, error) {
	if in.PlaceID == "" {
		return toolError("summon: place_id is required"), SummonOutput{}, nil
	}
	if len(in.NPCs) == 0 {
		if err := a.api.Summon(ctx, api.PlaceID(in.PlaceID)); err != nil {
			return toolError(fmt.Sprintf("summon failed: %s", err.Error())), SummonOutput{}, nil
		}
		a.logger.Info("mcp summon", "place", in.PlaceID)
		return nil, SummonOutput{
			OK:      true,
			SceneID: "place:" + in.PlaceID,
			Message: "summoned.",
		}, nil
	}
	npcs := make([]api.CharacterID, len(in.NPCs))
	for i, s := range in.NPCs {
		npcs[i] = api.CharacterID(s)
	}
	sceneID, err := a.api.SummonNew(ctx, api.PlaceID(in.PlaceID), npcs, in.Description)
	if err != nil {
		return toolError(fmt.Sprintf("summon failed: %s", err.Error())), SummonOutput{}, nil
	}
	a.logger.Info("mcp summon (new)", "place", in.PlaceID, "npcs", len(npcs))
	return nil, SummonOutput{
		OK:      true,
		SceneID: string(sceneID),
		Message: "summoned.",
	}, nil
}
```

- [ ] **Step 8.5: Run; verify tests pass**

Run: `go test ./internal/mcp/ -v`
Expected: PASS.

- [ ] **Step 8.6: Commit**

```bash
git add internal/mcp/tools.go internal/mcp/adapter_test.go internal/mcp/fake_world_test.go
git commit -m "mcp: summon tool accepts npcs[] + description; returns scene_id"
```

---

## Task 9: cmd/sim smoke test

End-to-end IRC → world → event log test, mirroring `TestSummonCathedralInjectAndSpeak`.

**Files:**
- Modify: `cmd/sim/smoke_test.go`
- Modify (deviation, see below): `seed/places/eton-on-thames.yaml`

> **Execution deviation (2026-05-14):** the verbatim test code below calls `SummonNew(ctx, "spire", []api.CharacterID{"vicar", "booger-bertha"}, ...)`. `booger-bertha` was only a member of `groups[1]` (the-gang) at plan-authoring time, and this test only boot-registers `groups[0]` (stinky-lads) plus the four `seed/places/*.yaml` scenes — none of which contained her. Result: `SummonNew` returned `unknown character(s): booger-bertha`. Resolved during execution by adding `booger-bertha` to `seed/places/eton-on-thames.yaml` (commit `0cd1a4c`), which is the minimum boot-data change to make the plan-exact test pass. Alternative would have been to swap the test's NPC list — also a plan deviation. Flagged here so a future reader sees both the original intent and the actual landing.

- [ ] **Step 9.1: Write the failing test**

Append to `cmd/sim/smoke_test.go`. Clone the structure of `TestSummonCathedralInjectAndSpeak` and swap the IRC line for the ad-hoc summon:

```go
func TestRuntimeAdHocPlaceSummonViaIRC(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	seedDir := filepath.Join("..", "..", "seed")
	chars, err := config.LoadCharacters(filepath.Join(seedDir, "characters.yaml"))
	if err != nil {
		t.Fatalf("load characters: %v", err)
	}
	groups, err := config.LoadGroups(filepath.Join(seedDir, "groups.yaml"))
	if err != nil {
		t.Fatalf("load groups: %v", err)
	}
	places, err := config.LoadPlaces(filepath.Join(seedDir, "places"))
	if err != nil {
		t.Fatalf("load places: %v", err)
	}
	if verr := config.Validate(chars, groups, places); verr != nil {
		t.Fatalf("validate: %v", verr)
	}

	llmImpl := echoLLM{}

	byID := make(map[api.CharacterID]*character.Character, len(chars))
	for _, spec := range chars {
		id := api.CharacterID(spec.ID)
		byID[id] = &character.Character{
			ID: id, Name: spec.Name, Persona: spec.Persona,
			Capabilities: spec.Capabilities, Blurb: spec.Blurb,
			Memory: memory.NewInMem(50),
			Inbox:  make(chan character.Perception, 8),
		}
	}

	g := groups[0]
	sc := &scene.Scene{
		ID:     api.SceneID(g.ID),
		Router: scene.LLMRouter{Model: llmImpl, PreFilterK: 0, MaxConsult: 0},
	}
	for _, mid := range g.Members {
		sc.Members = append(sc.Members, byID[api.CharacterID(mid)])
	}
	sc.Leader = byID[api.CharacterID(g.Leader)]

	w := world.New(world.Config{TickInterval: time.Hour}, st, llmImpl)
	w.RegisterScene(sc)

	// Pre-register a place (so the world has more than one scene at boot,
	// matching the production wiring).
	for _, p := range places {
		ps := &scene.Scene{
			ID: api.SceneID("place:" + p.ID), PlaceID: api.PlaceID(p.ID),
			Router: scene.LLMRouter{Model: llmImpl, PreFilterK: 0, MaxConsult: 0},
		}
		for _, nid := range p.NPCs {
			ps.Members = append(ps.Members, byID[api.CharacterID(nid)])
		}
		ps.Leader = ps.Members[0]
		w.RegisterScene(ps)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- w.Run(ctx) }()

	// Drive an IRC `!summon` line through the WorldAPI (the IRC adapter
	// itself is exercised in internal/irc; this test focuses on the
	// runtime registration + scene activation path).
	npcs := []api.CharacterID{"vicar", "booger-bertha"}
	sceneID, err := w.API().SummonNew(ctx, "spire", npcs, "A drafty steeple.")
	if err != nil {
		t.Fatalf("SummonNew: %v", err)
	}
	if sceneID != "place:spire" {
		t.Fatalf("scene id: want place:spire, got %q", sceneID)
	}

	// Give the orchestrate loop a moment to fan out.
	time.Sleep(250 * time.Millisecond)

	entries, err := w.API().Log(ctx, time.Hour)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	var sawSummon, sawInject, sawSpeech, sawSynth bool
	for _, e := range entries {
		if e.SceneID == api.SceneID(g.ID) && e.Kind != string(store.KindSceneEnter) {
			t.Errorf("gang scene saw unexpected event: %+v", e)
		}
		if e.SceneID != sceneID {
			continue
		}
		switch e.Kind {
		case string(store.KindSummon):
			sawSummon = true
		case string(store.KindInject):
			sawInject = true
		case string(store.KindSpeech):
			sawSpeech = true
		case string(store.KindSynthesized):
			sawSynth = true
			if e.Actor != string(npcs[0]) {
				t.Errorf("synthesized actor: want %s, got %s", npcs[0], e.Actor)
			}
		}
	}
	if !sawSummon {
		t.Error("no KindSummon on place:spire")
	}
	if !sawInject {
		t.Error("no KindInject on place:spire (description should have fired)")
	}
	if !sawSpeech {
		t.Error("no KindSpeech on place:spire (orchestrate did not fan out)")
	}
	if !sawSynth {
		t.Error("no KindSynthesized on place:spire (leader did not synthesize)")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("world.Run did not return after cancel")
	}
}
```

- [ ] **Step 9.2: Run; verify it passes**

Run: `go test ./cmd/sim/ -run TestRuntimeAdHocPlaceSummonViaIRC -v`
Expected: PASS. Failure-mode hints:

- "no KindSpeech" → check that `scene.LLMRouter` is constructed identically to the boot path; `PreFilterK: 0, MaxConsult: 0` means fan-out-to-all.
- "no KindInject" → confirm Task 4's `SummonNew` chains `InjectEvent` after `Summon` when description is non-empty.
- "no KindSynthesized" → `vicar` (the first npc) must already be a registered character. Confirm `seed/characters.yaml` still lists `vicar`.

- [ ] **Step 9.3: Run the entire test suite under `-race`**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 9.4: Commit**

```bash
git add cmd/sim/smoke_test.go
git commit -m "sim: smoke test for runtime ad-hoc place summon"
```

---

## Task 10: Documentation

**Files:**
- Modify: `README.md`
- Modify: `BACKLOG.md`

> **Execution deviation (2026-05-14):** Step 10.1 below says "replace the `!summon` line" in the README's IRC commands section, assuming entries existed for `!summon`, `!nudge`, and `!snapshot`. At execution time the section only contained `!inject` and `!log` — earlier work had shipped the other three commands without README updates. Resolved (commit `7f021e2`) by expanding the section to include accurate one-line entries for the missing commands alongside the verbatim two-line `!summon` block from this plan. The plan-quoted text was preserved exactly; only the surrounding context was filled in.

- [ ] **Step 10.1: Update README IRC commands section**

Find the IRC commands list in `README.md` (it lists `!inject`, `!summon`, `!nudge`, `!log`, `!snapshot`). Replace the `!summon` line with:

```
- `!summon <place-id>` — summon a pre-loaded place (`seed/places/*.yaml`).
- `!summon <place-id> n=<id1>,<id2>,... <description>` — register a new ad-hoc place-scene at runtime using existing characters. The first id is the scene leader. Description is recorded as an inject scoped to the new scene. Ad-hoc places live only for the binary's lifetime.
```

- [ ] **Step 10.2: Mark the BACKLOG follow-up shipped**

Edit `BACKLOG.md`. Find the deferred-follow-up under L2 that reads:

```
- **Runtime scene registration** — `World.RegisterScene` panics after `Run` starts. Lifting this requires coordinator-goroutine-owned scene creation so a place can be summoned without being pre-loaded.
```

Replace it with the strikethrough form matching the L1/L2/L3/S1 precedents:

```
- ~~**Runtime scene registration**~~ — SHIPPED 2026-05-14. Plan at `docs/superpowers/plans/2026-05-14-runtime-scene-registration.md`; spec at `docs/superpowers/specs/2026-05-14-runtime-scene-registration-design.md`. `WorldAPI.SummonNew(placeID, npcs, description)` registers an ad-hoc place-scene at runtime via the existing coordinator goroutine. IRC `!summon <id> n=...` and the MCP `summon` tool both surface it.
```

Preserve the other deferred follow-ups under L2 in place.

- [ ] **Step 10.3: Commit**

```bash
git add README.md BACKLOG.md
git commit -m "docs: !summon n=...; mark BACKLOG runtime-scene-registration shipped"
```

---

## Post-flight checklist

- [ ] **Step 11.1: Full test suite**

Run: `go test -race ./...`
Expected: PASS.

- [ ] **Step 11.2: Build the binary**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 11.3: Manual smoke (optional)**

If `vibebot.yaml` is configured with IRC, run the binary, join the channel, and try:

```
!summon spire n=vicar,booger-bertha A drafty steeple.
```

Expected reply: `summoned place:spire.`

Then:

```
!inject @place:spire The Vicar mutters about the wind.
```

Expected: NPCs in the cathedral participate in the new scene.

- [ ] **Step 11.4: Verify all 24 spec tests are covered**

Spec test → plan location mapping:

| Spec # | Spec name | Plan location |
|---|---|---|
| 1 | TestRegisterSceneAfterRunDoesNotPanic | Task 2.1 |
| 2 | TestSummonNewUnknownCharacterErrors | Task 4.1 (full API path) + Task 2.1 `TestRegisterSceneLockedRejectsUnknownMember` (helper-level) |
| 3 | TestSummonNewDuplicatePlaceErrors | Task 4.1 |
| 4 | TestSummonNewWithDescriptionWritesInject | Task 4.1 |
| 5 | TestSummonNewWithoutDescriptionEmitsOnlySummon | Task 4.1 |
| 6 | TestSummonNewConcurrentDistinctPlacesSafe | Task 5.1 |
| 7 | TestSummonNewSamePlaceConcurrentCollision | Task 5.1 |
| 8 | TestSummonNewCtxCancelledDuringRoundTrip | Task 5.2 (`TestSummonNewCtxCancelledBeforeSend` + `TestSummonNewCtxCancelledRacing`) |
| 9 | TestSummonNewKindSummonAppendFailure | Task 5.3 |
| 10 | TestWhereAfterSummonNewReturnsBootScene | Task 6.1 |
| 11 | TestNudgeAfterSummonNewTargetsBootScene | Task 6.1 |
| 12–19 | TestParseSummon* (eight cases) | Task 7.1 — consolidated as a single table-driven `TestParseSummonArgs` with eight sub-tests; sub-test names match the spec ("legacy", "adhoc full", etc.) |
| 20 | TestCmdSummonNewRoutesToSummonNew | Task 7.6 |
| 21 | TestE2ESummonNewAdHoc | Task 8.1 |
| 22 | TestE2ESummonLegacyStillWorks | Task 8.1 |
| 23 | TestE2ESummonNewErrorSurfacesAsToolError | Task 8.1 |
| 24 | TestRuntimeAdHocPlaceSummonViaIRC | Task 9.1 |

To run a single sub-test from the consolidated parser table: `go test ./internal/irc/ -run 'TestParseSummonArgs/adhoc_full' -v` (Go converts the table-name's spaces to underscores).

If any row is missing or its location is wrong, return to that task and add the test before considering this plan complete.

---

## Post-execution log (2026-05-14)

The plan was executed task-by-task via subagent-driven development on 2026-05-14. Commits `bb5d722..7f021e2` on `main` correspond to Tasks 1 through 10. Two intentional deviations from the plan-as-authored landed during execution; both are flagged inline at their respective tasks and summarized here for quick reference.

| Deviation | Location | Commit | Reason |
|---|---|---|---|
| Added `booger-bertha` to `seed/places/eton-on-thames.yaml` | Task 9 (plan listed only `cmd/sim/smoke_test.go` as modified) | `0cd1a4c` | Plan-exact test code referenced an NPC that wasn't boot-registered. Minimum seed-data fix preserves the plan's test code verbatim. |
| Expanded README IRC commands section instead of doing a single-line replace | Task 10 | `7f021e2` | The pre-existing section was already out of date — only `!inject` and `!log` were listed. The plan-quoted `!summon` two-line block landed verbatim; the other commands' one-liners were added accurately. |

Additionally, a follow-up commit `cee5140` ("memory: guard InMem and Embedded for concurrent Record/Retrieve") fixed a pre-existing data race in `internal/memory.(*InMem)` and `internal/memory.(*Embedded)` that this plan inherited from commit `76ee691` ("scene: leader synthesis pulls from leader's own memory"). The race was out of scope for this plan but had to be fixed before `go test -race ./...` could pass cleanly — which Step 11.1 of the post-flight checklist required.
