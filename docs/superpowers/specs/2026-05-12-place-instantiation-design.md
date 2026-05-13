# Place instantiation into Scenes (cathedral case)

Date: 2026-05-12
Status: Approved — pending implementation plan
Backlog item: L2

## Problem

`internal/place/place.go` defines a `Place` type. `seed/places/cathedral.yaml` exists. `config.LoadPlace` parses one yaml file. None of it is wired: nothing calls `LoadPlace`, no `*character.Character` is built from a place's NPC list, and the `!summon <place-id>` path in `internal/irc/adapter.go` only records a `KindSummon` event through `world.dispatchSummon` — it never spins anything up. Place data sits inert.

## Goal

When the simulator boots, the cathedral exists as a live scene with its NPCs (vicar, caretaker, cathedral-cat) running their decide loops. `!summon cathedral` confirms the scene is open; a scenario inject scoped to that scene reaches its NPCs and produces a synthesized utterance. The bar's gang does not perceive the cathedral's events, and vice versa.

## Non-goals

- **Runtime scene registration.** `World.RegisterScene` panics after `Run` starts. The skeleton pre-registers every place-scene at boot. Lifting this restriction (so a place can be summoned on demand without being pre-loaded) is a follow-up.
- **Scene idle-out.** Place-scenes stay alive for the binary's lifetime. Idle-out lands later.
- **Multiple simultaneous instances of the same place.** One scene per `Place.ID`. "Two cathedrals at once" is not in scope.
- **Cross-scene event leakage.** The "memory shared across scenes" idea is a much later phase. Scenes are isolated.
- **Per-NPC routing/leadership heuristics.** The first NPC in the place's `npcs` list is the leader. Don't model vicar-vs-cat hierarchy.
- **!summon arg validation for arbitrary IRC inputs.** If the place isn't loaded, `!summon` returns "unknown place". No fuzzy matching.

## Architecture

Place-scenes are first-class scenes. They are constructed at boot from yaml seeds and registered before `World.Run` starts, alongside the group scene. The `!summon` verb becomes a confirmation/log event scoped to the place-scene; it does not allocate anything.

Each NPC is an ordinary `*character.Character` with `memory.Embedded` + `WithPersister` — identical wiring to a group member. NPC seeds live in the existing `seed/characters.yaml`; `config.Validate` already permits characters that no group references. A new `LoadPlaces` directory walker materializes every `seed/places/*.yaml` into `[]PlaceSpec`, and a new validation pass cross-checks every place's `npcs` against the loaded character set.

Inject routing changes shape: `WorldAPI.InjectEvent` gains an explicit `sceneID api.SceneID` argument. Empty sceneID falls back to the default scene (the first one registered) so the existing `!inject <text>` IRC syntax keeps working for the gang. The IRC adapter recognises a leading `@<scene-id>` token on `!inject` to target a specific scene.

### Why pre-register at boot, not runtime-instantiate on summon

The BACKLOG plan listed two options for `SummonPlace`: pre-register at boot (simpler) or make `RegisterScene` runtime-safe (more code). We pick pre-register because:

- `World` owns `scenes`, `characters`, and `charScene` maps that are read by every coordinator-goroutine handler. Concurrent insertion would require either coordinator-goroutine-owned registration (an in-Run command) or a lock — both invite races we have no current need to solve.
- The skeleton goal — make !summon do something visible — is met without runtime registration. The cathedral is always live; `!summon` opens the curtain.
- Runtime registration is a clean separable follow-up. It changes nothing else.

The downside: every place's NPCs are running goroutines even when no one is "there." For three NPCs across one place, that is two extra goroutines blocked on inbox reads. Acceptable.

### Why scene id `place:<placeID>`

Group scenes today use the group's id (`the-gang`) as `SceneID`. Place-scenes need their own id namespace. Prefix `place:` keeps it visually distinct in log entries and prevents a future group named `cathedral` from colliding. The `PlaceID` field on `scene.Scene` was already plumbed; we set it.

### Leader selection

The first NPC in `Place.NPCs` is the scene leader. Document this in the yaml comment for `cathedral.yaml`. Reordering the list reorders leadership — that's a clear contract operators can rely on.

### Inject-to-scene routing

```go
// internal/api/api.go
type WorldAPI interface {
    InjectEvent(ctx context.Context, sceneID SceneID, target, description string) error
    // ...
}
```

`SceneID == ""` resolves to the default scene (first registered). Non-empty resolves via the world's `scenes` map; an unknown id returns an error. The `Inject` command struct gains a `SceneID` field; the dispatcher routes by it.

The default-scene-by-iteration-order in `world.defaultScene` becomes a problem the moment a second scene is registered, because Go map iteration is non-deterministic. We add an explicit `sceneOrder []api.SceneID` slice to `World` populated by `RegisterScene` in insertion order; `defaultScene()` returns `scenes[sceneOrder[0]]`. This is a real bug fix, not just a feature: with the cathedral registered alongside the gang, `!inject <text>` could otherwise reach either scene unpredictably.

### IRC syntax change

```
!inject <description>                  # default scene (the gang)
!inject @<scene-id> <description>      # explicit scene
```

`!summon <place-id>` is unchanged — it now succeeds when the place-scene exists and errors otherwise. It still emits a `KindSummon` event, now scoped to the place-scene rather than the default.

## NPC seed shape

`seed/characters.yaml` gets three new orphan characters: `vicar`, `caretaker`, `cathedral-cat`. They follow the same shape as group characters (id, name, persona, capabilities, blurb). `config.Validate` already allows characters that no group references; no relaxation needed.

A new validation rule: every `Place.NPCs[i]` must reference a known character id. This catches typos in place yaml at boot, not at summon.

## NPC memory persistence

NPCs use `memory.NewEmbedded(llmImpl, 200, memory.WithPersister(vs, npcID, modelID))` exactly like group members. Their `character_memory` rows are keyed by NPC character id. Implications:

- After a restart, NPC episodic memory is restored. Same as group members — this is the L1 contract earning its keep on the NPC layer for free.
- Two cathedrals **across time** share NPC memory rows because the character id is constant. That is intended for the skeleton (there is only ever one cathedral instance). Multi-instance places will need per-instance scoping when they ship.

## Cross-scene isolation

Events do not bleed between scenes. Concretely:

- `world.dispatchInject` routes the inject to one scene; only that scene's `Orchestrate` runs, only its members get a `Perception`, and only its `BroadcastForMemory` records the synthesized outcome.
- `world.handleTick` currently emits one ambient event to `defaultScene()`. Today that's the gang. After this change, the cathedral does not receive ambient ticks. Acceptable for the skeleton — ambient ticks per scene are a separate small item.
- `Memory.Retrieve` is per-character, so even though NPCs and group members share an `EventStore`, their similarity-ranked memories stay separated by character id.

## Open questions resolved

- **Do NPCs hear cross-scene chatter?** No. Scenes are isolated.
- **Does !summon spawn or surface?** It surfaces — the cathedral is always running. !summon's KindSummon event is the "I am here" log entry; it does not allocate.
- **Where do NPC specs live?** `seed/characters.yaml` as orphan characters. No new file or schema.
- **Is leader the vicar?** Yes — first NPC in `cathedral.yaml` is the leader. Reordering reorders leadership.
- **Tick ambient on every scene?** Not in this pass. Tick stays on the default scene only.

## Failure modes

| Scenario | Behavior |
|---|---|
| `seed/places/` is missing | `LoadPlaces` returns empty slice; no place-scenes registered; `!summon` returns "unknown place". Not fatal. |
| A `place.npcs` entry references a missing character | `config.Validate` returns an error before `runCtx` reaches scene construction. Boot aborts. |
| A place yaml has no NPCs | Skip it with a warn log. A leaderless scene cannot orchestrate, so registering it would just sit silent. |
| Place-scene NPC `Hydrate` fails | Same handling as group-member Hydrate today: boot aborts with the wrapped error. |
| `!inject @nonsense-scene <text>` | World returns `"scene not found"`; IRC adapter relays. |
| `!summon nonexistent` | World returns `"unknown place"`; IRC adapter relays. |

## Out of scope for L2 (explicit follow-ups)

These appear at the end of `BACKLOG.md` (or get added to it during execution):

- Runtime `SummonPlace` registration (coordinator-goroutine-owned scene creation).
- Scene idle-out for place-scenes.
- Multiple simultaneous instances of the same place; per-instance NPC memory scoping.
- Ambient tick fan-out to every registered scene.
- Cross-scene perception (e.g. cathedral cat hears the bar across the square).
