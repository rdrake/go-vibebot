// Package gemini is a thin REST client that implements llm.LLM against
// Google's Generative Language API (generativelanguage.googleapis.com).
//
// It deliberately avoids the official SDK to keep the dependency footprint
// small and to make the wire shape transparent for tests.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/afternet/go-vibebot/internal/llm"
)

// DefaultEndpoint is the public Generative Language API base.
const DefaultEndpoint = "https://generativelanguage.googleapis.com"

// DefaultModel is the rolling-latest flash-lite alias (cheapest, fast).
const DefaultModel = "gemini-flash-lite-latest"

// DefaultEmbeddingModel is Google's current text-embedding model.
const DefaultEmbeddingModel = "text-embedding-004"

// EmbeddingModelID is the stable namespaced identifier persisted alongside
// each embedding row. It is NOT the wire-level model string (that lives in
// DefaultEmbeddingModel) — the "gemini:" prefix exists so multiple providers
// can never collide on the same model name. Change this constant when the
// underlying embedding model changes; existing rows with stale IDs will be
// filtered out at hydrate time until the operator deletes them.
const EmbeddingModelID = "gemini:text-embedding-004"

// Provider implements llm.LLM by calling Gemini's REST endpoints.
type Provider struct {
	APIKey         string
	Model          string
	EmbeddingModel string
	Endpoint       string
	HTTPClient     *http.Client
}

// New returns a Provider with sensible defaults. The API key is required;
// callers should source it from an env var, not flags (process listings).
func New(apiKey string) *Provider {
	return &Provider{
		APIKey:         apiKey,
		Model:          DefaultModel,
		EmbeddingModel: DefaultEmbeddingModel,
		Endpoint:       DefaultEndpoint,
		HTTPClient:     &http.Client{Timeout: 30 * time.Second},
	}
}

// Complete sends a single-turn completion request.
func (p *Provider) Complete(ctx context.Context, req llm.CompleteRequest) (string, error) {
	if p.APIKey == "" {
		return "", errors.New("gemini: APIKey is empty")
	}
	body := buildCompleteRequest(req)
	raw, status, err := p.post(ctx, p.Model, "generateContent", body)
	if err != nil {
		return "", err
	}
	var resp completeResp
	if jerr := json.Unmarshal(raw, &resp); jerr != nil {
		return "", fmt.Errorf("gemini: decode response (status %d): %w", status, jerr)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("gemini: %s: %s", resp.Error.Status, resp.Error.Message)
	}
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("gemini: empty candidate set (status %d)", status)
	}
	return resp.Candidates[0].Content.Parts[0].Text, nil
}

// EmbedText returns the embedding vector for a single text input.
func (p *Provider) EmbedText(ctx context.Context, text string) ([]float32, error) {
	if p.APIKey == "" {
		return nil, errors.New("gemini: APIKey is empty")
	}
	body := embedReq{Content: content{Parts: []part{{Text: text}}}}
	raw, status, err := p.post(ctx, p.EmbeddingModel, "embedContent", body)
	if err != nil {
		return nil, err
	}
	var resp embedResp
	if jerr := json.Unmarshal(raw, &resp); jerr != nil {
		return nil, fmt.Errorf("gemini: decode embed response (status %d): %w", status, jerr)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("gemini: %s: %s", resp.Error.Status, resp.Error.Message)
	}
	return resp.Embedding.Values, nil
}

func (p *Provider) post(ctx context.Context, model, action string, body any) ([]byte, int, error) {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	httpClient := p.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	target := fmt.Sprintf("%s/v1beta/models/%s:%s?key=%s",
		endpoint, model, action, url.QueryEscape(p.APIKey))

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("gemini: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(buf))
	if err != nil {
		return nil, 0, fmt.Errorf("gemini: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, fmt.Errorf("gemini: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("gemini: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return raw, resp.StatusCode, fmt.Errorf("gemini: HTTP %d: %s", resp.StatusCode, string(raw))
	}
	return raw, resp.StatusCode, nil
}

func buildCompleteRequest(req llm.CompleteRequest) completeReq {
	out := completeReq{
		Contents:         make([]content, 0, len(req.Messages)),
		GenerationConfig: &generationConfig{Temperature: ptr(req.Temperature)},
	}
	if req.System != "" {
		out.SystemInstruction = &content{Parts: []part{{Text: req.System}}}
	}
	if req.MaxTokens > 0 {
		out.GenerationConfig.MaxOutputTokens = ptr(req.MaxTokens)
	}
	for _, m := range req.Messages {
		var role string
		switch m.Role {
		case llm.RoleSystem:
			// Already handled via system_instruction; skip duplicates.
			continue
		case llm.RoleAssistant:
			role = "model"
		case llm.RoleUser:
			role = "user"
		default:
			role = "user"
		}
		out.Contents = append(out.Contents, content{
			Role:  role,
			Parts: []part{{Text: m.Content}},
		})
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// --- wire types ---

type completeReq struct {
	SystemInstruction *content          `json:"system_instruction,omitempty"`
	Contents          []content         `json:"contents"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text string `json:"text"`
}

type generationConfig struct {
	Temperature     *float32 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

type completeResp struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	Error *apiError `json:"error,omitempty"`
}

type embedReq struct {
	Content content `json:"content"`
}

type embedResp struct {
	Embedding struct {
		Values []float32 `json:"values"`
	} `json:"embedding"`
	Error *apiError `json:"error,omitempty"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}
