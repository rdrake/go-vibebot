# go-vibebot

Multi-agent character simulation in Go. LLM-driven characters with distinct
personae form groups and react to externally-injected scenarios in a
persistent world. External users initially interact via IRC; future passes
add LLM tool-call access.

This repository is at the **walking skeleton** stage: end-to-end wiring is
in place, the LLM is a local echo provider, and routing/memory/places are
deliberately minimal.

## Architecture

```
        IRC users                                Future: LLM users
            │                                            │
       irc adapter                              tool adapter (future)
            │                                            │
            └────────────────┬───────────────────────────┘
                             │ (WorldAPI calls)
                             ▼
                      ┌─────────────┐
                      │  WorldAPI   │
                      └──────┬──────┘
                             │ writes → world.IRCEvents chan
                             │ reads  → store + state cache
                             ▼
                  ┌──────────────────────┐
                  │  World Coordinator   │  (single goroutine, owns state)
                  │  select over:        │
                  │   - IRCEvents chan   │
                  │   - GroupActions chan│
                  │   - Ticker           │
                  └──────┬───────────────┘
                         │ dispatches scenarios to scenes
                         ▼
              ┌────────────────────────────┐
              │  Scene goroutine           │
              │  (one per active scene)    │
              │  - holds members           │
              │  - runs leader.orchestrate │
              │  - fan-out + synthesize    │
              └──────┬─────────────────────┘
                     │ perception messages
                     ▼
              ┌──────────────────────┐
              │  Character goroutine │ (one per active character)
              │  - inbox <- chan     │
              │  - decide() via LLM  │
              └──────────────────────┘

   SQLite event store sits beside the coordinator. Every dispatched event
   is appended BEFORE it is broadcast — if the append fails, it didn't
   happen.
```

## Package layout

```
cmd/sim/                main entrypoint, wiring, echo LLM provider
internal/world/         coordinator goroutine, event dispatch, ticker
internal/scene/         scene lifecycle, leader fan-out + synthesize
internal/character/     character struct, decide loop
internal/place/         place definitions (NPCs land in phase 2)
internal/memory/        per-character memory store + retrieval
internal/llm/           LLM interface (pure — providers live elsewhere)
internal/store/         SQLite event log + filter queries
internal/api/           WorldAPI: the core read/write surface
internal/irc/           IRC adapter over WorldAPI
internal/config/        YAML loading for characters, groups, places
seed/                   characters.yaml, groups.yaml, places/*.yaml
```

Design principles, in priority order:

1. **Single-owner state.** All mutable world state belongs to one
   coordinator goroutine. Other goroutines communicate via channels.
2. **Event sourcing.** Every world change is an append-only SQLite event.
   Current state is derivable. Free adventure logs, replay debugging.
3. **Adapter pattern at the edges.** Core `WorldAPI` is the only surface
   adapters touch. IRC, tool calls, CLI: all are thin adapters.

   The MCP adapter is the second edge. Run `go-vibebot --mcp-stdio --seed ./seed` and any
   MCP-speaking client (Claude Desktop, Claude Code, Cursor) can issue `inject`,
   `nudge`, `summon`, and `log` as tool calls, and read `world://characters`,
   `world://places`, and `world://log` as resources. The MCP and IRC adapters are
   mutually exclusive in a single process — stdio reserves stdout for JSON-RPC.
4. **Scenes orchestrate, not groups.** Groups compose with Places into
   Scenes; the scene is the orchestration unit.
5. **Selective perception, no belief modeling.** Memory tracks what was
   witnessed, nothing more.
6. **Capability-based routing** (phase 2). Tag pre-filter then LLM router.
7. **Idiomatic Go.** stdlib first; small interfaces; errors as values;
   `context.Context` everywhere; no reflection-heavy magic.

## Running

```sh
go build ./...
go test ./...

# Run with the default config file, vibebot.yaml:
go run ./cmd/sim

# Run with another config file:
go run ./cmd/sim --config path/to/vibebot.yaml

# Run without IRC (ticker-only, no inbound):
go run ./cmd/sim --irc-server ''

# Run with IRC:
go run ./cmd/sim \
    --tick 30s \
    --irc-server irc.example.net --irc-port 6667 \
    --irc-nick vibebot --irc-channel '#vibebot'

# Run with xAI/Grok for generation:
XAI_API_KEY=... go run ./cmd/sim \
    --llm xai --xai-model grok-4-1-fast-reasoning
```

The binary auto-loads `vibebot.yaml` from the working directory when that
file exists. Flags override config-file values, so one-off runs can change
only the needed setting. `--llm xai` uses xAI for character generation; if a
Gemini key is also configured, Gemini is still used for memory embeddings.

IRC commands once joined:

- `!inject <description>` — injects a scenario; the group reacts.
- `!summon <place-id>` — summon a pre-loaded place (`seed/places/*.yaml`).
- `!summon <place-id> n=<id1>,<id2>,... <description>` — register a new ad-hoc place-scene at runtime using existing characters. The first id is the scene leader. Description is recorded as an inject scoped to the new scene. Ad-hoc places live only for the binary's lifetime.
- `!nudge <character-id>` — nudge a character to speak.
- `!log [duration]` — dumps events in the last `duration` (e.g. `15m`,
  `2h`). Default: `1h`.
- `!snapshot` — dump a summary of current characters and places.

The default DB is `vibebot.db` in the working directory; pass `--db
:memory:` for ephemeral runs.

## Deliberately deferred

These are intentionally out of scope for the walking skeleton and land in
later passes:

- Capability-tag pre-filter + LLM-based router; the skeleton fans out to
  every member.
- `Place` instantiation into Scenes with NPCs (cathedral case: vicar,
  caretaker, cat).
- Memory retrieval with embeddings and recency-boosted scoring
  (`score = sim + λ·exp(-age)`); the skeleton stores recent events and
  retrieves the last *k*.
- Rolling per-character summaries every N events.
- `!recap [character]` in-character narration query.
- Real LLM providers (OpenAI, Anthropic). The skeleton ships only an echo
  provider in `cmd/sim`; the `internal/llm` package is interface-only by
  design.
- Multi-user auth, web UI, distributed operation, production rate
  limiting.

## Tests

```sh
go test ./...
```

Test coverage for this pass:

- `internal/store` — append/query roundtrip, since-filter, scene-filter.
- `internal/world` — inject dispatch end-to-end with a mock LLM:
  append-before-broadcast invariant, members consulted, synthesized event
  produced.
