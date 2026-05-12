package mcp

import (
	"context"
	"fmt"
	"time"

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

// toolError builds a CallToolResult with IsError=true and the message
// packed into a single TextContent block. Use for WorldAPI errors and
// input validation — never for protocol-level breaks (return Go error).
func toolError(msg string) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: msg}},
	}
}

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
