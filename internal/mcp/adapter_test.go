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
