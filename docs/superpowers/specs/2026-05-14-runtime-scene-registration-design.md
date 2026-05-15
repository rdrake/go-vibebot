# Runtime Scene Registration — Design

**Date:** 2026-05-14
**Status:** Spec (rev. after dream-team review)
**Predecessor:** `docs/superpowers/specs/2026-05-12-place-instantiation-design.md`
**Implementation plan:** to be authored at `docs/superpowers/plans/2026-05-14-runtime-scene-registration.md`

## Goal

Allow operators to spin up a new place-scene at runtime, without restarting the
binary or editing `seed/places/*.yaml`. The first concrete surface is the IRC
command

```
!summon <place-id> n=<id1>,<id2>,... [free-form description]
```

and an equivalent MCP `summon` tool extension. The architectural prerequisite
is lifting the post-`Run` panic on `World.RegisterScene` so scene mutation can
happen safely while the coordinator is live.

## Scope

In scope for this round:

- Ad-hoc places that reference characters already loaded from
  `seed/characters.yaml`.
- IRC `!summon <place-id> n=...` extension, with the existing single-arg form
  preserved verbatim.
- MCP `summon` tool gains optional `npcs[]` and `description` fields.
- Single-writer, coordinator-owned scene registration via a new
  `RegisterSceneCmd` on the existing `commands` channel.
- `-race`-clean concurrent registration.

Out of scope — explicitly deferred (see "Deferred follow-ups" in the
Migration section):

- LLM-generated places and characters (the "C scope" from brainstorming).
- Place-scene idle-out.
- Multiple concurrent instances of the same place id.
- Ambient ticks fan-out to place-scenes.
- Truly new characters not present in `seed/characters.yaml`.
- Persisting ad-hoc places back to YAML.
- Refining `Where`/`Nudge` semantics for multi-scene characters
  (likely shape: a `charActiveScene` map alongside `charScene`, or a
  `latestSceneOf(charID)` helper that walks `sceneOrder` in reverse).

## Architecture

The world coordinator goroutine is the only writer of `w.scenes`,
`w.sceneOrder`, `w.characters`, and `w.charScene`. Today the only path that
writes those is the boot helper `World.RegisterScene`, which panics when
called after `Run`.

We add one new variant to the `Command` interface that already drains on the
coordinator:

```go
type RegisterSceneCmd struct {
    Scene *scene.Scene
    Reply chan error
}

func (RegisterSceneCmd) isCommand() {}
```

The struct name is suffixed `Cmd` to disambiguate from the existing
`*World.RegisterScene` method — Go's namespacing tolerates the collision but
human readers will not.

The coordinator's `handleCommand` switch grows one arm. The boot helper and
the runtime command both call a shared `registerSceneLocked(s *scene.Scene)
error`. The boot helper stays panic-on-error so misconfigured seed data still
fails loudly. The runtime command returns the error.

`WorldAPI` gains one method (added to `internal/api/api.go` alongside the
existing `Summon`):

```go
SummonNew(ctx context.Context, placeID api.PlaceID,
          npcs []api.CharacterID, description string) (api.SceneID, error)
```

It returns the registered scene id so the caller (IRC adapter, MCP tool,
future CLI) can act on the new scene in the same round-trip without
recomputing the `"place:" + placeID` formula.

`SummonNew` runs three or four coordinator round-trips:

1. `charactersByIDReq` — resolve the npc id list to `*character.Character`
   pointers. Coordinator-side handler so map reads stay single-threaded.
2. `RegisterSceneCmd` — register the new scene with the resolved members.
3. Existing `Summon` command — append the `KindSummon` event for the
   freshly-registered place.
4. If `description != ""`, an `Inject` against the new scene id.

Step order rationale: `Summon → Inject` (announce, then seed the premise).
Order 4-then-3 would make the inject-failure path atomic at the event-log
level, but it would not roll back the scene registration, so a failure still
leaves a registered-but-unannounced "ghost place" that is harder to debug
than the current order's "announced-but-no-premise" trail. The spec accepts
the current order with that trade-off explicit.

Append-before-broadcast holds: `KindSummon` is appended by `dispatchSummon`,
`KindInject` by `dispatchInject`, both before any fan-out.

## Components

### 1. Shared `registerSceneLocked`

Single mutation point. Validates uniqueness and member existence; refuses to
partially register on failure.

```go
func (w *World) registerSceneLocked(s *scene.Scene) error {
    if s == nil || s.ID == "" {
        return errors.New("world: scene must have an id")
    }
    if _, dup := w.scenes[s.ID]; dup {
        return fmt.Errorf("world: duplicate scene id %q", s.ID)
    }
    for _, m := range s.Members {
        if _, ok := w.characters[m.ID]; !ok {
            return fmt.Errorf(
                "world: scene %q references unknown character %q",
                s.ID, m.ID,
            )
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

Notes:

- The member-existence check enforces "only existing characters" — there is
  no character-spawning side effect here. New characters require a binary
  restart in this round.
- `charScene` is single-valued and is **not overwritten** when an existing
  character joins a second scene. The first scene registered wins for
  `Where`/`Nudge` routing.
- A character that is present in `w.characters` but in no boot-time scene
  (e.g., listed in `characters.yaml` but referenced by no place) has no
  `charScene` entry at boot. The first runtime scene that includes them is
  recorded — this is documented but not relied on by current seed data.

### 2. Boot helper

`World.RegisterScene` becomes a thin wrapper:

```go
func (w *World) RegisterScene(s *scene.Scene) {
    if w.running.Load() {
        panic("world: RegisterScene called after Run — use WorldAPI.SummonNew")
    }
    if err := w.registerSceneLocked(s); err != nil {
        panic("world: " + err.Error())
    }
}
```

Pre-`Run` callers (`cmd/sim/main.go`) keep their existing call sites.

### 3. `RegisterSceneCmd` + dispatch

```go
// in internal/world/messages.go
type RegisterSceneCmd struct {
    Scene *scene.Scene
    Reply chan error
}

func (RegisterSceneCmd) isCommand() {}
```

In `handleCommand`:

```go
case RegisterSceneCmd:
    c.Reply <- w.registerSceneLocked(c.Scene)
```

The trailing `default: panic("unhandled command")` arm relies on the
sealed-interface invariant; its comment is updated to list the new variant
explicitly so the invariant stays load-bearing documentation.

### 4. `charactersByIDReq`

Mirrors `whereReq`/`whoReq`/`charactersReq`/`placesReq` — same name for the
field and the request struct, `Resp` suffix for the response.

Added field on `World`:

```go
charactersByIDReq chan charactersByIDReq // (struct of the same name)
```

```go
type charactersByIDReq struct {
    ids   []api.CharacterID
    reply chan charactersByIDResp
}
type charactersByIDResp struct {
    chars []*character.Character
    err   error
}
```

Coordinator arm in the `select`:

```go
case req := <-w.charactersByIDReq:
    req.reply <- w.lookupCharactersByID(req.ids)
```

```go
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
            err: fmt.Errorf("unknown character(s): %s",
                strings.Join(missing, ", ")),
        }
    }
    return charactersByIDResp{chars: out}
}
```

### 5. `apiImpl.SummonNew`

Routes the `RegisterSceneCmd` round-trip through the existing
`apiImpl.send(ctx, mk)` helper so cancellation and reply handling stay in one
place. `SummonNew` returns the registered `api.SceneID` so callers can chain
follow-up calls.

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

    if err := a.send(ctx, func(reply chan<- error) Command {
        return RegisterSceneCmd{Scene: sc, Reply: reply}
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

`requestCharactersByID` is a new helper on `*World` that posts to
`charactersByIDReq` and awaits the reply, mirroring the existing `where`/`who`
helpers in `internal/world/reads.go`.

### 6. IRC `!summon` extension

`Adapter.cmdSummon` is rewritten; `parseSummonArgs` lives in
`internal/irc/adapter.go` next to the existing `parseInjectArgs`.

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

`parseSummonArgs(s string) (api.PlaceID, []api.CharacterID, string, error)`
behaviour — all input forms are pinned by tests 7–13 below:

- First whitespace-delimited token is `placeID`. Empty → error.
- If the **second** token starts with `n=`, the comma-separated list after
  `=` becomes `npcs`. Empty entries (e.g. `n=a,,b`) → error. Empty list
  (`n=` with no value) → error `"n= requires at least one character id"`.
  Remaining tokens form `description`.
- If the second token does not start with `n=`, the input is interpreted as
  legacy form: `npcs` is `nil`, `description` is `""`. If any trailing
  tokens are present, return an error
  `"description without n=...; use !summon <id> n=... <description> to create a new scene"`
  so the user gets feedback rather than silent discard.
- If a token *after* the second position starts with `n=`, return an error
  `"n= must be the second token; got it at position N"` to prevent the
  `!summon tavern A dark night n=bertha` footgun.
- Leading/trailing whitespace around the whole input is trimmed.

### 7. MCP `summon` tool extension

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

Handler:

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
            OK: true,
            SceneID: "place:" + in.PlaceID,
            Message: "summoned.",
        }, nil
    }
    npcs := make([]api.CharacterID, len(in.NPCs))
    for i, s := range in.NPCs {
        npcs[i] = api.CharacterID(s)
    }
    sceneID, err := a.api.SummonNew(
        ctx, api.PlaceID(in.PlaceID), npcs, in.Description,
    )
    if err != nil {
        return toolError(fmt.Sprintf("summon failed: %s", err.Error())), SummonOutput{}, nil
    }
    a.logger.Info("mcp summon (new)", "place", in.PlaceID, "npcs", len(npcs))
    return nil, SummonOutput{
        OK: true,
        SceneID: string(sceneID),
        Message: "summoned.",
    }, nil
}
```

### 8. Multi-scene membership (this round)

A character may belong to multiple registered scenes. The character goroutine
is scene-agnostic — it consumes from a single `Inbox` and writes to whichever
`Reply` channel each `Perception` carries. No goroutine churn is required to
add a character to a second scene.

`w.charScene` stays single-valued. The first scene a character is registered
into wins. Concretely:

- `WorldAPI.Where(charID)` returns the boot-time scene even when the
  character is currently active in an ad-hoc place.
- `WorldAPI.Nudge(charID)` always nudges in the boot-time scene. To prompt
  a character inside an ad-hoc place, callers `InjectEvent` against the
  ad-hoc scene id directly.
- `WorldAPI.Who(sceneID)` returns `s.Members` of the specified scene, so it
  correctly reflects per-scene membership.

A comment on the `WorldAPI` interface in `internal/api/api.go` near the
`Where`/`Nudge` declarations documents this asymmetry so callers reading the
interface — not just the implementation — see it.

This asymmetry is documented in the deferred follow-ups for a future round to
refine (see Scope: likely fix shape is a `charActiveScene` map alongside
`charScene`, or a `latestSceneOf(charID)` helper that walks `sceneOrder` in
reverse).

## Error semantics

| Failure                                                | Behaviour                                                              |
|--------------------------------------------------------|------------------------------------------------------------------------|
| `place_id` empty                                       | `SummonNew` returns error; no events; no registration                  |
| `npcs` empty                                           | `SummonNew` returns error; no events; no registration                  |
| Unknown character id in `npcs`                         | `SummonNew` returns error naming all missing ids; no partial registration |
| Place id already registered                            | `SummonNew` returns "duplicate scene id" error; no events; no registration |
| `ctx` cancelled during a coordinator round-trip        | `SummonNew` returns `ctx.Err()`                                        |
| `KindSummon` append fails after register               | Scene stays registered, no `KindSummon` in log, error returned; caller may retry `Summon(placeID)` |
| `Inject` for description fails after register + summon | Scene stays registered, `KindSummon` already in the log, no `KindInject`, error returned with the new scene id; caller may retry the inject |

The last two rows leave non-atomic state. Both are documented and accepted:
rolling back a successful register on a later-step failure requires either a
tombstone event or coordinator-side rollback, both heavier than the actual
failure modes warrant. The IRC adapter surfaces the returned scene id to the
user so a manual retry has the right target.

## Concurrency

- `commands` channel capacity stays at 16. Three or four round-trips per
  `SummonNew` is well within the existing budget for normal interactive load
  (single-digit RPS).
- **Backpressure assumption**: callers block on the channel send under a
  per-request `ctx`; cancelling any one caller's ctx unblocks only that
  caller. A pathological burst (32+ concurrent `SummonNew`s, each with a
  live ctx, against a coordinator slowed by an LLM call inside an unrelated
  `dispatchInject`) would pile up pending sends and increase tail latency.
  This is documented; a semaphore around `SummonNew` is deferred until
  load makes it real.
- **Same-place collision**: two callers racing `SummonNew("spire", ...)`
  serialise on the coordinator. One wins `RegisterSceneCmd`; the other gets
  the duplicate-scene-id error from `registerSceneLocked` and returns before
  calling `Summon`. The event log contains exactly one `KindSummon` for the
  place. Pinned by `TestSummonNewSamePlaceConcurrentCollision`.
- No `sync.Mutex` is added. The coordinator goroutine remains the only
  writer of the four state maps.
- `-race` on the test suite must stay clean.

## Tests

### `internal/world/world_test.go`

1. `TestRegisterSceneAfterRunDoesNotPanic` — start `Run`, post
   `RegisterSceneCmd` with a synthetic scene that references existing
   characters; assert reply is `nil` and `Inject` against the new scene id
   succeeds.
2. `TestSummonNewUnknownCharacterErrors` — request a scene whose npc list
   contains a missing id; assert error message names the missing id; assert
   `scenes` map size unchanged.
3. `TestSummonNewDuplicatePlaceErrors` — pre-register `cathedral`; call
   `SummonNew("cathedral", ...)`; assert error; assert single registration.
4. `TestSummonNewWithDescriptionWritesInject` — assert event log under
   `place:<id>` contains one `KindSummon` followed by one `KindInject`.
5. `TestSummonNewWithoutDescriptionEmitsOnlySummon` — assert no `KindInject`.
6. `TestSummonNewConcurrentDistinctPlacesSafe` — fire N goroutines each
   summoning a distinct place id; assert all succeed; assert `scenes` map
   size equals N plus boot-time count. Run with `-race`.
7. `TestSummonNewSamePlaceConcurrentCollision` — fire N goroutines all
   summoning the same place id; assert exactly one nil error and N-1
   duplicate-scene-id errors; assert exactly one `KindSummon` in the log.
   Run with `-race`.
8. `TestSummonNewCtxCancelledDuringRoundTrip` — cancel ctx before the first
   channel send; assert error wraps `context.Canceled`; assert `scenes` map
   unchanged. Second variant: cancel from a goroutine racing the call.
9. `TestSummonNewKindSummonAppendFailure` — install a store whose `Append`
   errors on `KindSummon`; assert the scene stays registered, the error is
   surfaced, and no partial log entries are written.
10. `TestWhereAfterSummonNewReturnsBootScene` — register gang scene at boot
    with `booger-bertha`; `SummonNew("spire", [booger-bertha], "")` at
    runtime; assert `Where(booger-bertha)` returns the gang scene id.
11. `TestNudgeAfterSummonNewTargetsBootScene` — same setup; `Nudge(booger-
    bertha)` writes a `KindNudge` event with the gang scene id, not
    `place:spire`.

### `internal/irc/`

12. `TestParseSummonLegacy` — `cathedral` → `(cathedral, nil, "")`.
13. `TestParseSummonAdHoc` — `spire n=vicar,booger-bertha A drafty steeple.`
    → `(spire, [vicar booger-bertha], "A drafty steeple.")`.
14. `TestParseSummonAdHocNoDescription` — `spire n=vicar` → desc `""`.
15. `TestParseSummonRejectsEmptyNpcEntry` — `spire n=vicar,,bertha` → error.
16. `TestParseSummonRejectsEmptyNpcList` — `spire n=` → error.
17. `TestParseSummonRejectsLegacyWithTrailingText` —
    `cathedral some description` → error pointing the user at `n=...`.
18. `TestParseSummonRejectsNEqualsAfterSecondToken` —
    `tavern A dark night n=bertha` → error.
19. `TestParseSummonTrimsWhitespace` — `"  spire  n=vicar  desc  "` →
    `(spire, [vicar], "desc")`.
20. `TestCmdSummonNewRoutesToSummonNew` — fake_world records a
    `SummonNewCall` with the parsed args; `SummonCall` count unchanged.

### `internal/mcp/`

21. `TestE2ESummonNewAdHoc` — MCP `summon` with `place_id`+`npcs`+
    `description`; fake_world records a `SummonNewCall`; tool result
    `scene_id` matches `"place:" + place_id`.
22. `TestE2ESummonLegacyStillWorks` — only `place_id`; fake_world records a
    legacy `SummonCall`, no `SummonNewCall`; tool result `scene_id` is
    `"place:" + place_id`.
23. `TestE2ESummonNewErrorSurfacesAsToolError` — fake_world returns an error
    from `SummonNew`; tool result has `IsError=true` and the message.

### `cmd/sim/`

24. `TestRuntimeAdHocPlaceSummonViaIRC` — drive an IRC line
    `!summon spire n=vicar,booger-bertha A drafty steeple.` through the
    adapter against a live `World` with an in-memory store and the
    `echoLLM{}` test double already used by `TestSummonCathedralInjectAndSpeak`
    in `cmd/sim/smoke_test.go`; assert the event log contains, scoped to
    `place:spire`, a `KindSummon`, a `KindInject`, at least one `KindSpeech`
    from an NPC member, and a `KindSynthesized` by the leader (`vicar`);
    assert no events leaked to the gang scene id.

Test #20 (IRC fake-world routing) is a deliberate narrow boundary test —
the cmd/sim smoke test in #24 exercises the full stack but does not
distinguish between `Summon` and `SummonNew` calls on the API at unit
granularity; #20 pins that contract.

## Migration & docs

- No SQL changes. Existing event-log filters and replays work unchanged.
- `README.md`: one line under the IRC commands list documenting
  `!summon <id> [n=...] [description]`.
- `BACKLOG.md`: the existing "Runtime scene registration" deferred follow-up
  entry under L2 is marked shipped by striking through the heading and
  appending `— SHIPPED 2026-05-14`, matching the precedent set by the L1,
  L2, L3, and S1 entries from 2026-05-12. Surviving follow-ups stay in
  place: idle-out, multiple concurrent instances of the same place,
  ambient ticks to place-scenes, `Where`/`Nudge` multi-scene refinement,
  persistence, LLM-generated places/characters.

## Estimated diff

Honest estimate after review: ~310 LOC code + ~290 LOC tests, spread across
`internal/world/`, `internal/api/`, `internal/irc/`, `internal/mcp/`, and
`cmd/sim/`. The `charactersByIDReq` round-trip plumbing (message type,
coordinator arm, helper wrapper) and the test-doubles updates in both
`fake_world_test.go` files are the items the earlier ~250/250 estimate
under-counted.

## Open questions

None for this round.
