package xai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/afternet/go-vibebot/internal/llm"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestCompleteWireShape(t *testing.T) {
	var captured chatReq
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path=%s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer k1" {
			t.Errorf("authorization=%q", got)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type=%s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello back"}}]}`)
	})

	p := New("k1")
	p.Endpoint = srv.URL
	got, err := p.Complete(context.Background(), llm.CompleteRequest{
		System:      "you are trouble",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}, {Role: llm.RoleAssistant, Content: "yes?"}, {Role: llm.RoleUser, Content: "ack"}},
		Temperature: 0.9,
		MaxTokens:   77,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got != "hello back" {
		t.Errorf("got=%q", got)
	}
	if captured.Model != DefaultModel {
		t.Errorf("model=%q", captured.Model)
	}
	if captured.Stream {
		t.Error("stream=true, want false")
	}
	if captured.Temperature == nil || *captured.Temperature != 0.9 {
		t.Errorf("temperature=%v", captured.Temperature)
	}
	if captured.MaxTokens == nil || *captured.MaxTokens != 77 {
		t.Errorf("max_tokens=%v", captured.MaxTokens)
	}
	if len(captured.Messages) != 4 {
		t.Fatalf("want system + 3 messages, got %d", len(captured.Messages))
	}
	if captured.Messages[0].Role != "system" || captured.Messages[0].Content != "you are trouble" {
		t.Errorf("system message=%+v", captured.Messages[0])
	}
	if captured.Messages[1].Role != "user" || captured.Messages[2].Role != "assistant" || captured.Messages[3].Role != "user" {
		t.Errorf("role mapping wrong: %+v", captured.Messages)
	}
}

func TestCompleteHTTPError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key","type":"auth"}}`)
	})
	p := New("k1")
	p.Endpoint = srv.URL
	_, err := p.Complete(context.Background(), llm.CompleteRequest{})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want HTTP 401 error, got %v", err)
	}
}

func TestCompleteAPIErrorIn200(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit"}}`)
	})
	p := New("k1")
	p.Endpoint = srv.URL
	_, err := p.Complete(context.Background(), llm.CompleteRequest{})
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("want API error, got %v", err)
	}
}

func TestMissingAPIKey(t *testing.T) {
	p := &Provider{}
	if _, err := p.Complete(context.Background(), llm.CompleteRequest{}); err == nil {
		t.Fatal("expected error on empty key")
	}
}
