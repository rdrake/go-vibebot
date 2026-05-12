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
