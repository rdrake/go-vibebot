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
