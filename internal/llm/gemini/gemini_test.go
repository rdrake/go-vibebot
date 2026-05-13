package gemini

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
	var captured completeReq
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
		}
		wantPath := "/v1beta/models/gemini-flash-lite-latest:generateContent"
		if r.URL.Path != wantPath {
			t.Errorf("path=%s want %s", r.URL.Path, wantPath)
		}
		if k := r.URL.Query().Get("key"); k != "k1" {
			t.Errorf("key=%s", k)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type=%s", ct)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello back"}]}}]}`)
	})

	p := New("k1")
	p.Endpoint = srv.URL

	got, err := p.Complete(context.Background(), llm.CompleteRequest{
		System:      "you are a parrot",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: "hi"}, {Role: llm.RoleAssistant, Content: "yes?"}, {Role: llm.RoleUser, Content: "ack"}},
		Temperature: 0.4,
		MaxTokens:   50,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got != "hello back" {
		t.Errorf("got=%q", got)
	}

	if captured.SystemInstruction == nil || captured.SystemInstruction.Parts[0].Text != "you are a parrot" {
		t.Errorf("system_instruction not sent: %+v", captured.SystemInstruction)
	}
	if len(captured.Contents) != 3 {
		t.Fatalf("want 3 contents, got %d", len(captured.Contents))
	}
	if captured.Contents[0].Role != "user" || captured.Contents[1].Role != "model" || captured.Contents[2].Role != "user" {
		t.Errorf("role mapping wrong: %+v", captured.Contents)
	}
	if captured.GenerationConfig == nil || captured.GenerationConfig.Temperature == nil || *captured.GenerationConfig.Temperature != 0.4 {
		t.Errorf("temperature missing or wrong: %+v", captured.GenerationConfig)
	}
	if captured.GenerationConfig.MaxOutputTokens == nil || *captured.GenerationConfig.MaxOutputTokens != 50 {
		t.Errorf("maxOutputTokens missing or wrong: %+v", captured.GenerationConfig)
	}
}

func TestCompleteHTTPError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":403,"message":"API key invalid","status":"PERMISSION_DENIED"}}`)
	})
	p := New("k1")
	p.Endpoint = srv.URL
	_, err := p.Complete(context.Background(), llm.CompleteRequest{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("err missing status: %v", err)
	}
}

func TestCompleteAPIErrorIn200(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"error":{"code":429,"message":"rate limited","status":"RESOURCE_EXHAUSTED"}}`)
	})
	p := New("k1")
	p.Endpoint = srv.URL
	_, err := p.Complete(context.Background(), llm.CompleteRequest{})
	if err == nil || !strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") {
		t.Errorf("want rate-limit error, got %v", err)
	}
}

func TestCompleteEmptyCandidates(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"candidates":[]}`)
	})
	p := New("k1")
	p.Endpoint = srv.URL
	_, err := p.Complete(context.Background(), llm.CompleteRequest{})
	if err == nil || !strings.Contains(err.Error(), "empty candidate") {
		t.Errorf("want empty-candidate error, got %v", err)
	}
}

func TestEmbedTextWireShape(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/v1beta/models/gemini-embedding-001:embedContent"
		if r.URL.Path != wantPath {
			t.Errorf("path=%s want %s", r.URL.Path, wantPath)
		}
		_, _ = io.WriteString(w, `{"embedding":{"values":[0.1,0.2,0.3]}}`)
	})
	p := New("k1")
	p.Endpoint = srv.URL
	got, err := p.EmbedText(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 0.1 {
		t.Errorf("embedding=%v", got)
	}
}

func TestMissingAPIKey(t *testing.T) {
	p := &Provider{}
	if _, err := p.Complete(context.Background(), llm.CompleteRequest{}); err == nil {
		t.Error("expected error on empty key")
	}
	if _, err := p.EmbedText(context.Background(), "x"); err == nil {
		t.Error("expected error on empty key")
	}
}
