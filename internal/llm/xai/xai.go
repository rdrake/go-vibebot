// Package xai is a thin OpenAI-compatible REST client for xAI chat models.
package xai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/llm/httpjson"
)

// DefaultEndpoint is xAI's OpenAI-compatible API base URL.
const DefaultEndpoint = "https://api.x.ai"

// DefaultModel is the Grok model used for live character generation.
const DefaultModel = "grok-4-1-fast-reasoning"

// Provider implements text completion via xAI Chat Completions.
type Provider struct {
	APIKey     string
	Model      string
	Endpoint   string
	HTTPClient *http.Client
}

// New returns a Provider with production defaults.
func New(apiKey string) *Provider {
	return &Provider{
		APIKey:     apiKey,
		Model:      DefaultModel,
		Endpoint:   DefaultEndpoint,
		HTTPClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// Complete sends a non-streaming Chat Completions request.
func (p *Provider) Complete(ctx context.Context, req llm.CompleteRequest) (string, error) {
	if p.APIKey == "" {
		return "", errors.New("xai: APIKey is empty")
	}
	body := buildChatRequest(p.model(), req)
	raw, status, err := p.post(ctx, "/v1/chat/completions", body)
	if err != nil {
		return "", err
	}
	var resp chatResp
	if jerr := json.Unmarshal(raw, &resp); jerr != nil {
		return "", fmt.Errorf("xai: decode response (status %d): %w", status, jerr)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("xai: %s: %s", resp.Error.Type, resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("xai: empty choices (status %d)", status)
	}
	out := strings.TrimSpace(resp.Choices[0].Message.Content)
	if out == "" {
		return "", fmt.Errorf("xai: empty message content (status %d)", status)
	}
	return out, nil
}

// EmbedText is intentionally unsupported: xAI is used here for generation,
// while cmd/sim composes it with Gemini or local embeddings for memory.
func (p *Provider) EmbedText(context.Context, string) ([]float32, error) {
	return nil, errors.New("xai: embeddings are not supported by this provider")
}

func (p *Provider) model() string {
	if p.Model == "" {
		return DefaultModel
	}
	return p.Model
}

func (p *Provider) post(ctx context.Context, path string, body any) ([]byte, int, error) {
	endpoint := strings.TrimRight(p.Endpoint, "/")
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	headers := map[string]string{"Authorization": "Bearer " + p.APIKey}
	raw, status, err := httpjson.Post(ctx, p.HTTPClient, endpoint+path, headers, body)
	if err != nil {
		return raw, status, fmt.Errorf("xai: %w", err)
	}
	return raw, status, nil
}

func buildChatRequest(model string, req llm.CompleteRequest) chatReq {
	out := chatReq{
		Model:    model,
		Messages: make([]chatMessage, 0, len(req.Messages)+1),
		Stream:   false,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		role := "user"
		switch m.Role {
		case llm.RoleSystem:
			role = "system"
		case llm.RoleAssistant:
			role = "assistant"
		case llm.RoleUser:
			role = "user"
		}
		out.Messages = append(out.Messages, chatMessage{Role: role, Content: m.Content})
	}
	if req.MaxTokens > 0 {
		out.MaxTokens = ptr(req.MaxTokens)
	}
	if req.Temperature > 0 {
		out.Temperature = ptr(req.Temperature)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

type chatReq struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Temperature *float32      `json:"temperature,omitempty"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResp struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *apiError `json:"error,omitempty"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}
