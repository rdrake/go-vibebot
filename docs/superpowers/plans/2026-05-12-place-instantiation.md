# Place Instantiation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `seed/places/*.yaml` into live scenes at boot, so `!summon cathedral` confirms a real cathedral scene and an inject scoped to it makes a vicar/caretaker/cathedral-cat speak.

**Architecture:** Pre-register one place-scene per yaml at boot, alongside the group scene. NPCs are ordinary `*character.Character` instances loaded from `seed/characters.yaml` as orphan characters; place yaml lists them by id. `WorldAPI.InjectEvent` gains an explicit `sceneID` argument so the IRC adapter and tests can target the cathedral instead of the default (gang) scene. `RegisterScene` records insertion order so the default scene is the *first* registered one rather than a non-deterministic map pick.

**Tech Stack:** Go 1.24, `gopkg.in/yaml.v3`, `slog`. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-12-place-instantiation-design.md`

---

## File Structure

| Path | Status | Responsibility |
|---|---|---|
| `seed/characters.yaml` | modify | Add vicar/caretaker/cathedral-cat orphan-character specs |
| `seed/places/cathedral.yaml` | modify | Add comment documenting "first npc is leader" |
| `internal/config/config.go` | modify | Add `LoadPlaces(dir string) ([]PlaceSpec, error)` directory walker |
| `internal/config/validate.go` | modify | Accept `places []PlaceSpec`; validate place ids non-empty/unique and every NPC referenced is a known character |
| `internal/config/validate_test.go` | modify | Tests for new validation rules |
| `internal/api/api.go` | modify | `InjectEvent` signature gains a leading `sceneID SceneID` parameter |
| `internal/world/messages.go` | modify | `Inject` command struct gains `SceneID` field |
| `internal/world/world.go` | modify | `sceneOrder []SceneID`; `RegisterScene` appends; `defaultScene` uses insertion order; `dispatchInject` and `dispatchSummon` route by `place:<placeID>` scene id; unknown id returns error |
| `internal/world/api.go` | modify | Propagate `sceneID` through `Inject{...}` |
| `internal/world/world_test.go` | modify | Update every `InjectEvent` callsite; add insertion-order test; add scene-targeted-inject test; add summon-unknown-place test |
| `internal/irc/adapter.go` | modify | Parse `!inject @<scene-id> <desc>` form; update `usage:` strings |
| `cmd/sim/main.go` | modify | Load places via `LoadPlaces`; build place-scenes; register them before `w.Run` |
| `cmd/sim/smoke_test.go` | modify | Update `TestSmokeEndToEnd` for the new signature; add `TestSummonCathedralInjectAndSpeak` |

---

## Pre-flight verification

- [ ] **Step 0.1: Confirm no caller of `config.LoadPlace` exists**

Run:
```bash
grep -rn "LoadPlace\b\|LoadPlaces\b" --include='*.go' .
```

Expected: hits only inside `internal/config/config.go` (declaration). If anything else shows up, read it before touching the plan — someone has started this work.

- [ ] **Step 0.2: Confirm `config.Validate` does not require characters to belong to a group**

Read `internal/config/validate.go`. Verify the loops only check that group-referenced ids exist in the character set, never the reverse. Adding orphan characters in Task 1 depends on this property.

- [ ] **Step 0.3: Confirm `World.scenes` is a bare map and `defaultScene()` iterates it**

Read `internal/world/world.go:240-246`. Expected: `defaultScene` does `for _, s := range w.scenes { return s }`. This is the non-deterministic pick that Task 5 fixes.

- [ ] **Step 0.4: Confirm `WorldAPI.InjectEvent` callsites**

Run:
```bash
grep -rn "InjectEvent(" --include='*.go' .
```

Expected callsites that Task 4 must update:
- `internal/api/api.go` (interface decl)
- `internal/world/api.go` (impl)
- `internal/irc/adapter.go:241` (cmdInject)
- `cmd/sim/smoke_test.go:90`
- `internal/world/world_test.go` (multiple)

Note the exact line numbers. The plan's signature change must update each, not just the first.

---

## Task 1: NPC character seeds

**Files:**
- Modify: `seed/characters.yaml`
- Modify: `seed/places/cathedral.yaml`

This task is data-only. No code, no test — the new specs are consumed by Task 7's smoke test, which is where their wiring is exercised.

- [ ] **Step 1.1: Append three orphan NPC specs to `seed/characters.yaml`**

Append after the existing `booger-bertha` entry, preserving the file's two-space indent style:

```yaml

  - id: vicar
    name: The Vicar
    persona: |
      An elderly clergyman who polishes a candelabrum and worries about
      the draft. Speaks in measured, slightly apologetic sentences and
      tends to find a moral in everything. Reads the room before
      offering a verdict.
    capabilities: [diplomacy, ritual, observation]
    blurb: Tends the cathedral. Worried about the draft.

  - id: caretaker
    name: The Caretaker
    persona: |
      A wiry, taciturn caretaker who mutters at a broom. Knows where
      every loose flagstone is. Distrusts visitors and any noise above
      a whisper.
    capabilities: [maintenance, suspicion, local-knowledge]
    blurb: Mutters at a broom. Sees everything, says little.

  - id: cathedral-cat
    name: The Cathedral Cat
    persona: |
      A black cat. Watches everything without comment. Responds to
      direct address with a slow blink. Occasionally knocks a candle
      over for emphasis.
    capabilities: [observation, gravity, silence]
    blurb: Watches everything. Comments rarely.
```

- [ ] **Step 1.2: Add leader-convention comment to `seed/places/cathedral.yaml`**

Edit `seed/places/cathedral.yaml`. Replace the `npcs:` block to read:

```yaml
# The first npc in the list is the scene leader (synthesizes the round).
npcs:
  - vicar
  - caretaker
  - cathedral-cat
```

- [ ] **Step 1.3: Commit**

```bash
git add seed/characters.yaml seed/places/cathedral.yaml
git commit -m "seed: add cathedral NPC characters and leader convention"
```

---

## Task 2: `config.LoadPlaces` directory walker

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/places_test.go` (new)

- [ ] **Step 2.1: Write the failing tests**

Create `internal/config/places_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlacesReadsEveryYAML(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.yaml", "id: a\nname: A\ndescription: first\nnpcs: [x]\n")
	write("b.yaml", "id: b\nname: B\ndescription: second\nnpcs: [y, z]\n")
	// non-yaml file should be ignored
	write("README.txt", "not yaml")

	places, err := LoadPlaces(dir)
	if err != nil {
		t.Fatalf("LoadPlaces: %v", err)
	}
	if len(places) != 2 {
		t.Fatalf("want 2 places, got %d", len(places))
	}
	// Sorted by id for deterministic test/iteration order.
	if places[0].ID != "a" || places[1].ID != "b" {
		t.Fatalf("want sorted [a, b], got [%s, %s]", places[0].ID, places[1].ID)
	}
	if len(places[1].NPCs) != 2 || places[1].NPCs[0] != "y" {
		t.Fatalf("npc list not parsed: %+v", places[1].NPCs)
	}
}

func TestLoadPlacesMissingDirReturnsEmpty(t *testing.T) {
	places, err := LoadPlaces(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing dir should not error, got %v", err)
	}
	if len(places) != 0 {
		t.Fatalf("want empty, got %d", len(places))
	}
}

func TestLoadPlacesPropagatesParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte("not: : valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlaces(dir); err == nil {
		t.Fatal("expected parse error")
	}
}
```

- [ ] **Step 2.2: Run the tests; verify they fail**

Run: `go test ./internal/config/ -run LoadPlaces -v`
Expected: FAIL with `undefined: LoadPlaces`.

- [ ] **Step 2.3: Implement `LoadPlaces`**

Append to `internal/config/config.go`:

```go
// LoadPlaces reads every .yaml/.yml file in dir as a PlaceSpec and returns
// them sorted by ID. A missing directory returns an empty slice (not an
// error); other I/O failures are surfaced.
func LoadPlaces(dir string) ([]PlaceSpec, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read places dir %s: %w", dir, err)
	}
	var places []PlaceSpec
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		p, err := LoadPlace(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		places = append(places, p)
	}
	sort.Slice(places, func(i, j int) bool { return places[i].ID < places[j].ID })
	return places, nil
}
```

Add the imports at the top of the file:

```go
import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)
```

(Existing imports are `fmt`, `os`, `gopkg.in/yaml.v3` — preserve them; add the four new ones.)

- [ ] **Step 2.4: Run the tests; verify they pass**

Run: `go test ./internal/config/ -run LoadPlaces -v`
Expected: PASS (3 tests).

- [ ] **Step 2.5: Commit**

```bash
git add internal/config/config.go internal/config/places_test.go
git commit -m "config: add LoadPlaces directory walker"
```

---

## Task 3: Validate places

**Files:**
- Modify: `internal/config/validate.go`
- Modify: `internal/config/validate_test.go`

- [ ] **Step 3.1: Update existing tests for the new signature, then add new failing tests**

Edit `internal/config/validate_test.go`. Update every existing `Validate(...)` call to add a third arg `nil` (empty places). Then append the new failing tests:

```go
func TestValidatePlaceNPCsMustExist(t *testing.T) {
	err := Validate(
		[]CharacterSpec{{ID: "a"}, {ID: "b"}},
		[]GroupSpec{{ID: "g", Leader: "a", Members: []string{"a"}}},
		[]PlaceSpec{
			{ID: "cathedral", NPCs: []string{"a", "ghost"}},
		},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), `place "cathedral" npc "ghost" not in characters`) {
		t.Fatalf("missing npc-not-in-characters error: %v", err)
	}
}

func TestValidateRejectsDuplicateAndEmptyPlaceIDs(t *testing.T) {
	err := Validate(
		nil,
		nil,
		[]PlaceSpec{
			{ID: ""},
			{ID: "x"},
			{ID: "x"},
		},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		"place with empty id",
		`duplicate place id "x"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in error: %s", want, msg)
		}
	}
}

func TestValidatePlaceWithNoNPCsWarnsButNotError(t *testing.T) {
	// Empty NPC list is allowed by Validate (a "place without people" is
	// data the caller may filter out later). It must not error here.
	err := Validate(
		[]CharacterSpec{{ID: "a"}},
		[]GroupSpec{{ID: "g", Leader: "a", Members: []string{"a"}}},
		[]PlaceSpec{{ID: "empty"}},
	)
	if err != nil {
		t.Fatalf("place with empty NPCs should not error, got %v", err)
	}
}
```

- [ ] **Step 3.2: Run; verify the new tests fail (and the old ones still compile)**

Run: `go test ./internal/config/ -run Validate -v`
Expected: COMPILE ERROR — `Validate` takes 2 args. Tests cannot run yet.

- [ ] **Step 3.3: Update `Validate` signature and implementation**

Edit `internal/config/validate.go`. Replace the function with:

```go
// Validate cross-checks character/group/place references and returns a
// joined error listing every problem found. Run after Load*; flagging all
// issues at parse-time beats a chain of one-at-a-time wiring failures.
func Validate(chars []CharacterSpec, groups []GroupSpec, places []PlaceSpec) error {
	known := make(map[string]struct{}, len(chars))
	var problems []string

	for _, c := range chars {
		if c.ID == "" {
			problems = append(problems, "character with empty id")
			continue
		}
		if _, dup := known[c.ID]; dup {
			problems = append(problems, fmt.Sprintf("duplicate character id %q", c.ID))
			continue
		}
		known[c.ID] = struct{}{}
	}

	for _, g := range groups {
		if g.ID == "" {
			problems = append(problems, "group with empty id")
		}
		if g.Leader == "" {
			problems = append(problems, fmt.Sprintf("group %q has no leader", g.ID))
		} else if _, ok := known[g.Leader]; !ok {
			problems = append(problems, fmt.Sprintf("group %q leader %q not in characters", g.ID, g.Leader))
		}
		leaderInMembers := false
		members := make(map[string]struct{}, len(g.Members))
		for _, m := range g.Members {
			if _, dup := members[m]; dup {
				problems = append(problems, fmt.Sprintf("group %q duplicate member %q", g.ID, m))
				continue
			}
			members[m] = struct{}{}
			if _, ok := known[m]; !ok {
				problems = append(problems, fmt.Sprintf("group %q member %q not in characters", g.ID, m))
			}
			if m == g.Leader {
				leaderInMembers = true
			}
		}
		if g.Leader != "" && !leaderInMembers {
			problems = append(problems, fmt.Sprintf("group %q leader %q not in members", g.ID, g.Leader))
		}
	}

	placeIDs := make(map[string]struct{}, len(places))
	for _, p := range places {
		if p.ID == "" {
			problems = append(problems, "place with empty id")
			continue
		}
		if _, dup := placeIDs[p.ID]; dup {
			problems = append(problems, fmt.Sprintf("duplicate place id %q", p.ID))
			continue
		}
		placeIDs[p.ID] = struct{}{}
		for _, n := range p.NPCs {
			if _, ok := known[n]; !ok {
				problems = append(problems, fmt.Sprintf("place %q npc %q not in characters", p.ID, n))
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New("config: " + strings.Join(problems, "; "))
}
```

- [ ] **Step 3.4: Run the tests; verify all pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (all existing tests + 3 new validate tests).

- [ ] **Step 3.5: Update the only other caller of `Validate` so the package builds**

Edit `cmd/sim/main.go`. Locate the `config.Validate(chars, groups)` call (around line 108) and change to:

```go
	places, err := config.LoadPlaces(filepath.Join(seedDir, "places"))
	if err != nil {
		return err
	}
	if err := config.Validate(chars, groups, places); err != nil {
		return err
	}
```

(The `places` slice is needed for Task 7; loading it here is the natural spot.)

- [ ] **Step 3.6: Run the full package build to confirm nothing else calls the old signature**

Run: `go build ./...`
Expected: success. If a callsite is missed, the compiler will name it; update that callsite to pass `nil` for places.

- [ ] **Step 3.7: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go cmd/sim/main.go
git commit -m "config: validate places against character set"
```

---

## Task 4: Inject takes an explicit SceneID

**Files:**
- Modify: `internal/api/api.go`
- Modify: `internal/world/messages.go`
- Modify: `internal/world/api.go`
- Modify: `internal/world/world.go`
- Modify: `internal/world/world_test.go`
- Modify: `internal/irc/adapter.go`
- Modify: `cmd/sim/smoke_test.go`

This task is mostly a refactor with one new behavior: an unknown scene id returns an error. The new behavior is the test we write first.

- [ ] **Step 4.1: Write the failing test for unknown-scene-id**

Append to `internal/world/world_test.go`:

```go
func TestInjectUnknownSceneIDErrors(t *testing.T) {
	w, _, _ := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	err := w.API().InjectEvent(ctx, "ghost-scene", "", "anything")
	if err == nil {
		t.Fatal("expected error for unknown scene id")
	}
	if !strings.Contains(err.Error(), "scene") {
		t.Fatalf("expected error to mention scene, got %v", err)
	}
}
```

- [ ] **Step 4.2: Run; expect compile error (signature is still 3-arg)**

Run: `go test ./internal/world/ -run InjectUnknown -v`
Expected: COMPILE ERROR — `InjectEvent` takes 2 args after ctx.

- [ ] **Step 4.3: Update the interface signature**

Edit `internal/api/api.go`. Replace the `InjectEvent` line in the `WorldAPI` interface with:

```go
	InjectEvent(ctx context.Context, sceneID SceneID, target, description string) error
```

- [ ] **Step 4.4: Extend the `Inject` command struct**

Edit `internal/world/messages.go`. Replace the `Inject` struct with:

```go
// Inject is an IRC `!inject` style scenario push targeted at one scene.
// SceneID may be empty: empty resolves to the default scene (the first
// scene registered). Target is free-form (caller-provided noun, often "").
type Inject struct {
	SceneID     api.SceneID
	Target      string
	Description string
	Reply       chan<- error
}
```

- [ ] **Step 4.5: Wire `sceneID` through the API impl**

Edit `internal/world/api.go`. Replace `InjectEvent`:

```go
func (a apiImpl) InjectEvent(ctx context.Context, sceneID api.SceneID, target, description string) error {
	return a.send(ctx, func(r chan<- error) Command {
		return Inject{SceneID: sceneID, Target: target, Description: description, Reply: r}
	})
}
```

- [ ] **Step 4.6: Route by scene id in the dispatcher**

Edit `internal/world/world.go`. In `handleCommand`, the `Inject` arm currently calls `w.dispatchInject(ctx, c.Target, c.Description)`. Change it to:

```go
		case Inject:
			c.Reply <- w.dispatchInject(ctx, c.SceneID, c.Target, c.Description)
```

Replace `dispatchInject` with:

```go
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
```

- [ ] **Step 4.7: Update every `InjectEvent` callsite**

Update each callsite to insert an empty `SceneID` as the first new arg. The callsites flagged in Step 0.4:

In `internal/irc/adapter.go:241`, `cmdInject` currently calls:

```go
if err := a.api.InjectEvent(ctx, "", args); err != nil {
```

Change to (the `@<scene-id>` parsing arrives in Task 8 — for now keep it default-scene):

```go
if err := a.api.InjectEvent(ctx, "", "", args); err != nil {
```

In `cmd/sim/smoke_test.go:90`, change:

```go
if injErr := api.InjectEvent(ctx, "", "Stinky Sam finds a suspicious sandwich"); injErr != nil {
```

to:

```go
if injErr := api.InjectEvent(ctx, "", "", "Stinky Sam finds a suspicious sandwich"); injErr != nil {
```

In `internal/world/world_test.go`, update **every** `InjectEvent(ctx, ...)` call. There are at least three callsites; each currently looks like `a.InjectEvent(ctx, "<target>", "<desc>")`. Change each to `a.InjectEvent(ctx, "", "<target>", "<desc>")`. Concretely:

- `TestInjectPersistsSpeechWhenSynthesisFails`: `w.API().InjectEvent(ctx, "", "trigger synthesis failure")` → `w.API().InjectEvent(ctx, "", "", "trigger synthesis failure")`
- `TestInjectAppendsAndDispatches`: `a.InjectEvent(ctx, "stinky-sam", "found a suspicious sandwich")` → `a.InjectEvent(ctx, "", "stinky-sam", "found a suspicious sandwich")`
- `TestInjectAppendBeforeBroadcast`: `a.InjectEvent(ctx, "x", "should fail")` → `a.InjectEvent(ctx, "", "x", "should fail")`
- `TestInjectBroadcastsSynthesizedToMemberMemory`: `w.API().InjectEvent(ctx, "", "the cat knocks over the lamp")` → `w.API().InjectEvent(ctx, "", "", "the cat knocks over the lamp")`

- [ ] **Step 4.8: Run all tests; expect them to pass including the new one**

Run: `go test ./...`
Expected: PASS everywhere.

- [ ] **Step 4.9: Commit**

```bash
git add internal/api/api.go internal/world/messages.go internal/world/api.go internal/world/world.go internal/world/world_test.go internal/irc/adapter.go cmd/sim/smoke_test.go
git commit -m "world: scene-targeted inject via explicit SceneID arg"
```

---

## Task 5: Insertion-order default scene

**Files:**
- Modify: `internal/world/world.go`
- Modify: `internal/world/world_test.go`

- [ ] **Step 5.1: Write the failing test**

Append to `internal/world/world_test.go`:

```go
func TestDefaultSceneIsFirstRegistered(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID) *character.Character {
		return &character.Character{
			ID: id, Name: string(id),
			Memory: memory.NewInMem(10),
			Inbox:  make(chan character.Perception, 1),
		}
	}
	first := &scene.Scene{ID: "first", Leader: mk("la"), Members: []*character.Character{mk("la")}}
	second := &scene.Scene{ID: "second", Leader: mk("lb"), Members: []*character.Character{mk("lb")}}

	w := New(Config{TickInterval: time.Hour}, st, &mockLLM{})
	w.RegisterScene(first)
	w.RegisterScene(second)

	// Call defaultScene 100 times; it must always return "first".
	for i := 0; i < 100; i++ {
		got := w.defaultScene()
		if got == nil || got.ID != "first" {
			t.Fatalf("iteration %d: want scene id 'first', got %v", i, got)
		}
	}
}
```

- [ ] **Step 5.2: Run; expect intermittent failures or `nil` returns**

Run: `go test ./internal/world/ -run TestDefaultSceneIsFirstRegistered -v -count=20`
Expected: at least one iteration sees "second" instead of "first" because Go map iteration is randomized. (If 20 runs all see "first" by luck, increase `-count`. The test is correct; the implementation is wrong.)

- [ ] **Step 5.3: Track scene insertion order**

Edit `internal/world/world.go`. In the `World` struct, add the field next to `scenes`:

```go
	scenes     map[api.SceneID]*scene.Scene
	sceneOrder []api.SceneID
	characters map[api.CharacterID]*character.Character
	charScene  map[api.CharacterID]api.SceneID
```

`New` doesn't need to initialize the slice (nil is a valid zero-value for append).

Update `RegisterScene`:

```go
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
```

Replace `defaultScene` with:

```go
func (w *World) defaultScene() *scene.Scene {
	if len(w.sceneOrder) == 0 {
		return nil
	}
	return w.scenes[w.sceneOrder[0]]
}
```

- [ ] **Step 5.4: Run; verify the test passes**

Run: `go test ./internal/world/ -run TestDefaultSceneIsFirstRegistered -v -count=20`
Expected: PASS, all iterations.

- [ ] **Step 5.5: Commit**

```bash
git add internal/world/world.go internal/world/world_test.go
git commit -m "world: stable default-scene by insertion order"
```

---

## Task 6: `dispatchSummon` validates place-scene exists

**Files:**
- Modify: `internal/world/world.go`
- Modify: `internal/world/world_test.go`

- [ ] **Step 6.1: Write the failing tests**

Append to `internal/world/world_test.go`:

```go
func TestSummonUnknownPlaceErrors(t *testing.T) {
	w, _, _ := newTestWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	err := w.API().Summon(ctx, "nowhere")
	if err == nil {
		t.Fatal("expected error summoning unknown place")
	}
	if !strings.Contains(err.Error(), "unknown place") {
		t.Fatalf("expected 'unknown place' in error, got %v", err)
	}
}

func TestSummonKnownPlaceWritesSummonEventScopedToPlaceScene(t *testing.T) {
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID) *character.Character {
		return &character.Character{
			ID: id, Name: string(id),
			Memory: memory.NewInMem(10),
			Inbox:  make(chan character.Perception, 1),
		}
	}
	gangLeader := mk("g-leader")
	gang := &scene.Scene{ID: "gang", Leader: gangLeader, Members: []*character.Character{gangLeader}}

	npc := mk("npc")
	cathedral := &scene.Scene{
		ID:      "place:cathedral",
		PlaceID: "cathedral",
		Leader:  npc,
		Members: []*character.Character{npc},
	}

	w := New(Config{TickInterval: time.Hour}, st, &mockLLM{})
	w.RegisterScene(gang)
	w.RegisterScene(cathedral)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	if err := w.API().Summon(ctx, "cathedral"); err != nil {
		t.Fatalf("summon: %v", err)
	}

	evs, err := st.Query(ctx, store.Filter{Kind: store.KindSummon})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("want 1 summon event, got %d", len(evs))
	}
	if evs[0].SceneID != "place:cathedral" {
		t.Fatalf("want summon scoped to place:cathedral, got %q", evs[0].SceneID)
	}
}
```

- [ ] **Step 6.2: Run; verify the new tests fail**

Run: `go test ./internal/world/ -run "Summon" -v`
Expected: FAIL — `dispatchSummon` currently routes to the default scene and never errors on unknown placeID.

- [ ] **Step 6.3: Replace `dispatchSummon`**

Edit `internal/world/world.go`. Replace `dispatchSummon` with:

```go
func (w *World) dispatchSummon(ctx context.Context, placeID api.PlaceID) error {
	sceneID := api.SceneID("place:" + string(placeID))
	sc, ok := w.scenes[sceneID]
	if !ok {
		return fmt.Errorf("world: unknown place %q", placeID)
	}
	return w.appendOnly(ctx, store.NewSummonEvent(sc.ID, placeID))
}
```

- [ ] **Step 6.4: Run; verify all world tests pass**

Run: `go test ./internal/world/ -v`
Expected: PASS, including the new Summon tests.

- [ ] **Step 6.5: Commit**

```bash
git add internal/world/world.go internal/world/world_test.go
git commit -m "world: summon resolves a registered place-scene or errors"
```

---

## Task 7: Construct place-scenes at boot

**Files:**
- Modify: `cmd/sim/main.go`

- [ ] **Step 7.1: Refactor character construction into a helper inside `runCtx`**

Edit `cmd/sim/main.go`. After the `places, err := config.LoadPlaces(...)` block (added in Step 3.5), the existing `byID := make(map[api.CharacterID]*character.Character, len(chars))` loop hydrates every character. Keep it as-is — no change. NPCs are already in `chars` (Task 1 added them to `seed/characters.yaml`).

- [ ] **Step 7.2: After the gang scene is constructed, build a scene per place and register it**

Locate the block in `runCtx` (currently around line 158) that ends with `w.RegisterScene(sc)` for the gang scene. **Before** the `worldAPI := w.API()` line that follows, insert:

```go
	for _, p := range places {
		if len(p.NPCs) == 0 {
			logger.Warn("place has no npcs; skipping", "place", p.ID)
			continue
		}
		placeScene := &scene.Scene{
			ID:      api.SceneID("place:" + p.ID),
			PlaceID: api.PlaceID(p.ID),
			Router:  scene.LLMRouter{Model: llmImpl, PreFilterK: 0, MaxConsult: 0},
		}
		for _, nid := range p.NPCs {
			npc, ok := byID[api.CharacterID(nid)]
			if !ok {
				// Validate ran earlier; reaching here would be a code bug.
				return fmt.Errorf("place %s references unknown character %s", p.ID, nid)
			}
			placeScene.Members = append(placeScene.Members, npc)
		}
		// First NPC in the yaml list is the leader.
		placeScene.Leader = placeScene.Members[0]
		w.RegisterScene(placeScene)
		logger.Info("registered place scene",
			"place", p.ID, "members", len(placeScene.Members), "leader", placeScene.Leader.ID)
	}
```

- [ ] **Step 7.3: Run a build to confirm imports/types resolve**

Run: `go build ./...`
Expected: success. `place` package is unused here — the NPC ids come from `PlaceSpec.NPCs` so no import of `internal/place` is needed.

- [ ] **Step 7.4: Run all tests; smoke test should still pass (gang-only) and runtime configuration tests too**

Run: `go test ./...`
Expected: PASS. The existing `TestSmokeEndToEnd` constructs its own world without using `runCtx`, so it's unaffected. The existing `TestRunCtxAbortsBootOnHydrateFailure` exercises `runCtx`; with cathedral NPCs added to `seed/characters.yaml`, the failing factory aborts at the *first* Hydrate (whichever character iterates first), so the test still passes.

- [ ] **Step 7.5: Commit**

```bash
git add cmd/sim/main.go
git commit -m "sim: register a scene per loaded place at boot"
```

---

## Task 8: IRC `!inject @<scene-id> <desc>` parser

**Files:**
- Modify: `internal/irc/adapter.go`
- Test: `internal/irc/inject_parse_test.go` (new)

- [ ] **Step 8.1: Write the failing test**

Create `internal/irc/inject_parse_test.go`:

```go
package irc

import "testing"

func TestParseInjectArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        string
		wantScene   string
		wantDesc    string
		wantOK      bool
	}{
		{"empty", "", "", "", false},
		{"plain", "found a sandwich", "", "found a sandwich", true},
		{"scene only", "@cathedral", "", "", false},
		{"scene and desc", "@cathedral the floor smells of incense", "cathedral", "the floor smells of incense", true},
		{"scene with namespace colon", "@place:cathedral incense again", "place:cathedral", "incense again", true},
		{"extra whitespace", "@cathedral    candle falls   ", "cathedral", "candle falls", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sceneID, desc, ok := parseInjectArgs(tt.args)
			if ok != tt.wantOK {
				t.Fatalf("ok: want %v, got %v", tt.wantOK, ok)
			}
			if !ok {
				return
			}
			if string(sceneID) != tt.wantScene {
				t.Errorf("scene: want %q, got %q", tt.wantScene, sceneID)
			}
			if desc != tt.wantDesc {
				t.Errorf("desc: want %q, got %q", tt.wantDesc, desc)
			}
		})
	}
}
```

- [ ] **Step 8.2: Run; verify it fails**

Run: `go test ./internal/irc/ -run TestParseInjectArgs -v`
Expected: FAIL with `undefined: parseInjectArgs`.

- [ ] **Step 8.3: Implement the parser and wire it into `cmdInject`**

Edit `internal/irc/adapter.go`. Add the helper near `cmdInject`:

```go
// parseInjectArgs splits the !inject argument string into an optional scene
// id and a description. Forms:
//   "<desc>"                → ("", "<desc>", true)
//   "@<scene-id> <desc>"    → ("<scene-id>", "<desc>", true)
// Empty args, or @scene with no description, returns ok=false.
func parseInjectArgs(args string) (api.SceneID, string, bool) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", "", false
	}
	if !strings.HasPrefix(args, "@") {
		return "", args, true
	}
	rest := strings.TrimPrefix(args, "@")
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) < 2 {
		return "", "", false
	}
	sceneID := strings.TrimSpace(parts[0])
	desc := strings.TrimSpace(parts[1])
	if sceneID == "" || desc == "" {
		return "", "", false
	}
	return api.SceneID(sceneID), desc, true
}
```

Replace the body of `cmdInject` with:

```go
func (a *Adapter) cmdInject(ctx context.Context, args string, reply func(string)) {
	sceneID, desc, ok := parseInjectArgs(args)
	if !ok {
		reply("usage: !inject [@<scene-id>] <description>")
		return
	}
	if err := a.api.InjectEvent(ctx, sceneID, "", desc); err != nil {
		reply("inject failed: " + err.Error())
		return
	}
	reply("injected.")
}
```

- [ ] **Step 8.4: Run; verify tests pass**

Run: `go test ./internal/irc/ -v`
Expected: PASS.

- [ ] **Step 8.5: Commit**

```bash
git add internal/irc/adapter.go internal/irc/inject_parse_test.go
git commit -m "irc: parse !inject @<scene-id> for targeted injects"
```

---

## Task 9: Cathedral smoke test

**Files:**
- Modify: `cmd/sim/smoke_test.go`

- [ ] **Step 9.1: Write the failing test**

Append to `cmd/sim/smoke_test.go`:

```go
// TestSummonCathedralInjectAndSpeak boots the full runtime wiring (real
// seed YAML, echoLLM, SQLite, place loader), summons the cathedral, and
// injects a scenario scoped to the cathedral scene. It asserts:
//   - !summon succeeds (no error)
//   - the inject produces speech from at least one cathedral NPC
//   - the synthesized event is attributed to the cathedral's leader (vicar)
//   - the gang scene is undisturbed: zero events with the gang scene id
func TestSummonCathedralInjectAndSpeak(t *testing.T) {
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
	if len(places) == 0 {
		t.Fatal("no places loaded; seed/places empty?")
	}

	llmImpl := echoLLM{}

	byID := make(map[api.CharacterID]*character.Character, len(chars))
	for _, spec := range chars {
		id := api.CharacterID(spec.ID)
		byID[id] = &character.Character{
			ID:           id,
			Name:         spec.Name,
			Persona:      spec.Persona,
			Capabilities: spec.Capabilities,
			Blurb:        spec.Blurb,
			Memory:       memory.NewEmbedded(llmImpl, 200),
			Inbox:        make(chan character.Perception, 8),
		}
	}

	w := world.New(world.Config{TickInterval: time.Hour}, st, llmImpl)

	// Gang scene first so it remains the default.
	g := groups[0]
	gang := &scene.Scene{ID: api.SceneID(g.ID), Router: scene.LLMRouter{Model: llmImpl}}
	for _, mid := range g.Members {
		gang.Members = append(gang.Members, byID[api.CharacterID(mid)])
	}
	gang.Leader = byID[api.CharacterID(g.Leader)]
	w.RegisterScene(gang)

	var cathedralSceneID api.SceneID
	var cathedralLeader api.CharacterID
	npcIDs := map[api.CharacterID]struct{}{}
	for _, p := range places {
		if p.ID != "cathedral" {
			continue
		}
		sc := &scene.Scene{
			ID:      api.SceneID("place:" + p.ID),
			PlaceID: api.PlaceID(p.ID),
			Router:  scene.LLMRouter{Model: llmImpl},
		}
		for _, nid := range p.NPCs {
			c, ok := byID[api.CharacterID(nid)]
			if !ok {
				t.Fatalf("place %s npc %s not in characters", p.ID, nid)
			}
			sc.Members = append(sc.Members, c)
			npcIDs[c.ID] = struct{}{}
		}
		sc.Leader = sc.Members[0]
		w.RegisterScene(sc)
		cathedralSceneID = sc.ID
		cathedralLeader = sc.Leader.ID
	}
	if cathedralSceneID == "" {
		t.Fatal("cathedral place not found in seed/places")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan struct{})
	go func() { defer close(runDone); _ = w.Run(ctx) }()

	wapi := w.API()

	if err := wapi.Summon(ctx, "cathedral"); err != nil {
		t.Fatalf("summon: %v", err)
	}
	if err := wapi.InjectEvent(ctx, cathedralSceneID, "", "the flagstones smell of incense"); err != nil {
		t.Fatalf("inject: %v", err)
	}

	entries, err := wapi.Log(ctx, time.Hour)
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	var sawSummon, sawSynth, sawSpeech bool
	for _, e := range entries {
		if e.SceneID == api.SceneID(g.ID) && e.Kind != string(store.KindSceneEnter) {
			// Any event landing on the gang scene id from this test would
			// indicate cross-scene leakage. The only allowed gang-id event
			// is a scene_enter (which we don't emit) — strict zero events.
			t.Errorf("gang scene saw unexpected event: %+v", e)
		}
		if e.SceneID != cathedralSceneID {
			continue
		}
		switch store.Kind(e.Kind) {
		case store.KindSummon:
			sawSummon = true
		case store.KindSynthesized:
			sawSynth = true
			if e.Actor != string(cathedralLeader) {
				t.Errorf("synthesized actor: want %s, got %s", cathedralLeader, e.Actor)
			}
		case store.KindSpeech:
			if _, ok := npcIDs[api.CharacterID(e.Actor)]; !ok {
				t.Errorf("speech actor not an NPC: %s", e.Actor)
			}
			sawSpeech = true
		}
	}
	if !sawSummon {
		t.Error("no summon event on cathedral scene")
	}
	if !sawSpeech {
		t.Error("no NPC speech on cathedral scene — orchestrate did not fan out")
	}
	if !sawSynth {
		t.Error("no synthesized event on cathedral scene — leader did not synthesize")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("world.Run did not return after cancel")
	}
}
```

- [ ] **Step 9.2: Run; verify it passes (after the prior tasks landed)**

Run: `go test ./cmd/sim/ -run TestSummonCathedralInjectAndSpeak -v`
Expected: PASS. If it fails:

- "no NPC speech" → either Task 7 didn't register the cathedral scene, or `scene.Orchestrate` short-circuits because `Members` is empty. Read the test log for the `runCtx` "registered place scene" line; absence means Task 7's wiring landed in the wrong place.
- "summon: unknown place" → Task 6's scene-id derivation (`place:<placeID>`) does not match what Task 7 registered. Both must use the exact string `"place:" + p.ID`.
- "scene not found" on inject → `InjectEvent` is passing `cathedralSceneID` but the scene didn't register; check the smoke test built the cathedral scene before calling `wapi.InjectEvent`.

- [ ] **Step 9.3: Run the entire test suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 9.4: Commit**

```bash
git add cmd/sim/smoke_test.go
git commit -m "sim: smoke test for summon cathedral + scene-scoped inject"
```

---

## Task 10: Update BACKLOG follow-ups

**Files:**
- Modify: `BACKLOG.md`

- [ ] **Step 10.1: Mark L2 shipped; record deferred follow-ups**

Edit `BACKLOG.md`. Replace the entire "L2. Place instantiation into Scenes (cathedral case)" section with:

```markdown
### ~~L2. Place instantiation into Scenes (cathedral case)~~ — SHIPPED 2026-05-12

Plan at `docs/superpowers/plans/2026-05-12-place-instantiation.md`; spec at `docs/superpowers/specs/2026-05-12-place-instantiation-design.md`.

What shipped:
- `config.LoadPlaces(dir)` walks `seed/places/*.yaml`.
- `config.Validate` cross-checks place NPC ids against character set.
- Three new orphan characters in `seed/characters.yaml` (vicar, caretaker, cathedral-cat).
- `WorldAPI.InjectEvent` takes an explicit `sceneID` arg; empty resolves to the first-registered scene.
- `World.sceneOrder` makes `defaultScene` deterministic.
- `dispatchSummon` resolves `place:<placeID>` and errors on unknown places.
- `cmd/sim/main.go` registers one scene per loaded place at boot; first NPC in the yaml is leader.
- IRC `!inject @<scene-id> <desc>` syntax for targeted injects.
- `cmd/sim/smoke_test.go::TestSummonCathedralInjectAndSpeak` covers the full path.

Deferred follow-ups (open):
- **Runtime scene registration** — `World.RegisterScene` panics after `Run` starts. Lifting this requires coordinator-goroutine-owned scene creation so a place can be summoned without being pre-loaded.
- **Scene idle-out for place-scenes** — place-scene NPC goroutines stay alive for the binary's lifetime. Add an idle timer.
- **Multiple simultaneous instances of the same place** — one scene per `Place.ID` today; NPC memory rows are keyed by character id so two cathedrals over time share memory. Per-instance scoping needed when this lands.
- **Ambient tick fan-out** — `handleTick` only emits to the default scene. Place-scenes never receive ambient ticks.
- **Cross-scene perception** — explicitly out of scope; events stay isolated.
```

- [ ] **Step 10.2: Commit**

```bash
git add BACKLOG.md
git commit -m "docs: mark BACKLOG L2 shipped; note place-instantiation follow-ups"
```

---

## Post-flight checklist

- [ ] **`go vet ./...` is clean**

Run: `go vet ./...`
Expected: no output.

- [ ] **`go test ./...` passes**

Run: `go test ./...`
Expected: PASS everywhere. If the Gemini smoke test is gated on an env var (`gemini_smoke_test.go`), that gate still applies.

- [ ] **Binary boots with the new seed**

Run: `go run ./cmd/sim -irc-server=""` (no IRC needed)
Expected: log lines `registered place scene place=cathedral members=3 leader=vicar` and then `world running scenes=2 characters=6 ...`. Ctrl-C to exit.
