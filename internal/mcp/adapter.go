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
	mcpsdk.AddTool(a.server,
		&mcpsdk.Tool{
			Name:        "summon",
			Description: "Open a place scene so its NPCs become reachable. Errors if the place is not loaded.",
		},
		a.summonHandler,
	)
}
func (a *Adapter) registerResources() {}
