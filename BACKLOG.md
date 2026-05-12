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
| Place instantiation with NPCs | Open. See plan below. |
| Rolling per-character summaries | Open. Small. See below. |
| `!recap [character]` | Open. Small. See below. |
| Tool-call adapter for LLM users | Open. See plan below. |
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

### L2. Place instantiation into Scenes (cathedral case)

**Why now**: Places are seeded but inert. Wiring them into Scenes is the first time the Place layer earns its keep, and the cathedral case (vicar, caretaker, cat) is small enough to be a good shakedown.

**Current state**:
- `internal/place/place.go` defines `Place{ID, Name, Description, NPCs []string}`. Pure data.
- `seed/places/*.yaml` likely exists — `internal/config/` would load them (verify).
- `scene.Scene` has `PlaceID api.PlaceID` already plumbed but unused.
- `!summon <place-id>` exists in `internal/irc/adapter.go` as scaffold (`NewSummonEvent`); it doesn't actually instantiate anything yet.

**Design call**:
- A Place's NPCs are character specs that get spun up only when the place is summoned into a scene. They share the same `character.Character` shape as group members.
- On `!summon`, the world coordinator should: (a) look up the place, (b) instantiate any NPC characters not already running (Memory, Inbox, decide goroutine), (c) attach them to an appropriate Scene (or create a per-place transient Scene). Keeping the "per-place transient Scene" model preserves the "scenes are the orchestration unit" rule from the README.
- NPC seeds live alongside characters; the difference is they're not in any group, only in a place's `NPCs []string`.

**Plan**:
1. Confirm `internal/config` can load places (likely already wired; verify and document).
2. Add `world.SummonPlace(ctx, placeID)`: creates a transient scene from the place's NPCs if one isn't running, registers it, starts the goroutines.
3. Wire `WorldAPI.Summon` (already exists per IRC adapter) to call into the new method.
4. NPC `Memory`: use `Embedded` like normal characters. With L1 done, NPC memory also persists.
5. Decide scene lifecycle: do place-scenes idle out after some period? Skeleton answer: keep them alive until shutdown. Mark idle-out as follow-up.

**Files**: `internal/world/`, `internal/scene/`, `internal/config/`, `cmd/sim/main.go`, `seed/places/cathedral.yaml` (if missing).

**Test strategy**: extend the existing smoke test — summon a place, inject an event scoped to it, confirm an NPC speaks. Tests live in `cmd/sim/`.

**Open question to settle in the session**: do summoned NPCs receive events from *other* scenes (e.g., cathedral cat hears bar gossip)? Skeleton answer: no — events stay scoped to their scene. The "shared memory across scenes" idea is a much later phase.

---

### L3. Tool-call adapter for LLM users

**Why now**: This is the second adapter that proves the architecture (IRC adapter was the first). It also unlocks running vibebot characters as MCP tools or as targets for other LLM systems, which is the design payoff promised by the README's "Adapter pattern at the edges."

**Current state**: `internal/api.WorldAPI` is the only surface adapters need. `internal/irc/adapter.go` is the reference adapter — translates IRC commands to WorldAPI calls and emits results back. Nothing wires up a tool-call surface yet.

**Design call**:
- Build as an MCP server (Model Context Protocol) so any LLM client speaking MCP can drive the world. Standard, mature, language-agnostic. Alternative would be a raw JSON-RPC or HTTP adapter — but MCP gives you Claude Desktop / Claude Code / Cursor / etc. for free.
- Use Anthropic's Go MCP SDK if it exists by then; otherwise the JSON-RPC over stdio is simple to hand-roll.
- Surface the same verbs as IRC: `inject`, `log`, `nudge`, `summon`. Plus probably a `recap` once L4 lands.
- Resources (not just tools): expose `world://log?since=1h`, `world://characters`, `world://places` as MCP resources. Read-only.

**Plan**:
1. Choose MCP stdio vs HTTP+SSE transport. Start with stdio — simplest, works with Claude Desktop directly.
2. New package `internal/mcp/adapter.go`: takes `api.WorldAPI`, registers tools that map 1:1 to WorldAPI calls.
3. New `cmd/mcp-server/main.go` entrypoint that wires it up identical to `cmd/sim` but instead of (or alongside) IRC, runs the MCP server.
4. Document in README.
5. Test by adding the binary to Claude Desktop's MCP config and exercising verbs.

**Files**: `internal/mcp/` (new), `cmd/mcp-server/` (new), README.

**Test strategy**: protocol-level unit tests against the MCP message contract; manual e2e via a real MCP client.

**Open question**: should the MCP adapter and IRC adapter run in the same binary? Probably yes — pass `--mcp-stdio` flag to enable. Saves boot duplication.

---

## Smaller items (1-2 paragraph scope)

### S1. Leader synthesis pulls from memory

Natural follow-up to the memory work that just landed. `internal/scene/leader.go`'s synthesize step renders `"Situation: " + prompt + "\n\nReactions:\n" + replies` without any historical context. The leader's *own* memory is the right thing to retrieve here — three-ish similar past events from `s.Leader.Memory.Retrieve(ctx, prompt, 3)` prepended as a "Group's recent history:" block. Same pattern as `character.recallContext`. Watch token budget: synthesis prompt + reactions + memory could get long; consider trimming to 2 events if needed. **Files**: `internal/scene/leader.go` only.

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
