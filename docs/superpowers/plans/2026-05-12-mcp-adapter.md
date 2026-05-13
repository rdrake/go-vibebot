# MCP Adapter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a second adapter that drives `api.WorldAPI` over the Model Context Protocol via stdio, so any MCP client (Claude Desktop / Claude Code / Cursor) can issue `inject`, `nudge`, `summon`, and `log` tool calls and read `world://characters`, `world://places`, and `world://log` resources against a running `cmd/sim`.

**Architecture:** New package `internal/mcp/` parallel to `internal/irc/`. Uses the official Go SDK `github.com/modelcontextprotocol/go-sdk` to build a server with typed tool handlers (`mcp.AddTool[In, Out]`) and resource handlers. Runs over `mcp.StdioTransport{}`. `cmd/sim/main.go` gains a `-mcp-stdio` flag that swaps the IRC adapter for the MCP adapter (mutually exclusive — stdio reserves stdout). `api.WorldAPI` gains `Characters` and `Places` read methods so the MCP adapter consumes only the WorldAPI surface, no side channels.

**Tech Stack:** Go 1.26, `github.com/modelcontextprotocol/go-sdk` v1.6.0+. No other new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-12-mcp-adapter-design.md`

---

## File Structure

| Path | Status | Responsibility |
|---|---|---|
| `go.mod`, `go.sum` | modify | Add `github.com/modelcontextprotocol/go-sdk` dependency |
| `internal/api/api.go` | modify | Add `Characters` and `Places` to `WorldAPI`; add `PlaceRef` type |
| `internal/world/reads.go` | modify | Add `charactersReq` / `placesReq` request types, `Characters` / `Places` methods, `lookupCharacters` / `lookupPlaces` helpers |
| `internal/world/world.go` | modify | Add `charactersReq` / `placesReq` channels; new `case` arms in `Run` select |
| `internal/world/api.go` | modify | Implement `Characters` and `Places` on `apiImpl` |
| `internal/world/world_test.go` | modify | Tests for `Characters` and `Places` |
| `internal/mcp/adapter.go` | create | `Config`, `Adapter`, `New`, `Run`; wires SDK server + stdio transport |
| `internal/mcp/tools.go` | create | Input/output structs and `ToolHandlerFor` implementations for `inject`, `nudge`, `summon`, `log` |
| `internal/mcp/resources.go` | create | Resource handlers for `world://characters`, `world://places`, `world://log{?since,scene}` |
| `internal/mcp/tools_test.go` | create | Per-tool unit tests against a fake `api.WorldAPI` |
| `internal/mcp/resources_test.go` | create | Per-resource unit tests against a fake `api.WorldAPI` |
| `internal/mcp/adapter_test.go` | create | End-to-end protocol test via `mcp.NewInMemoryTransports` |
| `internal/mcp/fake_world_test.go` | create | Shared fake `api.WorldAPI` for the package's tests |
| `cmd/sim/runtime_config.go` | modify | `MCPStdio bool` option + flag binding |
| `cmd/sim/runtime_config_test.go` | modify | Test the new flag |
| `cmd/sim/main.go` | modify | Branch on `MCPStdio` in `runCtx`; refuse simultaneous IRC + MCP |
| `README.md` | modify | Document MCP mode under "Adapter pattern at the edges" |
| `BACKLOG.md` | modify | Strike L3 as shipped; record follow-ups |

---

## Pre-flight verification

- [ ] **Step 0.1: Confirm `internal/mcp` does not exist yet**

Run:
```bash
ls internal/mcp 2>/dev/null; echo "exit=$?"
```
Expected: `exit=2` (directory does not exist). If it exists, stop and read whatever is there before continuing.

- [ ] **Step 0.2: Confirm WorldAPI signature is L2-post**

Run:
```bash
grep -n "InjectEvent\|Summon\|Nudge\|Where\|Log\|Who\|Describe" internal/api/api.go
```

Expected: `InjectEvent(ctx context.Context, sceneID SceneID, target, description string) error`. If sceneID is missing, the L2 plan was not actually shipped and this plan is out of date.

- [ ] **Step 0.3: Confirm no stray stdout writes on the boot path**

Run:
```bash
grep -rn 'fmt\.Println\|os\.Stdout\|fmt\.Fprintln(os\.Stdout' \
  cmd/sim/ internal/world/ internal/api/ internal/scene/ \
  internal/character/ internal/memory/ internal/store/ internal/llm/ internal/config/ \
  --include='*.go' | grep -v '_test\.go'
```

Expected: **exactly one hit** — `cmd/sim/main.go:36: printRuntimeUsage(os.Stdout)`. This is the `flag.ErrHelp` path that runs *before* the transport ever starts, so it does not corrupt MCP stdio. Any other hits are bugs; investigate before continuing. (Logger output via slog already targets stderr — `cmd/sim/main.go:31`.)

Task 12b also changes that call to write to `os.Stderr` for defence-in-depth, so the help-path footgun goes away once the plan ships.

- [ ] **Step 0.4: Confirm Go module version supports the SDK**

Run:
```bash
head -1 go.mod
```
Expected: `module github.com/afternet/go-vibebot`. Then:
```bash
grep '^go ' go.mod
```
Expected: `go 1.26.2` (or any 1.23+). The MCP SDK requires Go 1.23+.

- [ ] **Step 0.5: Confirm coordinator channel pattern**

Run:
```bash
grep -n 'whereReq\|whoReq' internal/world/reads.go internal/world/world.go
```

Expected: declarations in `reads.go`, channel fields in `world.go`, two `case req := <-w.whereReq` / `<-w.whoReq` arms in `Run`. The new `charactersReq` / `placesReq` channels mirror this pattern exactly.

---

## Task 1: Add the MCP SDK dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1.1: Fetch the SDK at a pinned version**

Run:
```bash
go get github.com/modelcontextprotocol/go-sdk@v1.6.0
```

Expected: `go get` reports a successful add at v1.6.0. We pin (not `@latest`) so the plan's code snippets match the SDK's documented surface; bump only as a separate dep-update PR.

- [ ] **Step 1.2: Tidy modules**

Run:
```bash
go mod tidy
```

Expected: no errors. The dependency moves from `indirect` to `direct` once we import it in Task 3, but adding it now keeps the dep-bump in its own commit.

- [ ] **Step 1.3: Verify the SDK surface this plan depends on**

Three load-bearing assumptions about the SDK. Confirm each before writing any handler code; if any fails, stop and reconcile with the maintainer rather than improvising.

```bash
go doc github.com/modelcontextprotocol/go-sdk/mcp NewServer
go doc github.com/modelcontextprotocol/go-sdk/mcp AddTool
go doc github.com/modelcontextprotocol/go-sdk/mcp ToolHandlerFor
go doc github.com/modelcontextprotocol/go-sdk/mcp Server.AddResource
go doc github.com/modelcontextprotocol/go-sdk/mcp Server.AddResourceTemplate
go doc github.com/modelcontextprotocol/go-sdk/mcp StdioTransport
go doc github.com/modelcontextprotocol/go-sdk/mcp NewInMemoryTransports
go doc github.com/modelcontextprotocol/go-sdk/mcp ResourceContents
```

Expected output (the load-bearing parts; other lines may differ):

- `func NewServer(impl *Implementation, opts *ServerOptions) *Server`
- `func AddTool[In, Out any](s *Server, t *Tool, h ToolHandlerFor[In, Out])`
- `type ToolHandlerFor[In, Out any] func(_ context.Context, request *CallToolRequest, input In) (result *CallToolResult, output Out, _ error)`
- `func (s *Server) AddResource(r *Resource, h ResourceHandler)`
- `func (s *Server) AddResourceTemplate(t *ResourceTemplate, h ResourceHandler)`
- `type StdioTransport struct{ ... }` (a concrete type)
- `func NewInMemoryTransports() (*InMemoryTransport, *InMemoryTransport)`
- `type ResourceContents struct { URI string ...; MIMEType string ...; Text string ...; ... }`

If `AddTool` and `ToolHandlerFor` do not match the three-return shape `(result *CallToolResult, output Out, _ error)`, the handler signatures in Tasks 4–7 are wrong; halt and consult the SDK source. If `URITemplate` does not appear as a field on `ResourceTemplate`, halt and re-check Task 10's template form.

- [ ] **Step 1.4: Commit**

```bash
git add go.mod go.sum
git commit -m "deps: add modelcontextprotocol/go-sdk for MCP adapter"
```

---

## Task 2: Extend WorldAPI with Characters and Places

**Files:**
- Modify: `internal/api/api.go`
- Modify: `internal/world/reads.go`
- Modify: `internal/world/world.go`
- Modify: `internal/world/api.go`
- Modify: `internal/world/world_test.go`

### Task 2a: Declare PlaceRef, JSON-tag the public refs, extend the interface

- [ ] **Step 2a.1: Add JSON tags to `CharacterRef`**

Edit `internal/api/api.go`. Replace the existing `CharacterRef` struct (around lines 12-16) with:

```go
// CharacterRef is a lightweight handle to a character for read APIs.
// JSON tags are required because adapters serialize these refs directly
// to LLM consumers that parse by lowercase key.
type CharacterRef struct {
	ID    CharacterID `json:"id"`
	Name  string      `json:"name"`
	Blurb string      `json:"blurb"`
}
```

Adding tags does not change any Go callsite (field access is by name, not tag); only JSON marshalling output changes.

- [ ] **Step 2a.2: Add `PlaceRef` and the two new methods to the interface**

In the same file, after the `SceneSnapshot` type (around line 25), add:

```go
// PlaceRef is a lightweight handle to a registered place, suitable for
// listing in read APIs. SceneID is the synthetic id under which the place
// runs (today: "place:<PlaceID>"); Leader is the first NPC in the place's
// yaml; Members are all NPCs in the place's scene. JSON tags are required
// for the same reason as CharacterRef.
type PlaceRef struct {
	ID      PlaceID       `json:"id"`
	SceneID SceneID       `json:"scene_id"`
	Leader  CharacterID   `json:"leader"`
	Members []CharacterRef `json:"members"`
}
```

In the `WorldAPI` interface, in the "Reads" section, append two methods so the interface ends like this:

```go
	// Reads — narrative-rich, suitable for direct user/LLM consumption.
	Where(ctx context.Context, characterID CharacterID) (SceneSnapshot, error)
	Log(ctx context.Context, since time.Duration) ([]LogEntry, error)
	Who(ctx context.Context, sceneID SceneID) ([]CharacterRef, error)
	Describe(ctx context.Context, id string) (string, error)
	Characters(ctx context.Context) ([]CharacterRef, error)
	Places(ctx context.Context) ([]PlaceRef, error)
}
```

- [ ] **Step 2a.2: Confirm build fails (interface not implemented)**

Run:
```bash
go build ./...
```

Expected: failures like `*world.apiImpl does not implement api.WorldAPI (missing Characters method)`. If the build succeeds, the interface change did not take effect; re-read.

### Task 2b: World coordinator queries

- [ ] **Step 2b.1: Add request types + lookup helpers to `internal/world/reads.go`**

Append to `internal/world/reads.go`:

```go
type charactersReq struct {
	reply chan []api.CharacterRef
}

type placesReq struct {
	reply chan []api.PlaceRef
}

// Characters lists every character registered with the world, marshalled
// via the coordinator goroutine so concurrent scene mutation cannot tear
// the result.
func (w *World) Characters(ctx context.Context) ([]api.CharacterRef, error) {
	rep := make(chan []api.CharacterRef, 1)
	select {
	case w.charactersReq <- charactersReq{reply: rep}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-rep:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Places lists every place-bound scene, in registration order.
func (w *World) Places(ctx context.Context) ([]api.PlaceRef, error) {
	rep := make(chan []api.PlaceRef, 1)
	select {
	case w.placesReq <- placesReq{reply: rep}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-rep:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *World) lookupCharacters() []api.CharacterRef {
	out := make([]api.CharacterRef, 0, len(w.characters))
	for id, c := range w.characters {
		out = append(out, api.CharacterRef{ID: id, Name: c.Name, Blurb: c.Blurb})
	}
	// Stable order — map iteration is not.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (w *World) lookupPlaces() []api.PlaceRef {
	out := make([]api.PlaceRef, 0)
	for _, sid := range w.sceneOrder {
		sc := w.scenes[sid]
		if sc == nil || sc.PlaceID == "" {
			continue
		}
		members := make([]api.CharacterRef, 0, len(sc.Members))
		for _, m := range sc.Members {
			members = append(members, api.CharacterRef{ID: m.ID, Name: m.Name, Blurb: m.Blurb})
		}
		ref := api.PlaceRef{
			ID:      sc.PlaceID,
			SceneID: sc.ID,
			Members: members,
		}
		if sc.Leader != nil {
			ref.Leader = sc.Leader.ID
		}
		out = append(out, ref)
	}
	return out
}
```

Add `"sort"` to the existing import block. The file's current imports are `"context"` and the `api` package; add `"sort"` between them alphabetically.

- [ ] **Step 2b.2: Add the channels to `World` and wire them**

Edit `internal/world/world.go`. In the `World` struct (around lines 36-40), in the same block as `whereReq` / `whoReq`, add:

```go
	whereReq      chan whereReq
	whoReq        chan whoReq
	charactersReq chan charactersReq
	placesReq     chan placesReq
```

(Replace the existing two-line block with these four lines.)

In the `New` function (around lines 66-72), wherever `whereReq` and `whoReq` are constructed, add the matching `make` calls. Replace:

```go
		whereReq:     make(chan whereReq),
		whoReq:       make(chan whoReq),
```

with:

```go
		whereReq:      make(chan whereReq),
		whoReq:        make(chan whoReq),
		charactersReq: make(chan charactersReq),
		placesReq:     make(chan placesReq),
```

In the `Run` select (around lines 137-140), after the existing `whoReq` case, add:

```go
		case req := <-w.charactersReq:
			req.reply <- w.lookupCharacters()
		case req := <-w.placesReq:
			req.reply <- w.lookupPlaces()
```

- [ ] **Step 2b.3: Implement on `apiImpl`**

Edit `internal/world/api.go`. After the existing `Who` method (~line 70), add:

```go
func (a apiImpl) Characters(ctx context.Context) ([]api.CharacterRef, error) {
	return a.w.Characters(ctx)
}

func (a apiImpl) Places(ctx context.Context) ([]api.PlaceRef, error) {
	return a.w.Places(ctx)
}
```

- [ ] **Step 2b.4: Verify the build succeeds**

Run:
```bash
go build ./...
```

Expected: no errors. The interface is now fully implemented.

### Task 2c: Tests for the new methods

The existing `newTestWorld(t)` helper in `internal/world/world_test.go:59` boots a world with one scene (`scene-1`, no `PlaceID`) and characters that have no `Blurb` populated. The new tests need (a) a place-scene to exercise `Places`, and (b) non-empty `Blurb` values to verify ref shape end-to-end. Rather than mutate the shared helper and risk regressing the existing tests that rely on it, both new tests build their own inline fixture.

The world coordinator's `Characters` and `Places` methods use unbuffered request channels (`charactersReq`, `placesReq`); calls block until `Run` is servicing them. **Both new tests must `go w.Run(ctx)` before invoking the read** — same pattern as `TestWhereReturnsSceneSnapshot` at `world_test.go:232`.

- [ ] **Step 2c.1: Write failing tests with a self-contained place-scene fixture**

Append to `internal/world/world_test.go` (after the last existing test, `TestSummonKnownPlaceWritesSummonEventScopedToPlaceScene`):

```go
// newCharactersPlacesWorld builds a world with one regular scene and one
// place-scene, characters carry blurbs so the assertions exercise real
// data. Returned context is already long enough for the read query.
func newCharactersPlacesWorld(t *testing.T) (*World, context.Context, context.CancelFunc) {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mk := func(id api.CharacterID, name, blurb string) *character.Character {
		return &character.Character{
			ID:     id,
			Name:   name,
			Blurb:  blurb,
			Memory: memory.NewInMem(10),
			Inbox:  make(chan character.Perception, 4),
		}
	}

	gangLeader := mk("stinky-sam", "Stinky Sam", "smells like a wet dog")
	gang := &scene.Scene{
		ID:      api.SceneID("the-gang"),
		Members: []*character.Character{gangLeader, mk("booger-bertha", "Booger Bertha", "picks her nose")},
		Leader:  gangLeader,
	}

	vicar := mk("vicar", "The Vicar", "worried about the draft")
	cathedral := &scene.Scene{
		ID:      api.SceneID("place:cathedral"),
		PlaceID: api.PlaceID("cathedral"),
		Members: []*character.Character{vicar, mk("caretaker", "The Caretaker", "mutters at a broom")},
		Leader:  vicar,
	}

	w := New(Config{TickInterval: time.Hour}, st, &mockLLM{})
	w.RegisterScene(gang)
	w.RegisterScene(cathedral)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	go func() { _ = w.Run(ctx) }()
	return w, ctx, cancel
}

func TestWorldCharactersReturnsSortedRefs(t *testing.T) {
	w, ctx, cancel := newCharactersPlacesWorld(t)
	defer cancel()

	refs, err := w.Characters(ctx)
	if err != nil {
		t.Fatalf("Characters: %v", err)
	}
	if len(refs) != 4 {
		t.Fatalf("Characters len: got %d, want 4", len(refs))
	}
	for i := 1; i < len(refs); i++ {
		if refs[i-1].ID >= refs[i].ID {
			t.Errorf("Characters not sorted: %q before %q", refs[i-1].ID, refs[i].ID)
		}
	}
	for _, r := range refs {
		if r.Name == "" {
			t.Errorf("CharacterRef %q has empty Name", r.ID)
		}
		if r.Blurb == "" {
			t.Errorf("CharacterRef %q has empty Blurb", r.ID)
		}
	}
}

func TestWorldPlacesOnlyIncludesPlaceScenes(t *testing.T) {
	w, ctx, cancel := newCharactersPlacesWorld(t)
	defer cancel()

	places, err := w.Places(ctx)
	if err != nil {
		t.Fatalf("Places: %v", err)
	}
	if len(places) != 1 {
		t.Fatalf("Places len: got %d, want 1 (the gang scene has no PlaceID)", len(places))
	}
	p := places[0]
	if p.ID != api.PlaceID("cathedral") {
		t.Errorf("place ID: got %q, want %q", p.ID, "cathedral")
	}
	if p.SceneID != api.SceneID("place:cathedral") {
		t.Errorf("scene id: got %q, want %q", p.SceneID, "place:cathedral")
	}
	if p.Leader != api.CharacterID("vicar") {
		t.Errorf("leader: got %q, want %q", p.Leader, "vicar")
	}
	if len(p.Members) != 2 {
		t.Errorf("members len: got %d, want 2", len(p.Members))
	}
}
```

If the existing imports in `internal/world/world_test.go` do not already include `context`, `time`, `store`, `memory`, `character`, `scene`, and `api`, leave them as-is — they are all present today (verified by the existing tests in the file). No import edits required.

- [ ] **Step 2c.2: Run tests, expect failures to clear**

Run:
```bash
go test ./internal/world/... -run 'TestWorldCharacters|TestWorldPlaces' -v
```

Expected: both tests PASS. If `newCharactersPlacesWorld` is reported as undefined, Step 2c.1 was applied incompletely; the helper goes in `world_test.go` alongside the tests.

- [ ] **Step 2c.3: Run the full world test suite to catch regressions**

Run:
```bash
go test ./internal/world/... -v
```

Expected: all PASS.

- [ ] **Step 2c.4: Commit**

```bash
git add internal/api/api.go internal/world/reads.go internal/world/world.go internal/world/api.go internal/world/world_test.go
git commit -m "api: add Characters and Places to WorldAPI for read adapters"
```

---

## Task 3: Scaffold `internal/mcp` package

**Files:**
- Create: `internal/mcp/adapter.go`
- Create: `internal/mcp/fake_world_test.go`
- Create: `internal/mcp/adapter_test.go`

- [ ] **Step 3.1: Create the package directory**

```bash
mkdir -p internal/mcp
```

- [ ] **Step 3.2: Write the adapter skeleton**

Create `internal/mcp/adapter.go`:

```go
// Package mcp adapts api.WorldAPI to a Model Context Protocol server. The
// adapter is parallel in shape to internal/irc/adapter.go: a Config, a
// New, and a Run that blocks until ctx is cancelled. Tools and resources
// are registered in New so external tests can inspect them.
package mcp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/afternet/go-vibebot/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Implementation identity reported to MCP clients on initialize. Bump
// Version when the tool surface changes incompatibly.
const (
	serverName    = "go-vibebot"
	serverVersion = "v0"
)

// Config configures the adapter. Logger is required only nominally — a
// nil Logger falls back to slog.Default.
type Config struct {
	Logger *slog.Logger
}

// Adapter owns the MCP server and its handlers. Construct with New, then
// call Run with a transport.
type Adapter struct {
	cfg    Config
	api    api.WorldAPI
	logger *slog.Logger
	server *mcpsdk.Server
}

// New constructs an Adapter and registers every tool and resource against
// the supplied WorldAPI. Tools and resources are pure wrappers — they
// hold no state of their own; the WorldAPI is the truth.
func New(cfg Config, w api.WorldAPI) (*Adapter, error) {
	if w == nil {
		return nil, errors.New("mcp: WorldAPI is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	a := &Adapter{cfg: cfg, api: w, logger: cfg.Logger}
	a.server = mcpsdk.NewServer(
		&mcpsdk.Implementation{Name: serverName, Version: serverVersion},
		nil,
	)
	a.registerTools()
	a.registerResources()
	return a, nil
}

// Run blocks, serving MCP over the provided transport until ctx is
// cancelled or the client disconnects. Use a *mcpsdk.StdioTransport for
// the cmd/sim --mcp-stdio path, or NewInMemoryTransports for tests.
func (a *Adapter) Run(ctx context.Context, t mcpsdk.Transport) error {
	return a.server.Run(ctx, t)
}

// registerTools and registerResources are implemented in tools.go and
// resources.go. Empty stubs here keep New compiling until later tasks add
// the real registrations.
func (a *Adapter) registerTools()     {}
func (a *Adapter) registerResources() {}
```

- [ ] **Step 3.3: Write a shared fake WorldAPI for tests**

Create `internal/mcp/fake_world_test.go`:

```go
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

	// Recorded calls.
	InjectCalls []InjectCall
	SummonCalls []SummonCall
	NudgeCalls  []NudgeCall
	LogCalls    []LogCall
}

type InjectCall struct {
	SceneID     api.SceneID
	Target      string
	Description string
}

type SummonCall struct{ PlaceID api.PlaceID }
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

func (f *fakeWorld) Characters(_ context.Context) ([]api.CharacterRef, error) {
	return f.CharactersReturn, f.CharactersErr
}

func (f *fakeWorld) Places(_ context.Context) ([]api.PlaceRef, error) {
	return f.PlacesReturn, f.PlacesErr
}
```

- [ ] **Step 3.4: Write a compile/wire test**

Create `internal/mcp/adapter_test.go`:

```go
package mcp

import (
	"testing"
)

func TestNewRejectsNilWorld(t *testing.T) {
	_, err := New(Config{}, nil)
	if err == nil {
		t.Fatal("New(nil) returned no error")
	}
}

func TestNewBuildsAdapterWithDefaults(t *testing.T) {
	a, err := New(Config{}, &fakeWorld{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.logger == nil {
		t.Error("logger fallback not applied")
	}
	if a.server == nil {
		t.Error("server not constructed")
	}
}
```

- [ ] **Step 3.5: Run the scaffold tests**

Run:
```bash
go test ./internal/mcp/... -v
```

Expected: both tests PASS.

- [ ] **Step 3.6: Commit**

```bash
git add internal/mcp/adapter.go internal/mcp/fake_world_test.go internal/mcp/adapter_test.go
git commit -m "mcp: scaffold adapter package wrapping WorldAPI"
```

---

## Task 4: `inject` tool

**Files:**
- Create: `internal/mcp/tools.go`
- Create: `internal/mcp/tools_test.go`

- [ ] **Step 4.1: Write the failing inject handler test**

Create `internal/mcp/tools_test.go`:

```go
package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/afternet/go-vibebot/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInjectHandlerForwardsArgs(t *testing.T) {
	fw := &fakeWorld{}
	a, err := New(Config{}, fw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, out, err := a.injectHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		InjectInput{SceneID: "cathedral", Target: "vicar", Description: "a candle falls"},
	)
	if err != nil {
		t.Fatalf("inject handler returned error: %v", err)
	}
	if result != nil && result.IsError {
		t.Fatalf("inject handler reported tool error: %+v", result)
	}
	if !out.OK {
		t.Errorf("InjectOutput.OK=false; want true")
	}
	if len(fw.InjectCalls) != 1 {
		t.Fatalf("expected 1 InjectEvent call, got %d", len(fw.InjectCalls))
	}
	got := fw.InjectCalls[0]
	want := InjectCall{SceneID: api.SceneID("cathedral"), Target: "vicar", Description: "a candle falls"}
	if got != want {
		t.Errorf("InjectEvent call: got %+v, want %+v", got, want)
	}
}

func TestInjectHandlerSurfacesWorldErrorAsToolError(t *testing.T) {
	fw := &fakeWorld{InjectErr: errors.New("unknown scene \"void\"")}
	a, err := New(Config{}, fw)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, _, err := a.injectHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		InjectInput{SceneID: "void", Description: "x"},
	)
	if err != nil {
		t.Fatalf("inject handler returned protocol error %v; want tool-level error", err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("expected IsError result, got %+v", result)
	}
	body := contentText(result)
	if !strings.Contains(body, "unknown scene") {
		t.Errorf("error content %q does not include underlying world error", body)
	}
}

// contentText concatenates the Text field of every TextContent in the
// CallToolResult's Content slice. Returns "" if Content is empty.
func contentText(r *mcpsdk.CallToolResult) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
```

- [ ] **Step 4.2: Run the test; expect compile failure**

Run:
```bash
go test ./internal/mcp/... -run TestInject -v
```

Expected: FAIL — `a.injectHandler undefined`, `InjectInput undefined`, `InjectOutput undefined`. This is the red phase.

- [ ] **Step 4.3: Implement the inject tool**

Create `internal/mcp/tools.go`. Imports are intentionally minimal — Task 7 adds `"time"` when the log tool needs `time.ParseDuration`.

```go
package mcp

import (
	"context"
	"fmt"

	"github.com/afternet/go-vibebot/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// InjectInput / InjectOutput are the typed payload for the "inject" tool.
// JSON Schema is inferred from struct tags; ,omitempty fields are optional.
type InjectInput struct {
	SceneID     string `json:"scene_id,omitempty" jsonschema:"optional scene id; empty means the default scene"`
	Target      string `json:"target,omitempty" jsonschema:"optional target character id"`
	Description string `json:"description" jsonschema:"the scenario text to inject"`
}

type InjectOutput struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (a *Adapter) injectHandler(ctx context.Context, _ *mcpsdk.CallToolRequest, in InjectInput) (*mcpsdk.CallToolResult, InjectOutput, error) {
	if in.Description == "" {
		return toolError("inject: description is required"), InjectOutput{}, nil
	}
	if err := a.api.InjectEvent(ctx, api.SceneID(in.SceneID), in.Target, in.Description); err != nil {
		return toolError(fmt.Sprintf("inject failed: %s", err.Error())), InjectOutput{}, nil
	}
	a.logger.Info("mcp inject", "scene", in.SceneID, "target", in.Target)
	return nil, InjectOutput{OK: true, Message: "injected."}, nil
}

// toolError builds a CallToolResult with IsError=true and the message
// packed into a single TextContent block. Use for WorldAPI errors and
// input validation — never for protocol-level breaks (return Go error).
func toolError(msg string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: msg}},
	}
}
```

Modify the `registerTools` stub in `internal/mcp/adapter.go` (Task 3, Step 3.2) to register the inject tool. Replace:

```go
func (a *Adapter) registerTools()     {}
```

with:

```go
func (a *Adapter) registerTools() {
	mcpsdk.AddTool(a.server,
		&mcpsdk.Tool{
			Name:        "inject",
			Description: "Inject a scenario event into a scene. scene_id empty = default scene.",
		},
		a.injectHandler,
	)
}
```

- [ ] **Step 4.4: Run the inject tests; expect green**

Run:
```bash
go test ./internal/mcp/... -run TestInject -v
```

Expected: both `TestInjectHandlerForwardsArgs` and `TestInjectHandlerSurfacesWorldErrorAsToolError` PASS.

- [ ] **Step 4.5: Run the full mcp test suite**

Run:
```bash
go test ./internal/mcp/...
```

Expected: PASS.

- [ ] **Step 4.6: Commit**

```bash
git add internal/mcp/tools.go internal/mcp/adapter.go internal/mcp/tools_test.go
git commit -m "mcp: add inject tool wired to WorldAPI.InjectEvent"
```

---

## Task 5: `nudge` tool

**Files:**
- Modify: `internal/mcp/tools.go`
- Modify: `internal/mcp/tools_test.go`
- Modify: `internal/mcp/adapter.go`

- [ ] **Step 5.1: Write the failing nudge tests**

Append to `internal/mcp/tools_test.go`:

```go
func TestNudgeHandlerForwardsArgs(t *testing.T) {
	fw := &fakeWorld{}
	a, _ := New(Config{}, fw)
	_, out, err := a.nudgeHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		NudgeInput{CharacterID: "stinky-sam"},
	)
	if err != nil {
		t.Fatalf("nudge handler returned error: %v", err)
	}
	if !out.OK {
		t.Errorf("NudgeOutput.OK=false; want true")
	}
	if len(fw.NudgeCalls) != 1 || fw.NudgeCalls[0].CharacterID != api.CharacterID("stinky-sam") {
		t.Errorf("NudgeCalls: %+v", fw.NudgeCalls)
	}
}

func TestNudgeHandlerRejectsEmptyCharacter(t *testing.T) {
	fw := &fakeWorld{}
	a, _ := New(Config{}, fw)
	result, _, err := a.nudgeHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		NudgeInput{},
	)
	if err != nil {
		t.Fatalf("got protocol err %v; want tool err", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("empty character_id must produce tool error")
	}
	if len(fw.NudgeCalls) != 0 {
		t.Errorf("Nudge should not have been called: %+v", fw.NudgeCalls)
	}
}
```

- [ ] **Step 5.2: Run — expect failure on undefined symbols**

Run:
```bash
go test ./internal/mcp/... -run TestNudge -v
```

Expected: FAIL on `NudgeInput undefined`.

- [ ] **Step 5.3: Implement nudge**

Append to `internal/mcp/tools.go` (just below the inject types):

```go
type NudgeInput struct {
	CharacterID string `json:"character_id" jsonschema:"the character id to nudge"`
}

type NudgeOutput struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (a *Adapter) nudgeHandler(ctx context.Context, _ *mcpsdk.CallToolRequest, in NudgeInput) (*mcpsdk.CallToolResult, NudgeOutput, error) {
	if in.CharacterID == "" {
		return toolError("nudge: character_id is required"), NudgeOutput{}, nil
	}
	if err := a.api.Nudge(ctx, api.CharacterID(in.CharacterID)); err != nil {
		return toolError(fmt.Sprintf("nudge failed: %s", err.Error())), NudgeOutput{}, nil
	}
	a.logger.Info("mcp nudge", "character", in.CharacterID)
	return nil, NudgeOutput{OK: true, Message: "nudged."}, nil
}
```

In `internal/mcp/adapter.go`, extend `registerTools`:

```go
func (a *Adapter) registerTools() {
	mcpsdk.AddTool(a.server,
		&mcpsdk.Tool{
			Name:        "inject",
			Description: "Inject a scenario event into a scene. scene_id empty = default scene.",
		},
		a.injectHandler,
	)
	mcpsdk.AddTool(a.server,
		&mcpsdk.Tool{
			Name:        "nudge",
			Description: "Nudge a character so they take a turn now instead of on the next tick.",
		},
		a.nudgeHandler,
	)
}
```

- [ ] **Step 5.4: Run — expect green**

Run:
```bash
go test ./internal/mcp/...
```

Expected: PASS.

- [ ] **Step 5.5: Commit**

```bash
git add internal/mcp/tools.go internal/mcp/adapter.go internal/mcp/tools_test.go
git commit -m "mcp: add nudge tool"
```

---

## Task 6: `summon` tool

**Files:**
- Modify: `internal/mcp/tools.go`
- Modify: `internal/mcp/tools_test.go`
- Modify: `internal/mcp/adapter.go`

- [ ] **Step 6.1: Write the failing summon tests**

Append to `internal/mcp/tools_test.go`:

```go
func TestSummonHandlerForwardsPlaceID(t *testing.T) {
	fw := &fakeWorld{}
	a, _ := New(Config{}, fw)
	_, out, err := a.summonHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		SummonInput{PlaceID: "cathedral"},
	)
	if err != nil {
		t.Fatalf("summon handler returned error: %v", err)
	}
	if !out.OK {
		t.Error("SummonOutput.OK=false")
	}
	if len(fw.SummonCalls) != 1 || fw.SummonCalls[0].PlaceID != api.PlaceID("cathedral") {
		t.Errorf("SummonCalls: %+v", fw.SummonCalls)
	}
}

func TestSummonHandlerSurfacesUnknownPlaceAsToolError(t *testing.T) {
	fw := &fakeWorld{SummonErr: errors.New(`summon: unknown place "void"`)}
	a, _ := New(Config{}, fw)
	result, _, err := a.summonHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		SummonInput{PlaceID: "void"},
	)
	if err != nil {
		t.Fatalf("got protocol err %v; want tool err", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("unknown place must produce IsError result")
	}
	if !strings.Contains(contentText(result), "unknown place") {
		t.Errorf("missing underlying error in content: %q", contentText(result))
	}
}
```

- [ ] **Step 6.2: Run — expect failure**

Run:
```bash
go test ./internal/mcp/... -run TestSummon -v
```

Expected: FAIL — `SummonInput undefined`.

- [ ] **Step 6.3: Implement summon**

Append to `internal/mcp/tools.go`:

```go
type SummonInput struct {
	PlaceID string `json:"place_id" jsonschema:"the place id to summon (must be loaded)"`
}

type SummonOutput struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (a *Adapter) summonHandler(ctx context.Context, _ *mcpsdk.CallToolRequest, in SummonInput) (*mcpsdk.CallToolResult, SummonOutput, error) {
	if in.PlaceID == "" {
		return toolError("summon: place_id is required"), SummonOutput{}, nil
	}
	if err := a.api.Summon(ctx, api.PlaceID(in.PlaceID)); err != nil {
		return toolError(fmt.Sprintf("summon failed: %s", err.Error())), SummonOutput{}, nil
	}
	a.logger.Info("mcp summon", "place", in.PlaceID)
	return nil, SummonOutput{OK: true, Message: "summoned."}, nil
}
```

Extend `registerTools` in `internal/mcp/adapter.go`:

```go
	mcpsdk.AddTool(a.server,
		&mcpsdk.Tool{
			Name:        "summon",
			Description: "Open a place scene so its NPCs become reachable. Errors if the place is not loaded.",
		},
		a.summonHandler,
	)
```

- [ ] **Step 6.4: Run — expect green**

Run:
```bash
go test ./internal/mcp/...
```

Expected: PASS.

- [ ] **Step 6.5: Commit**

```bash
git add internal/mcp/tools.go internal/mcp/adapter.go internal/mcp/tools_test.go
git commit -m "mcp: add summon tool with unknown-place tool error path"
```

---

## Task 7: `log` tool

**Files:**
- Modify: `internal/mcp/tools.go`
- Modify: `internal/mcp/tools_test.go`
- Modify: `internal/mcp/adapter.go`

- [ ] **Step 7.1: Write the failing log tool tests**

First, extend the import block in `internal/mcp/tools_test.go` to include `"time"`:

```go
import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)
```

Then append to the same file:

```go
func TestLogHandlerUsesDefaultSinceWhenEmpty(t *testing.T) {
	fw := &fakeWorld{LogReturn: []api.LogEntry{}}
	a, _ := New(Config{}, fw)
	_, out, err := a.logHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		LogInput{},
	)
	if err != nil {
		t.Fatalf("log handler returned error: %v", err)
	}
	if len(fw.LogCalls) != 1 {
		t.Fatalf("expected 1 Log call, got %d", len(fw.LogCalls))
	}
	if fw.LogCalls[0].Since != time.Hour {
		t.Errorf("default since: got %v, want 1h", fw.LogCalls[0].Since)
	}
	if out.Entries == nil {
		t.Errorf("Entries must be non-nil even when empty")
	}
}

func TestLogHandlerParsesSince(t *testing.T) {
	fw := &fakeWorld{LogReturn: []api.LogEntry{}}
	a, _ := New(Config{}, fw)
	_, _, err := a.logHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		LogInput{Since: "30m"},
	)
	if err != nil {
		t.Fatalf("log handler returned error: %v", err)
	}
	if fw.LogCalls[0].Since != 30*time.Minute {
		t.Errorf("since: got %v, want 30m", fw.LogCalls[0].Since)
	}
}

func TestLogHandlerRejectsBadSince(t *testing.T) {
	fw := &fakeWorld{}
	a, _ := New(Config{}, fw)
	result, _, err := a.logHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		LogInput{Since: "thursday"},
	)
	if err != nil {
		t.Fatalf("got protocol err %v; want tool err", err)
	}
	if result == nil || !result.IsError {
		t.Fatal("bad since should produce IsError result")
	}
	if len(fw.LogCalls) != 0 {
		t.Errorf("Log must not be called on bad since: %+v", fw.LogCalls)
	}
}

func TestLogHandlerFiltersByScene(t *testing.T) {
	t0 := time.Now()
	fw := &fakeWorld{LogReturn: []api.LogEntry{
		{Timestamp: t0, SceneID: api.SceneID("the-gang"), Actor: "world", Kind: "inject", Text: "a"},
		{Timestamp: t0, SceneID: api.SceneID("place:cathedral"), Actor: "world", Kind: "inject", Text: "b"},
	}}
	a, _ := New(Config{}, fw)
	_, out, err := a.logHandler(context.Background(),
		&mcpsdk.CallToolRequest{},
		LogInput{SceneID: "place:cathedral"},
	)
	if err != nil {
		t.Fatalf("log handler error: %v", err)
	}
	if len(out.Entries) != 1 {
		t.Fatalf("got %d entries, want 1 (scene-filtered)", len(out.Entries))
	}
	if out.Entries[0].Text != "b" {
		t.Errorf("filtered entry text: got %q want %q", out.Entries[0].Text, "b")
	}
}
```

- [ ] **Step 7.2: Run — expect failure**

Run:
```bash
go test ./internal/mcp/... -run TestLog -v
```

Expected: FAIL — `LogInput undefined`.

- [ ] **Step 7.3: Implement log tool**

In `internal/mcp/tools.go`, extend the import block to add `"time"` (newly consumed by `time.ParseDuration`, `time.Hour`, `time.RFC3339Nano`):

```go
import (
	"context"
	"fmt"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)
```

Then append:

```go
type LogInput struct {
	Since   string `json:"since,omitempty" jsonschema:"Go duration string (e.g. \"1h\", \"30m\"); default 1h"`
	SceneID string `json:"scene_id,omitempty" jsonschema:"optional scene id filter; empty = all scenes"`
}

type LogOutput struct {
	Since   string         `json:"since"`
	Entries []LogEntryJSON `json:"entries"`
}

// LogEntryJSON mirrors api.LogEntry but emits an RFC3339 timestamp string,
// because JSON consumers parse strings more reliably than time.Time tagged
// values via the SDK's schema inference.
type LogEntryJSON struct {
	Timestamp string `json:"timestamp"`
	SceneID   string `json:"scene_id"`
	Actor     string `json:"actor"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
}

const defaultLogSince = time.Hour

func (a *Adapter) logHandler(ctx context.Context, _ *mcpsdk.CallToolRequest, in LogInput) (*mcpsdk.CallToolResult, LogOutput, error) {
	dur := defaultLogSince
	if in.Since != "" {
		d, err := time.ParseDuration(in.Since)
		if err != nil {
			return toolError(fmt.Sprintf("log: invalid since %q: %s", in.Since, err.Error())), LogOutput{}, nil
		}
		dur = d
	}
	entries, err := a.api.Log(ctx, dur)
	if err != nil {
		return toolError(fmt.Sprintf("log failed: %s", err.Error())), LogOutput{}, nil
	}
	out := LogOutput{
		Since:   dur.String(),
		Entries: make([]LogEntryJSON, 0, len(entries)),
	}
	for _, e := range entries {
		if in.SceneID != "" && string(e.SceneID) != in.SceneID {
			continue
		}
		out.Entries = append(out.Entries, LogEntryJSON{
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
			SceneID:   string(e.SceneID),
			Actor:     e.Actor,
			Kind:      e.Kind,
			Text:      e.Text,
		})
	}
	return nil, out, nil
}
```

Extend `registerTools` in `internal/mcp/adapter.go`:

```go
	mcpsdk.AddTool(a.server,
		&mcpsdk.Tool{
			Name:        "log",
			Description: "Read recent world events. since defaults to 1h; scene_id filters to one scene.",
		},
		a.logHandler,
	)
```

- [ ] **Step 7.4: Run — expect green**

Run:
```bash
go test ./internal/mcp/...
```

Expected: PASS.

- [ ] **Step 7.5: Commit**

```bash
git add internal/mcp/tools.go internal/mcp/adapter.go internal/mcp/tools_test.go
git commit -m "mcp: add log tool with since parsing and scene filter"
```

---

## Task 8: `world://characters` resource

**Files:**
- Create: `internal/mcp/resources.go`
- Create: `internal/mcp/resources_test.go`
- Modify: `internal/mcp/adapter.go`

- [ ] **Step 8.1: Write the failing characters resource test**

Create `internal/mcp/resources_test.go`:

```go
package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/afternet/go-vibebot/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCharactersResourceReturnsJSON(t *testing.T) {
	fw := &fakeWorld{CharactersReturn: []api.CharacterRef{
		{ID: "stinky-sam", Name: "Stinky Sam", Blurb: "smells like a wet dog"},
		{ID: "vicar", Name: "The Vicar", Blurb: "worried about the draft"},
	}}
	a, _ := New(Config{}, fw)

	res, err := a.charactersHandler(context.Background(),
		&mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: "world://characters"}},
	)
	if err != nil {
		t.Fatalf("charactersHandler: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("Contents len: %d", len(res.Contents))
	}
	got := res.Contents[0]
	if got.URI != "world://characters" {
		t.Errorf("URI: %q", got.URI)
	}
	if got.MIMEType != "application/json" {
		t.Errorf("MIMEType: %q", got.MIMEType)
	}

	var parsed []api.CharacterRef
	if err := json.Unmarshal([]byte(got.Text), &parsed); err != nil {
		t.Fatalf("unmarshal: %v; text=%q", err, got.Text)
	}
	if len(parsed) != 2 {
		t.Errorf("len: %d", len(parsed))
	}
	if !strings.Contains(got.Text, "stinky-sam") {
		t.Errorf("Text missing character: %q", got.Text)
	}
	// Lock the JSON key casing contract — LLM consumers parse by lowercase keys.
	for _, want := range []string{`"id":"stinky-sam"`, `"name":"Stinky Sam"`, `"blurb":"smells like a wet dog"`} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("Text missing %q in %q", want, got.Text)
		}
	}
}
```

- [ ] **Step 8.2: Run — expect failure**

Run:
```bash
go test ./internal/mcp/... -run TestCharactersResource -v
```

Expected: FAIL — `a.charactersHandler undefined`.

- [ ] **Step 8.3: Implement the resource**

Create `internal/mcp/resources.go`. Imports are kept minimal — only what this task needs. Task 10 adds `net/url` and `time` when the log resource lands.

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	uriCharacters = "world://characters"
	uriPlaces     = "world://places"
	uriLogStatic  = "world://log"
	uriLogTmpl    = "world://log{?since,scene}"
	mimeJSON      = "application/json"
)

func (a *Adapter) charactersHandler(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	refs, err := a.api.Characters(ctx)
	if err != nil {
		return nil, fmt.Errorf("characters: %w", err)
	}
	body, err := json.Marshal(refs)
	if err != nil {
		return nil, fmt.Errorf("characters: marshal: %w", err)
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{
			URI:      uriCharacters,
			MIMEType: mimeJSON,
			Text:     string(body),
		}},
	}, nil
}
```

Modify `registerResources` in `internal/mcp/adapter.go`. Replace:

```go
func (a *Adapter) registerResources() {}
```

with:

```go
func (a *Adapter) registerResources() {
	a.server.AddResource(
		&mcpsdk.Resource{
			URI:         uriCharacters,
			Name:        "characters",
			Description: "All characters registered with the world.",
			MIMEType:    mimeJSON,
		},
		a.charactersHandler,
	)
}
```

Note: `internal/api` is NOT imported in `resources.go` — the package's types reach this file only via `a.api.Characters(ctx)` whose return type is inferred, which does not require an explicit `api.X` reference at the file level. Adding the import would fail `go vet` with "imported and not used."

- [ ] **Step 8.4: Run — expect green**

Run:
```bash
go test ./internal/mcp/...
```

Expected: PASS.

- [ ] **Step 8.5: Commit**

```bash
git add internal/mcp/resources.go internal/mcp/adapter.go internal/mcp/resources_test.go
git commit -m "mcp: add world://characters resource"
```

---

## Task 9: `world://places` resource

**Files:**
- Modify: `internal/mcp/resources.go`
- Modify: `internal/mcp/resources_test.go`
- Modify: `internal/mcp/adapter.go`

- [ ] **Step 9.1: Write the failing places resource test**

Append to `internal/mcp/resources_test.go`:

```go
func TestPlacesResourceReturnsJSON(t *testing.T) {
	fw := &fakeWorld{PlacesReturn: []api.PlaceRef{
		{
			ID:      "cathedral",
			SceneID: "place:cathedral",
			Leader:  "vicar",
			Members: []api.CharacterRef{
				{ID: "vicar", Name: "The Vicar"},
				{ID: "caretaker", Name: "The Caretaker"},
			},
		},
	}}
	a, _ := New(Config{}, fw)

	res, err := a.placesHandler(context.Background(),
		&mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: "world://places"}},
	)
	if err != nil {
		t.Fatalf("placesHandler: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("Contents len: %d", len(res.Contents))
	}
	got := res.Contents[0]
	if got.URI != "world://places" {
		t.Errorf("URI: %q", got.URI)
	}

	var parsed []api.PlaceRef
	if err := json.Unmarshal([]byte(got.Text), &parsed); err != nil {
		t.Fatalf("unmarshal: %v; text=%q", err, got.Text)
	}
	if len(parsed) != 1 || parsed[0].ID != "cathedral" || parsed[0].Leader != "vicar" {
		t.Errorf("parsed: %+v", parsed)
	}
	// Lock the JSON key casing contract — PlaceRef tags are lowercase / snake_case.
	for _, want := range []string{`"id":"cathedral"`, `"scene_id":"place:cathedral"`, `"leader":"vicar"`, `"members":`} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("Text missing %q in %q", want, got.Text)
		}
	}
}
```

- [ ] **Step 9.2: Run — expect failure**

Run:
```bash
go test ./internal/mcp/... -run TestPlacesResource -v
```

Expected: FAIL.

- [ ] **Step 9.3: Implement places resource**

In `internal/mcp/resources.go`, after `charactersHandler`, append:

```go
func (a *Adapter) placesHandler(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	refs, err := a.api.Places(ctx)
	if err != nil {
		return nil, fmt.Errorf("places: %w", err)
	}
	body, err := json.Marshal(refs)
	if err != nil {
		return nil, fmt.Errorf("places: marshal: %w", err)
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{
			URI:      uriPlaces,
			MIMEType: mimeJSON,
			Text:     string(body),
		}},
	}, nil
}
```

Extend `registerResources` in `internal/mcp/adapter.go`:

```go
	a.server.AddResource(
		&mcpsdk.Resource{
			URI:         uriPlaces,
			Name:        "places",
			Description: "All places currently registered as scenes.",
			MIMEType:    mimeJSON,
		},
		a.placesHandler,
	)
```

- [ ] **Step 9.4: Run — expect green**

Run:
```bash
go test ./internal/mcp/...
```

Expected: PASS.

- [ ] **Step 9.5: Commit**

```bash
git add internal/mcp/resources.go internal/mcp/adapter.go internal/mcp/resources_test.go
git commit -m "mcp: add world://places resource"
```

---

## Task 10: `world://log` resource template with query params

**Files:**
- Modify: `internal/mcp/resources.go`
- Modify: `internal/mcp/resources_test.go`
- Modify: `internal/mcp/adapter.go`

- [ ] **Step 10.1: Write the failing log resource tests**

First, extend the import block in `internal/mcp/resources_test.go` to include `"time"`:

```go
import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)
```

Then append to the same file:

```go
func TestLogResourceDefaultsSinceTo1h(t *testing.T) {
	fw := &fakeWorld{LogReturn: []api.LogEntry{}}
	a, _ := New(Config{}, fw)
	_, err := a.logResourceHandler(context.Background(),
		&mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: "world://log"}},
	)
	if err != nil {
		t.Fatalf("logResourceHandler: %v", err)
	}
	if len(fw.LogCalls) != 1 || fw.LogCalls[0].Since != time.Hour {
		t.Errorf("LogCalls: %+v", fw.LogCalls)
	}
}

func TestLogResourceParsesSinceQuery(t *testing.T) {
	fw := &fakeWorld{LogReturn: []api.LogEntry{}}
	a, _ := New(Config{}, fw)
	_, err := a.logResourceHandler(context.Background(),
		&mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: "world://log?since=15m"}},
	)
	if err != nil {
		t.Fatalf("logResourceHandler: %v", err)
	}
	if len(fw.LogCalls) != 1 || fw.LogCalls[0].Since != 15*time.Minute {
		t.Errorf("LogCalls: %+v", fw.LogCalls)
	}
}

func TestLogResourceFiltersByScene(t *testing.T) {
	t0 := time.Now()
	fw := &fakeWorld{LogReturn: []api.LogEntry{
		{Timestamp: t0, SceneID: api.SceneID("the-gang"), Actor: "world", Kind: "inject", Text: "a"},
		{Timestamp: t0, SceneID: api.SceneID("place:cathedral"), Actor: "world", Kind: "inject", Text: "b"},
	}}
	a, _ := New(Config{}, fw)
	res, err := a.logResourceHandler(context.Background(),
		&mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: "world://log?scene=place:cathedral"}},
	)
	if err != nil {
		t.Fatalf("logResourceHandler: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("Contents len: %d", len(res.Contents))
	}
	body := res.Contents[0].Text
	if !strings.Contains(body, `"text":"b"`) {
		t.Errorf("expected entry b in body: %q", body)
	}
	if strings.Contains(body, `"text":"a"`) {
		t.Errorf("entry a should have been filtered out: %q", body)
	}
}

func TestLogResourceRejectsBadSince(t *testing.T) {
	fw := &fakeWorld{}
	a, _ := New(Config{}, fw)
	_, err := a.logResourceHandler(context.Background(),
		&mcpsdk.ReadResourceRequest{Params: &mcpsdk.ReadResourceParams{URI: "world://log?since=thursday"}},
	)
	if err == nil {
		t.Fatal("bad since should return an error")
	}
	if len(fw.LogCalls) != 0 {
		t.Errorf("Log must not be called on bad since: %+v", fw.LogCalls)
	}
}
```

- [ ] **Step 10.2: Run — expect failure**

Run:
```bash
go test ./internal/mcp/... -run TestLogResource -v
```

Expected: FAIL — `a.logResourceHandler undefined`.

- [ ] **Step 10.3: Implement log resource handler**

In `internal/mcp/resources.go`, extend the import block to add `net/url` and `time` (both now consumed by the new handler):

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)
```

Append:

```go
func (a *Adapter) logResourceHandler(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	requestedURI := ""
	if req != nil && req.Params != nil {
		requestedURI = req.Params.URI
	}
	since, scene, err := parseLogQuery(requestedURI)
	if err != nil {
		return nil, err
	}
	entries, err := a.api.Log(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	out := struct {
		Since   string         `json:"since"`
		Entries []LogEntryJSON `json:"entries"`
	}{
		Since:   since.String(),
		Entries: make([]LogEntryJSON, 0, len(entries)),
	}
	for _, e := range entries {
		if scene != "" && string(e.SceneID) != scene {
			continue
		}
		out.Entries = append(out.Entries, LogEntryJSON{
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
			SceneID:   string(e.SceneID),
			Actor:     e.Actor,
			Kind:      e.Kind,
			Text:      e.Text,
		})
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("log: marshal: %w", err)
	}
	uriOut := requestedURI
	if uriOut == "" {
		uriOut = uriLogStatic
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{
			URI:      uriOut,
			MIMEType: mimeJSON,
			Text:     string(body),
		}},
	}, nil
}

// parseLogQuery extracts ?since and ?scene from the resolved URI. Missing
// since uses 1h. Empty scene means no filter. Unparseable since returns
// an error so the resource read fails (not a tool-error path).
func parseLogQuery(raw string) (time.Duration, string, error) {
	since := time.Hour
	scene := ""
	if raw == "" {
		return since, scene, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return 0, "", fmt.Errorf("log: invalid uri %q: %w", raw, err)
	}
	q := u.Query()
	if s := q.Get("since"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, "", fmt.Errorf("log: invalid since %q: %w", s, err)
		}
		since = d
	}
	if s := q.Get("scene"); s != "" {
		scene = s
	}
	return since, scene, nil
}
```

Extend `registerResources` in `internal/mcp/adapter.go`:

```go
	a.server.AddResourceTemplate(
		&mcpsdk.ResourceTemplate{
			URITemplate: uriLogTmpl,
			Name:        "log",
			Description: "Recent world events. Optional query params: since=<duration>, scene=<scene-id>.",
			MIMEType:    mimeJSON,
		},
		a.logResourceHandler,
	)
```

- [ ] **Step 10.4: Run — expect green**

Run:
```bash
go test ./internal/mcp/...
```

Expected: PASS.

- [ ] **Step 10.5: Commit**

```bash
git add internal/mcp/resources.go internal/mcp/adapter.go internal/mcp/resources_test.go
git commit -m "mcp: add world://log resource template with since/scene params"
```

---

## Task 11: End-to-end protocol test

**Files:**
- Modify: `internal/mcp/adapter_test.go`

This test proves that the SDK actually wires our handlers to the JSON-RPC protocol. It uses `mcp.NewInMemoryTransports` so no stdio plumbing is needed.

- [ ] **Step 11.1: Replace `adapter_test.go` with the full file**

The current `adapter_test.go` from Task 3 has two scaffold tests and a minimal import block; we are growing it to also house the e2e tests. Rather than append-with-merge (which has tripped reviewers — duplicate `import` blocks are a compile error), overwrite the file entirely with the content below. The Task 3 tests (`TestNewRejectsNilWorld`, `TestNewBuildsAdapterWithDefaults`) are preserved verbatim.

Replace the entire contents of `internal/mcp/adapter_test.go` with:

```go
package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/afternet/go-vibebot/internal/api"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewRejectsNilWorld(t *testing.T) {
	_, err := New(Config{}, nil)
	if err == nil {
		t.Fatal("New(nil) returned no error")
	}
}

func TestNewBuildsAdapterWithDefaults(t *testing.T) {
	a, err := New(Config{}, &fakeWorld{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.logger == nil {
		t.Error("logger fallback not applied")
	}
	if a.server == nil {
		t.Error("server not constructed")
	}
}

// runAdapter starts the adapter on serverT in a goroutine and returns a
// stop func that cancels the context, drains the run goroutine, and
// reports any non-cancellation error via t.Errorf — but only while the
// test is still alive. Calling stop from a t.Cleanup keeps t valid.
func runAdapter(t *testing.T, a *Adapter, serverT mcpsdk.Transport) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, serverT) }()
	stop := func() {
		cancel()
		err := <-done
		if err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			t.Errorf("adapter.Run: %v", err)
		}
	}
	return ctx, stop
}

func TestE2EInjectViaInMemoryTransport(t *testing.T) {
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
		Name: "inject",
		Arguments: map[string]any{
			"scene_id":    "cathedral",
			"description": "a candle falls",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("inject returned IsError; content=%v", res.Content)
	}
	if len(fw.InjectCalls) != 1 {
		t.Fatalf("InjectCalls len: %d", len(fw.InjectCalls))
	}
	got := fw.InjectCalls[0]
	if got.SceneID != api.SceneID("cathedral") || got.Description != "a candle falls" {
		t.Errorf("InjectCall mismatch: %+v", got)
	}
}

func TestE2ESummonUnknownPlaceReturnsToolError(t *testing.T) {
	fw := &fakeWorld{SummonErr: errors.New(`summon: unknown place "void"`)}
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
		Arguments: map[string]any{"place_id": "void"},
	})
	if err != nil {
		t.Fatalf("CallTool returned protocol error %v; want tool error in result", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true on summon failure")
	}
	body := ""
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			body += tc.Text
		}
	}
	if !strings.Contains(body, "unknown place") {
		t.Errorf("error body missing underlying message: %q", body)
	}
}

func TestE2EReadCharactersResource(t *testing.T) {
	fw := &fakeWorld{CharactersReturn: []api.CharacterRef{
		{ID: "vicar", Name: "The Vicar", Blurb: "worried about the draft"},
	}}
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

	res, err := session.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "world://characters"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("Contents len: %d", len(res.Contents))
	}
	text := res.Contents[0].Text
	if !strings.Contains(text, `"id":"vicar"`) {
		t.Errorf("Contents.Text missing lowercase id key: %q", text)
	}
	if !strings.Contains(text, `"blurb":"worried about the draft"`) {
		t.Errorf("Contents.Text missing lowercase blurb key: %q", text)
	}
}
```

This file is now the *only* `adapter_test.go`. Don't append the new functions to the previous version — the file is being overwritten.

- [ ] **Step 11.2: Run — expect green**

Run:
```bash
go test ./internal/mcp/... -run TestE2E -v
```

Expected: all three E2E tests PASS.

- [ ] **Step 11.3: Run the full test suite**

Run:
```bash
go test ./...
```

Expected: PASS (no regressions outside `internal/mcp`).

- [ ] **Step 11.4: Commit**

```bash
git add internal/mcp/adapter_test.go
git commit -m "mcp: e2e protocol tests via InMemoryTransports"
```

---

## Task 12: Wire `--mcp-stdio` flag in cmd/sim

**Files:**
- Modify: `cmd/sim/runtime_config.go`
- Modify: `cmd/sim/runtime_config_test.go`
- Modify: `cmd/sim/main.go`

### Task 12a: Flag plumbing

- [ ] **Step 12a.1: Add option, flag, file-config field**

Edit `cmd/sim/runtime_config.go`. In the `runtimeOptions` struct (around line 17), append a field:

```go
type runtimeOptions struct {
	ConfigPath   string
	DBPath       string
	SeedDir      string
	Tick         time.Duration
	LLMProvider  string
	GeminiModel  string
	GeminiAPIKey string
	IRC          ircOptions
	MCPStdio     bool
}
```

In `fileConfig` (around line 38), append:

```go
	MCP fileMCPConfig `yaml:"mcp"`
```

Below `fileIRCSASLAuth`, add:

```go
type fileMCPConfig struct {
	Stdio *bool `yaml:"stdio"`
}
```

In `runtimeFlagValues` (around line 65), append:

```go
	mcpStdio     *bool
```

In `defaultRuntimeOptions` (unchanged; `MCPStdio` zero-value is `false`).

In `bindRuntimeFlags` (around line 171), append a binding before the closing brace:

```go
		mcpStdio: fs.Bool("mcp-stdio", opts.MCPStdio, "run an MCP server over stdin/stdout instead of IRC"),
```

In `parseRuntimeOptions` (around line 97), after the `explicit["irc-sasl-pass"]` block, add:

```go
	if explicit["mcp-stdio"] {
		opts.MCPStdio = *flags.mcpStdio
	}
```

In `applyConfigFile` (around line 200), after the `IRC:` overrides, add:

```go
	if cfg.MCP.Stdio != nil {
		opts.MCPStdio = *cfg.MCP.Stdio
	}
```

(Place this in the same logical block as the other `cfg.X != nil` overrides at the bottom of `applyConfigFile`.)

- [ ] **Step 12a.2: Write a failing flag-parse test**

Edit `cmd/sim/runtime_config_test.go`. Append:

```go
func TestParseRuntimeOptions_MCPStdioFlag(t *testing.T) {
	opts, err := parseRuntimeOptions([]string{"-mcp-stdio"}, t.TempDir())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !opts.MCPStdio {
		t.Fatal("MCPStdio: got false, want true")
	}
}

func TestParseRuntimeOptions_MCPStdioDefaultsFalse(t *testing.T) {
	opts, err := parseRuntimeOptions(nil, t.TempDir())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.MCPStdio {
		t.Fatal("MCPStdio default: got true, want false")
	}
}
```

- [ ] **Step 12a.3: Run flag-parse tests**

Run:
```bash
go test ./cmd/sim/... -run TestParseRuntimeOptions_MCPStdio -v
```

Expected: both PASS. (If FAIL, fix the binding in Step 12a.1.)

### Task 12b: Wire MCP into runCtx

`Step 12b.1` is split into four sub-steps so each edit is small enough to verify on its own. Build after each.

Also: `runCtx`'s signature changes (gains `mcpStdio bool`), which breaks the existing callsite at `cmd/sim/smoke_test.go:149`. Step 12b.0 patches it first so the build never goes red on that file.

- [ ] **Step 12b.0: Update the runCtx callsite in `cmd/sim/smoke_test.go`**

Open `cmd/sim/smoke_test.go`. Find the call (around line 149-150):

```go
	err := runCtx(ctx, logger, echoLLM{}, echoEmbeddingModelID,
		dbPath, seedDir, 100*time.Millisecond, nil, failingFactory)
```

Replace with:

```go
	err := runCtx(ctx, logger, echoLLM{}, echoEmbeddingModelID,
		dbPath, seedDir, 100*time.Millisecond, nil, false, failingFactory)
```

The new `false` is the `mcpStdio` argument the signature is about to gain.

- [ ] **Step 12b.1a: Add the new imports to `cmd/sim/main.go`**

Open `cmd/sim/main.go`. Find the existing import block (around lines 7-28). Insert two new imports next to the `irc` import; final import list ends like this:

```go
	"github.com/afternet/go-vibebot/internal/api"
	"github.com/afternet/go-vibebot/internal/character"
	"github.com/afternet/go-vibebot/internal/config"
	"github.com/afternet/go-vibebot/internal/irc"
	"github.com/afternet/go-vibebot/internal/llm"
	mcpadapter "github.com/afternet/go-vibebot/internal/mcp"
	"github.com/afternet/go-vibebot/internal/memory"
	"github.com/afternet/go-vibebot/internal/scene"
	"github.com/afternet/go-vibebot/internal/store"
	"github.com/afternet/go-vibebot/internal/world"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
```

Build:
```bash
go build ./...
```
Expected: PASS — `mcpadapter` and `mcpsdk` are imported but not yet referenced; Go will report "imported and not used." This is a *known intermediate red* — both imports are consumed in Step 12b.1c. If you want a green intermediate state, add blank-identifier guards (`var _ = mcpadapter.New; var _ = mcpsdk.StdioTransport{}`) and remove them in 12b.1c. Otherwise, accept the temporary red and proceed.

- [ ] **Step 12b.1b: Update `run` and `runCtx` signatures + the `main` callsite**

Replace the `run` function definition with:

```go
func run(logger *slog.Logger, llmImpl llm.LLM, modelID, dbPath, seedDir string,
	tick time.Duration, ircCfg *irc.Config, mcpStdio bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCtx(ctx, logger, llmImpl, modelID, dbPath, seedDir, tick, ircCfg, mcpStdio, defaultVectorStore)
}
```

Replace the `runCtx` function declaration line with:

```go
func runCtx(ctx context.Context, logger *slog.Logger, llmImpl llm.LLM,
	modelID, dbPath, seedDir string, tick time.Duration, ircCfg *irc.Config,
	mcpStdio bool, vsFactory vectorStoreFactory) error {
```

In `main`, replace the existing `if err := run(logger, model, modelID, ..., ircConfig(opts.IRC, logger)); err != nil {` block with:

```go
	if err := run(logger, model, modelID, opts.DBPath, opts.SeedDir, opts.Tick,
		ircConfig(opts.IRC, logger), opts.MCPStdio); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
```

Build:
```bash
go build ./...
```
Expected: still red (`mcpadapter` / `mcpsdk` unused). That's fine — Step 12b.1c closes the loop.

- [ ] **Step 12b.1c: Replace the IRC dispatch block with the MCP-or-IRC switch**

Inside `runCtx`, find the existing block. Today it reads (from `cmd/sim/main.go:195-219`, exact text):

```go
	var ircErr chan error
	if ircCfg != nil {
		a, err := irc.New(*ircCfg, worldAPI)
		if err != nil {
			return err
		}
		ircErr = make(chan error, 1)
		go func() { ircErr <- a.Run(ctx) }()
		logger.Info("irc adapter dialing", "server", ircCfg.Server)
	} else {
		logger.Info("irc adapter disabled (no -irc-server provided)")
	}

	select {
	case <-ctx.Done():
		<-worldErr
		if ircErr != nil {
			<-ircErr
		}
		return nil
	case err := <-worldErr:
		return err
	case err := <-ircErr:
		return err
	}
}
```

Replace that entire block (from `var ircErr chan error` through the closing `}` of `runCtx`) with:

```go
	if mcpStdio && ircCfg != nil {
		return fmt.Errorf("mcp-stdio and irc-server are mutually exclusive (stdio reserves stdout)")
	}

	var (
		ircErr chan error
		mcpErr chan error
	)
	switch {
	case mcpStdio:
		a, err := mcpadapter.New(mcpadapter.Config{Logger: logger}, worldAPI)
		if err != nil {
			return err
		}
		mcpErr = make(chan error, 1)
		go func() { mcpErr <- a.Run(ctx, &mcpsdk.StdioTransport{}) }()
		logger.Info("mcp adapter serving over stdio")
	case ircCfg != nil:
		a, err := irc.New(*ircCfg, worldAPI)
		if err != nil {
			return err
		}
		ircErr = make(chan error, 1)
		go func() { ircErr <- a.Run(ctx) }()
		logger.Info("irc adapter dialing", "server", ircCfg.Server)
	default:
		logger.Info("no edge adapter enabled (set -irc-server or -mcp-stdio)")
	}

	select {
	case <-ctx.Done():
		<-worldErr
		if ircErr != nil {
			<-ircErr
		}
		if mcpErr != nil {
			<-mcpErr
		}
		return nil
	case err := <-worldErr:
		return err
	case err := <-ircErr:
		return err
	case err := <-mcpErr:
		return err
	}
}
```

(Note on Go semantics: a `case err := <-ircErr` against a nil `ircErr` channel is silently skipped — it does not panic and does not race. So whichever adapter is *not* selected leaves its channel nil and that case stays dormant. Confirmed valid pattern.)

- [ ] **Step 12b.1d: Redirect help output to stderr (eliminates the Step 0.3 footgun)**

In the `main` function, change:

```go
		printRuntimeUsage(os.Stdout)
```

to:

```go
		printRuntimeUsage(os.Stderr)
```

This keeps stdout free of *all* writes from the binary's boot path, so `--mcp-stdio --help` (which would otherwise print usage to the JSON-RPC channel) is safe.

- [ ] **Step 12b.2: Build, expect green**

Run:
```bash
go build ./...
```

Expected: no errors. If errors, one of 12b.1a–12b.1d was incomplete; the error message names the file.

- [ ] **Step 12b.3: Run the full test suite**

Run:
```bash
go test ./...
```

Expected: PASS. The `cmd/sim` smoke test must still pass — its `runCtx` callsite was patched in Step 12b.0 and does not exercise the MCP path (passes `false` for `mcpStdio`).

- [ ] **Step 12b.4: Commit**

```bash
git add cmd/sim/runtime_config.go cmd/sim/runtime_config_test.go cmd/sim/main.go cmd/sim/smoke_test.go
git commit -m "sim: add --mcp-stdio flag, run mcp adapter mutually-exclusive with irc"
```

---

## Task 13: Documentation + backlog updates

**Files:**
- Modify: `README.md`
- Modify: `BACKLOG.md`

- [ ] **Step 13.1: README — point readers at the MCP mode**

Read `README.md`'s "Adapter pattern at the edges" section. Find the paragraph that names the IRC adapter. Append after it:

```markdown
The MCP adapter is the second edge. Run `go-vibebot --mcp-stdio --seed ./seed` and any
MCP-speaking client (Claude Desktop, Claude Code, Cursor) can issue `inject`,
`nudge`, `summon`, and `log` as tool calls, and read `world://characters`,
`world://places`, and `world://log` as resources. The MCP and IRC adapters are
mutually exclusive in a single process — stdio reserves stdout for JSON-RPC.
```

Also: if the README has a "Deliberately deferred" list that names "Tool-call adapter for LLM users" as open, remove that bullet (it just shipped).

- [ ] **Step 13.2: BACKLOG — strike L3 with a SHIPPED note**

Edit `BACKLOG.md`. Replace the `### L3. Tool-call adapter for LLM users` heading and its body with a SHIPPED block in the same style as L1/L2:

```markdown
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
```

Update the status table at the top of BACKLOG.md too: change `Tool-call adapter for LLM users | Open. See plan below.` to `Tool-call adapter for LLM users | **Shipped 2026-05-12.** See L3 below.`

- [ ] **Step 13.3: Final full suite + commit**

Run:
```bash
go test ./...
go build ./...
```

Expected: both succeed.

```bash
git add README.md BACKLOG.md
git commit -m "docs: mark L3 MCP adapter shipped; document --mcp-stdio in README"
```

---

## Post-implementation manual verification (optional but recommended)

Not part of the plan's automated coverage; do these by hand to sanity-check.

- [ ] **M.1: Smoke the binary**

Build and run with the flag, send an `initialize` JSON-RPC frame on stdin:

```bash
go build -o /tmp/vibebot ./cmd/sim
echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"manual","version":"v0"}}}' | \
  /tmp/vibebot --mcp-stdio --db :memory: --seed ./seed
```

Expected: a JSON `result` frame on stdout containing `"name":"go-vibebot","version":"v0"`. Process should not crash on EOF; ctrl-c kills it cleanly.

- [ ] **M.2: Wire into Claude Desktop / Claude Code**

Add to the host's MCP config (path varies by client). Example for Claude Code's `mcpServers` block:

```json
{
  "go-vibebot": {
    "command": "/path/to/go-vibebot",
    "args": ["--mcp-stdio", "--seed", "/path/to/seed", "--db", ":memory:"]
  }
}
```

Restart the host; verify the `inject` / `nudge` / `summon` / `log` tools appear, and reading `world://characters` returns JSON.
