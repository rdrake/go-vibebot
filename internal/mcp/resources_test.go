package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
