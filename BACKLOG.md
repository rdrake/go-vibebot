# Backlog

Curated work list for go-vibebot. Items toward the top are higher leverage / more shovel-ready. Each "large" item has enough plan to start a cold session; small items are one-paragraph scope notes.

Generated 2026-05-12 after sessions covering: IRC `!log` multiline fix, Gemini OpenAI-compat probe, and embedding-backed memory retrieval.

---

## Status of the README's "Deliberately deferred" list

The README still lists these as deferred. Reality as of this writing:

| README item | Actual status |
|---|---|
| Capability-tag pre-filter + LLM router | **Already implemented** in `internal/scene/router.go`, `llm_router.go`, `prefilter.go`. The skeleton's `LLMRouter{PreFilterK: 0, MaxConsult: 0}` wiring is what makes it act like fan-out-to-all. See "Tune router defaults" below. |
| Real LLM providers (OpenAI, Anthropic) | Gemini is in (`internal/llm/gemini`). Open. |
| Memory retrieval with embeddings | **Done this session** (`internal/memory/embedded.go`). |
| Place instantiation with NPCs | **Done 2026-05-12** (cathedral case). See L2 below for follow-ups. |
| Rolling per-character summaries | Open. Small. See below. |
| `!recap [character]` | Open. Small. See below. |
| Tool-call adapter for LLM users | **Shipped 2026-05-12.** See L3 below. |
| Multi-user auth / web UI / distributed ops / prod rate limiting | Out of scope for this milestone. |

A doc pass on README to retire the stale entries is its own small task (see "Smaller items").

---

## Larger items (with plans)

### ~~L1. Persist embeddings across restarts~~ — SHIPPED 2026-05-12

Landed on `main` as commits `e1bedc2..2280a38` (+ lint cleanup `783cc5c`). Spec at `docs/superpowers/specs/2026-05-12-persistent-embeddings-design.md`, plan at `docs/superpowers/plans/2026-05-12-persistent-embeddings.md`.

What shipped:
- `character_memory` SQLite table with composite PK and descending index, schema-bootstrapped by `OpenSQLite`.
- `internal/store/SQLiteVectorStore` (`Save` / `Load`) sharing the host `*sql.DB`.
- Little-endian `float32` blob codec in `internal/store/vector_codec.go`.
- `memory.VectorStore` / `EmbeddingRow` / `EventLookup` interfaces in `internal/memory/persister.go` plus `NewSQLiteVectorStoreAdapter` adapter.
- `Embedded.Hydrate(ctx, EventLookup)` and `Record` save-on-write; `WithPersister(vs, owner, modelID)` option.
- Per-provider `EmbeddingModelID` constants (`gemini:text-embedding-004`, `echo:none`); `selectLLM` returns the ID alongside the LLM.
- `cmd/sim/main.go` split into `run` + `runCtx` so tests can drive the boot path; every character is `Hydrate`d before `w.Run` launches; Hydrate errors abort boot.
- Test coverage: blob round-trip + NaN, `LookupByIDs`, vector store ordering / model filter / unbounded limit, Hydrate populates / empty-DB / replace-on-second-call, Record persist / save-failure / nil-embedding skip / zero-ID skip, `runCtx` aborts on Hydrate failure.

Deferred follow-ups (open):
- **Round-trip integration test in `cmd/sim`** — currently stubbed: `TestRunCtxPersistsAndHydratesRoundTrip` in `cmd/sim/smoke_test.go` calls `t.Skip("TODO: round-trip integration test (see plan Step 8.5)")`. Unit-level coverage already proves Record persists and Hydrate loads; this would only confirm they meet through the `cmd/sim` wiring. Either fill it in (copy `TestSmokeEndToEnd`'s inlined-world setup, add `WithPersister`+`Hydrate`, inject one event, kill, reopen, assert `len(mem.entries) > 0`) or delete the stub.
- **Model migration runbook in README** — one paragraph: "to retire an embedding model, run `DELETE FROM character_memory WHERE model_id = ?`. Stale rows are otherwise filtered at hydrate time but consume disk."
- **Per-character `Hydrate` timeout** — `runCtx` currently blocks boot indefinitely on a stuck `VectorStore.Load`. Wrap each `mem.Hydrate(ctx, st)` call in a per-character timeout (e.g., 5s) so a slow backend fails one character rather than the whole boot.

---

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

---

### ~~L3. Tool-call adapter for LLM users~~ — SHIPPED 2026-05-12

Plan at `docs/superpowers/plans/2026-05-12-mcp-adapter.md`; spec at `docs/superpowers/specs/2026-05-12-mcp-adapter-design.md`.

What shipped:
- `internal/mcp` package: `Adapter`, `New(cfg, api.WorldAPI)`, `Run(ctx, mcpsdk.Transport)`.
- Tools: `inject`, `nudge`, `summon`, `log` mapped 1:1 to WorldAPI verbs; world errors surface as `IsError` tool-level results.
- Resources: `world://characters`, `world://places`, and the `world://log{?since,scene}` template (JSON, defaults `since=1h`).
- `WorldAPI.Characters` / `WorldAPI.Places` added (plus `api.PlaceRef`); coordinator queries match the existing `whereReq` / `whoReq` shape.
- `cmd/sim` flag `--mcp-stdio` runs the adapter over `mcp.StdioTransport{}`; mutually exclusive with `-irc-server`.
- Test coverage: per-tool / per-resource unit tests with a fake WorldAPI; three end-to-end tests via `mcp.NewInMemoryTransports` (inject success, summon error → IsError, characters resource read).

Deferred follow-ups (open):
- **HTTP+SSE transport.** Same `internal/mcp.Adapter` accepts a different `mcp.Transport`; needs a new flag and a server lifecycle wrapper. Out of this milestone.
- **`recap` tool.** Waits for backlog S2 (`!recap [character]`) to land its WorldAPI surface; MCP will register the matching tool afterwards.
- **MCP prompts.** Templated prompts a client can fetch. Not needed yet.
- **Authentication.** Not needed for stdio (1:1 with spawning client). HTTP+SSE will need to revisit.

---

## Smaller items (1-2 paragraph scope)

### ~~S1. Leader synthesis pulls from memory~~ — SHIPPED 2026-05-12

`synthesize` now calls `recallForSynth`, which pulls up to `synthRecallK=3` similar past events from the leader's memory and prepends them as a `"Group's recent history:"` block. The current event ID is filtered out (same pattern as `character.recallContext`). Retrieval failures log-and-continue. Coverage in `internal/scene/leader_test.go`. Drop `synthRecallK` to 2 if MaxTokens (120) starts truncating synthesized output.

### S2. `!recap [character]` command

In-character narrative summary over recent events. Without a character arg, the bot speaks as a narrator over the scene's events. With an arg (e.g. `!recap stinky-sam`), it speaks as that character recapping from their POV using their `Memory.Summary()` plus retrieval. Implementation: another `cmdRecap` in `internal/irc/adapter.go` that calls a new `WorldAPI.Recap(ctx, characterID, dur)` → returns a string already rendered by the LLM. Smaller than it sounds because the plumbing is identical to `!log`.

### S3. Rolling per-character summaries every N events

`Character` accumulates events; once memory gets long, retrieved snippets stop covering "who is this character now." A rolling summary — every 25 events, ask the LLM to compress the oldest ~25 events into a 200-token narrative paragraph — drops in cleanly. Store as a special event kind `KindSummary` in the character's memory so retrieval can still surface it. The hardest design question: do summaries replace the underlying events in memory, or live alongside? Skeleton answer: live alongside, with cap retention preferring summaries.

### S4. Tune router defaults in main.go

`scene.LLMRouter{Model: llmImpl, PreFilterK: 0, MaxConsult: 0}` in `cmd/sim/main.go:108` and `cmd/sim/smoke_test.go:61` disables the cap-based gating, so the router effectively fans out to everyone. Real defaults probably want `PreFilterK: 4, MaxConsult: 3` — pre-filter to top 4 by tag overlap, ask the LLM to pick at most 3. Tiny change, but smoke-test the new defaults still produce synthesized speech with the seed group.

### S5. IRC fallback flood resilience

The `!log` fix in this session bundles output into one multiline BATCH when the server has `draft/multiline`. On Afternet (which probably doesn't), the fallback in `internal/irc/adapter.go` `sendMessage` sends N PRIVMSGs paced only by girc's built-in rate limiter. That may still trip excess-flood on a long log. Two options: (a) detect the missing cap at handshake and cap `!log` output to ~10 entries in fallback mode, (b) pace PRIVMSGs ourselves with a 600ms gap on the fallback path. (a) is the cheap fix; ship it if a real flood happens.

### S6. Documentation pass on README

The "Deliberately deferred" section overstates what's deferred (see top of this file). Also worth adding: how to run with Gemini (`GEMINI_API_KEY=… --llm=gemini`), and a one-line note on the embedded memory store. Keep brief — README is already on the long side.

### S7. Cleanup: `cmd/probecompat`

Leftover from the Gemini OpenAI-compat investigation. Pure stdlib so it doesn't bloat `go.sum`, but it has no production purpose. Decision: keep it around long enough to re-probe after Google updates the compat layer (3–6 months), then delete. Or move to `tools/probecompat/` if the project adopts that convention.

---

## Recorded decisions (reference, not work)

- **Gemini OpenAI-compat layer is not viable for chat-side Gemini-only knobs.** Empirically confirmed via `cmd/probecompat`: `safety_settings`, `thinking_config`, and `cached_content` are all HTTP 400-rejected on `/v1/chat/completions` (root or inside `extra_body`), even though the docs imply chat support. Only OpenAI-shaped fields (`reasoning_effort`, `tools`, etc.) work for chat. If safety tuning is ever needed for character RP that drifts edgy, drop to the native Gemini REST API for those calls.
- **OpenAI Go SDK swap is deferred.** ~150-180 LOC of Gemini wire-shape code would disappear, but the upside-from-compat-extras story (caching, thinking budget, safety knobs) doesn't materialize today. Revisit if the compat layer expands.
- **Memory store wiring.** `Embedded` is production; `InMem` is for tests that don't want an LLM dependency. Both implement `memory.Store`. `Record` and `Retrieve` both take `ctx` and `Record` returns `error` — embedding failures don't crash a turn.
- **Memory recency defaults.** `λ = 0.3`, `τ = 1h`. `τ` matches the default `!log` window so the two surfaces feel aligned.

---

## How to use this file

In a fresh session, point Claude at this file with something like:

> "Read BACKLOG.md and let's tackle L2 (Place instantiation). Brief me on what you'd touch first."

Each large item has enough scaffolding for a cold-start plan; small items are short enough to inline-quote. Mark items complete by striking through here or moving to a CHANGELOG.
