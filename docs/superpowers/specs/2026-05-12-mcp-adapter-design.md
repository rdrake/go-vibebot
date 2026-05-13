# MCP tool-call adapter for LLM users

Date: 2026-05-12
Status: Approved — pending implementation plan
Backlog item: L3

## Problem

The README claims "WorldAPI is the only surface adapters need" and that adapters live at the edges. Today there is one adapter: `internal/irc/adapter.go`. The architectural claim is unfalsified — one adapter is not a pattern. Worse, the only way for an LLM (Claude Desktop, Claude Code, Cursor, etc.) to drive the simulation is to act as an IRC client, which it cannot do.

`internal/api.WorldAPI` exposes `InjectEvent / Summon / Nudge / Where / Log / Who / Describe`. The IRC adapter consumes a subset (`InjectEvent`, `Summon`, `Nudge`, `Log`). Nothing else wires up a tool-call surface.

## Goal

Ship a second adapter — an MCP (Model Context Protocol) server that exposes the same world to any MCP-speaking LLM client over stdio. Any user can add `go-vibebot --mcp-stdio` to a Claude Desktop / Claude Code / Cursor MCP config, point their LLM at the running world, and have it issue `inject`, `nudge`, `summon`, and `log` as tool calls, plus read `world://characters`, `world://places`, and `world://log` as resources.

The architectural payoff: two very different adapters (line-based IRC vs JSON-RPC over stdio) consuming the same `WorldAPI` proves the surface is the right boundary.

## Non-goals

- **HTTP+SSE transport.** Stdio only. Same-machine, one client per process. HTTP+SSE is a clean follow-up that touches no other code.
- **`recap` tool.** Defers to backlog S2 (`!recap [character]`) — the recap surface does not exist on WorldAPI yet, and inventing it inside the MCP adapter would duplicate work S2 will do correctly.
- **`where` / `who` / `describe` as MCP tools.** Reads suitable for an LLM are already covered by the `world://characters`, `world://places`, and `world://log` resources. Exposing reads as both tools and resources doubles the surface for no consumer.
- **MCP prompts.** Templated prompts a client can fetch. Not needed to prove the adapter pattern; a clean future addition.
- **Authentication / authorization.** Stdio is local and 1:1 with the spawning client. Trust the caller.
- **Multiple concurrent MCP clients.** Stdio is 1:1 by nature; HTTP+SSE would be the right place to add fan-in later.
- **Live BACKLOG-style "deferred features" carve-outs beyond the above.** This adapter is small enough to ship in one pass; we are not splitting it.

## Architecture

The MCP adapter is a Go package `internal/mcp/` that wraps an `api.WorldAPI` and exposes it as an MCP server via the official Go SDK (`github.com/modelcontextprotocol/go-sdk`, v1.6.0+). The adapter is parallel in shape to `internal/irc/adapter.go`: a `Config`, a `New(cfg, api) (*Adapter, error)`, a `Run(ctx) error` that blocks until ctx is cancelled. The sim binary (`cmd/sim/main.go`) gains a `-mcp-stdio` flag that, when set, runs this adapter instead of the IRC adapter.

```text
                  ┌─────────────────────┐
   stdin/stdout──>│  internal/mcp       │──> api.WorldAPI ──> world coordinator
                  │  (Adapter+Server)   │
                  └─────────────────────┘
                       ▲          ▲
                  tools/call    resources/read
```

Tools map 1:1 to write verbs on WorldAPI; resources map to reads. The adapter never touches the world coordinator directly — `api.WorldAPI` is the only surface.

### SDK / transport

**SDK: `github.com/modelcontextprotocol/go-sdk` v1.6.0.** Official, maintained in collaboration with Google, has the surface this adapter needs:

- `mcp.NewServer(&mcp.Implementation{Name, Version}, nil)` for the server handle.
- `mcp.AddTool[In, Out](server, &mcp.Tool{Name, Description}, handlerFn)` for typed tools — JSON Schema is inferred from the `In`/`Out` Go types, including struct tags `json:"…"` and `jsonschema:"…"`. Input validation runs before the handler. Errors get packed into `CallToolResult.IsError + Content` via `(*CallToolResult).SetError`, which is what MCP clients expect so an LLM can self-correct.
- `server.AddResource(&mcp.Resource{URI, …}, handlerFn)` for static URIs (`world://characters`, `world://places`).
- `server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate, …}, handlerFn)` for parameterized URIs (`world://log{?since,scene}`).
- `server.Run(ctx, &mcp.StdioTransport{})` to serve over stdin/stdout.
- `mcp.NewInMemoryTransports()` for in-process e2e tests — server and client share two paired transports.

Rationale for not hand-rolling: JSON-RPC over stdio is ~200 LOC, but JSON Schema generation, URI-template parsing, and the typed input/output validation behavior that ToolHandlerFor gives free of charge would push it closer to 1000 LOC. We get all of that, and Anthropic-maintained protocol-level fidelity, by depending on the SDK. The IRC adapter already pulls `lrstanley/girc`; depending on an MCP SDK is the analogous decision.

**Transport: stdio (`mcp.StdioTransport{}`).** Works out of the box with Claude Desktop / Claude Code / Cursor MCP configs that name a binary plus args. HTTP+SSE is a separable follow-up — the same `internal/mcp.Adapter` will accept a different transport without changing tools or resources.

### Stdio hygiene

When `-mcp-stdio` is set, stdout is reserved for JSON-RPC frames. Two consequences:

- The IRC adapter must not be started in MCP mode (it does not write to stdout, but skipping it removes a moving part).
- The sim's `slog.NewTextHandler` already writes to `os.Stderr` (`cmd/sim/main.go:31`). Keep it that way. No change needed but it is load-bearing: any direct `fmt.Println` / stdout writes anywhere on the boot path would corrupt the channel. Pre-flight in the plan greps for stdout writes.

### Tool surface

Four tools, exact 1:1 with the verbs the IRC adapter forwards today. Names match the IRC verb spelling so users translating from IRC have no new vocabulary to learn.

| Tool      | Input fields                                          | WorldAPI call                                   |
|-----------|-------------------------------------------------------|-------------------------------------------------|
| `inject`  | `scene_id?` string, `target?` string, `description` string | `InjectEvent(ctx, SceneID(scene_id), target, description)` |
| `nudge`   | `character_id` string                                 | `Nudge(ctx, CharacterID(character_id))`         |
| `summon`  | `place_id` string                                     | `Summon(ctx, PlaceID(place_id))`                |
| `log`     | `since?` string (Go duration; default `"1h"`), `scene_id?` string | `Log(ctx, dur)` then optional scene filter on the result |

Input/output types are Go structs in `internal/mcp/tools.go`; the SDK infers their JSON Schema. Required vs optional is expressed via the `jsonschema` struct tag (`jsonschema:"the scene to target,optional"`).

**Tool errors.** WorldAPI errors (e.g., `summon: unknown place "void"`) are surfaced as MCP *tool* errors (`CallToolResult.IsError = true` with the error text in `Content`), not JSON-RPC protocol errors. This is the SDK-recommended pattern: protocol errors imply "the request was malformed"; tool errors imply "the request was well-formed but the action failed", which is the right shape for an LLM to read and adjust.

**Tool output.** `inject`, `nudge`, `summon` return a tiny success result (`{"ok": true, "message": "injected."}` etc.) — the IRC adapter's reply strings, but in a structured shape an LLM can parse without prose-extraction. `log` returns `{"entries": [LogEntry, …]}` with each entry shaped `{timestamp, scene_id, actor, kind, text}`. `ToolHandlerFor[In, Out]` auto-populates `Content` with the JSON marshalling so a text-only MCP client still sees the data.

### Resource surface

Three resources. All return `application/json`. None take any path-dependent state; the data comes from WorldAPI reads at request time. All JSON keys are lowercase / snake_case so LLM consumers can rely on the contract — `api.CharacterRef` and `api.PlaceRef` carry explicit `json:"..."` tags (added in the implementation plan) rather than letting Go's default capitalised field names leak through.

| URI                              | Type      | Contents                                              |
|----------------------------------|-----------|-------------------------------------------------------|
| `world://characters`             | static    | `[{id, name, blurb}, …]` — every character in the world |
| `world://places`                 | static    | `[{id, scene_id, leader, members: [{id, name, blurb}, …]}, …]` — every place currently registered |
| `world://log{?since,scene}`      | template  | `{since, entries: [LogEntry, …]}` — same shape as the `log` tool |

`world://log` is a `ResourceTemplate` with URI template `world://log{?since,scene}` (RFC 6570 query expansion). The handler receives the resolved URI in `request.Params.URI`, parses the `since` and `scene` query params, and calls `WorldAPI.Log(ctx, dur)`. Missing `since` defaults to `"1h"`. Missing `scene` returns all scenes. Unparseable `since` returns an error (the resource read fails — clients should retry with a valid duration).

### WorldAPI surface change

The MCP adapter needs to enumerate characters and places. Today `WorldAPI` exposes `Who(sceneID)` (members of one scene) but not "all characters" or "all places." Rather than have the MCP adapter reach around WorldAPI into the sim's loaded yaml, this spec adds two new read methods:

```go
// internal/api/api.go
type WorldAPI interface {
    // ... existing methods ...
    Characters(ctx context.Context) ([]CharacterRef, error)
    Places(ctx context.Context) ([]PlaceRef, error)
}

// PlaceRef is a lightweight handle to a registered place.
type PlaceRef struct {
    ID      PlaceID
    SceneID SceneID
    Leader  CharacterID
    Members []CharacterRef
}
```

Reasoning:

- "WorldAPI is the only surface adapters need" must stay true. Adding the methods is the right shape; smuggling character/place data through a side channel into the MCP adapter is the wrong shape.
- `Characters` is a coordinator-goroutine query (matches the existing `whereReq` / `whoReq` pattern). The world already has `characters map[CharacterID]*character.Character`; the handler iterates it and returns `CharacterRef`s.
- `Places` is also a coordinator-goroutine query, but the world does not currently track places explicitly — it tracks scenes, some of which have a non-empty `PlaceID`. The handler iterates `sceneOrder`, picks scenes with `PlaceID != ""`, and emits a `PlaceRef` per scene. No new state.
- The IRC adapter does not need either method today; adding them does not change IRC.
- Cost is ~80 LOC across `api/api.go`, `world/api.go`, `world/reads.go`, `world/world.go` — mechanical, isomorphic to the existing patterns.

### Why not snapshot at adapter construction

An alternative is to pass `[]config.CharacterSpec` and `[]config.PlaceSpec` into `mcp.New` at boot, dodging the WorldAPI change. Rejected because:

- It weakens the architectural invariant the README sells.
- It goes stale if the L2 follow-up "runtime scene registration" ever lands. With WorldAPI methods backed by the coordinator, runtime additions show up automatically.
- It forces the MCP adapter to know which scene a character is in, work the world already does.

### Why one binary, not `cmd/mcp-server/`

The BACKLOG plan asked whether the MCP adapter should run alongside IRC in the same binary. Yes:

- All the bootstrap — store open, yaml load, character/scene construction, world.New, Hydrate — is identical. Duplicating it in a new `cmd/mcp-server/main.go` either copies 200 LOC or forces a refactor of `cmd/sim` into a library, both worse than a flag.
- The `-mcp-stdio` flag is one branch in `runCtx`: when set, skip IRC, start the MCP adapter. When unset, behave as today. Mode is mutually exclusive with IRC (stdio reserves stdout), which the spec makes explicit at the flag layer.
- Operationally, "the binary that runs vibebot" stays one thing. Claude Desktop / Cursor MCP configs invoke `go-vibebot --mcp-stdio --seed …` and that is the whole story.

### Concurrent run with IRC

If both `-irc-server` and `-mcp-stdio` are set, we error at startup. Reason: stdio MCP needs exclusive stdout (the protocol depends on it). The current IRC adapter writes only to stderr via slog, but mixing modes is a footgun worth refusing. Plan task gates them with a check in `runCtx`.

### Logging

The MCP adapter takes a `*slog.Logger` in its `Config`, same as IRC. It logs to stderr only — the SDK does not log; we add structured events for tool invocations and errors. The plan locks an `slog.New(slog.NewTextHandler(os.Stderr, …))` invariant in pre-flight.

### Testing strategy

Three layers, matching the L1/L2 pattern:

1. **Tool handler unit tests** (`internal/mcp/tools_test.go`). Each tool's handler is a pure function taking `(ctx, *CallToolRequest, In) → (*CallToolResult, Out, error)`. Drive with a fake `api.WorldAPI` that records the call and returns canned errors / data. Assert the handler maps WorldAPI errors to `IsError` results.
2. **Resource handler unit tests** (`internal/mcp/resources_test.go`). Same shape: fake WorldAPI, assert resolved URI parsing and JSON shape of `ResourceContents.Text`.
3. **Protocol-level e2e** (`internal/mcp/adapter_test.go`). Use `mcp.NewInMemoryTransports()`. Build a real `internal/mcp.Adapter` against a fake WorldAPI; build an MCP client over the paired transport; call `session.CallTool(... "inject" ...)` and assert (a) the fake WorldAPI saw the right `InjectEvent` call and (b) the client got a non-error `CallToolResult`. Repeat for `summon` error → assert `IsError` is set and `Content` contains the error text. One end-to-end test per tool path is enough — we trust the SDK's wire protocol.

E2e against a real world coordinator (via `cmd/sim` smoke test) is deferred — too much wiring vs the value it adds. The plan calls it out as a "stretch" with a placeholder Skip, parallel to the way L1's round-trip integration test is structured.

### Why MCP names match IRC names

`!inject`, `!nudge`, `!summon`, `!log` → `inject`, `nudge`, `summon`, `log`. Same spelling, same semantics. A user who knows the IRC verbs already knows the MCP tool names; the README's adapter notes can point to one verb glossary instead of two.

## Resolved design questions

- **Should `log` be a tool, a resource, or both?** Both. Tool because the IRC adapter has it as a verb (consistency); resource because some MCP clients prefer browsing reads as resources. Cost is one extra handler; payoff is matching how each client expects to consume.
- **Should `inject`'s `target` be optional?** Yes. The WorldAPI signature is `InjectEvent(ctx, sceneID, target, description)` — target is already free-form ("" for an ambient event). Match it.
- **What MCP server name / version?** `Name: "go-vibebot"`, `Version: "v0"`. Bump the version when the tool surface changes incompatibly. Until then, v0 signals "skeleton, not stable."
- **Should we register one tool per WorldAPI verb (i.e., also `where`, `who`, `describe`) for symmetry?** No. The resources cover those reads in a shape MCP clients prefer (browseable URIs). Exposing identical surfaces as both tools and resources doubles the schema without adding any consumer.
- **Should the MCP adapter own its server lifecycle, or accept an `*mcp.Server` from outside?** Own it. The `internal/mcp.Adapter` constructs the `*mcp.Server` in `New`, registers tools and resources inline, and `Run` calls `server.Run(ctx, &mcp.StdioTransport{})`. Symmetric with `irc.New` constructing a `*girc.Client` internally.
- **Where do new WorldAPI methods live in `world/`?** `Characters` and `Places` go in `internal/world/reads.go` (matches `Where`, `Who`). New request types `charactersReq` and `placesReq`. New `chan` fields on `World`. New `case` arms in the `Run` select.
- **How does the log resource template syntax work?** RFC 6570: `world://log{?since,scene}` means "either `world://log`, or `world://log?since=2h`, or `world://log?scene=cathedral`, or both query params." The handler parses the resolved URI's query string with `net/url.Parse`. Missing param uses default.
