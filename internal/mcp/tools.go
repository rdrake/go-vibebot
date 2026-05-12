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
