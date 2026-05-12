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
