package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/llm/gemini"
	"github.com/afternet/go-vibebot/internal/llm/xai"
)

// selectLLM returns the llm.LLM implementation requested by --llm along with
// its embedding model identifier (used to scope persisted vector rows).
func selectLLM(provider, geminiModel, geminiKey, xaiModel, xaiKey string) (llm.LLM, string, error) {
	switch provider {
	case "echo":
		return echoLLM{}, echoEmbeddingModelID, nil
	case "gemini":
		if geminiKey == "" {
			return nil, "", errors.New("gemini provider requires an API key (set GEMINI_API_KEY, --gemini-api-key, or gemini_api_key in config)")
		}
		g := gemini.New(geminiKey)
		if geminiModel != "" {
			g.Model = geminiModel
		}
		return g, gemini.EmbeddingModelID, nil
	case "xai":
		if xaiKey == "" {
			return nil, "", errors.New("xai provider requires an API key (set XAI_API_KEY, --xai-api-key, or xai_api_key in config)")
		}
		gen := xai.New(xaiKey)
		if xaiModel != "" {
			gen.Model = xaiModel
		}
		if geminiKey != "" {
			embed := gemini.New(geminiKey)
			return splitLLM{generator: gen, embedder: embed}, gemini.EmbeddingModelID, nil
		}
		return splitLLM{generator: gen, embedder: echoLLM{}}, echoEmbeddingModelID, nil
	default:
		return nil, "", fmt.Errorf("unknown --llm provider %q (want: echo, gemini, xai)", provider)
	}
}

type splitLLM struct {
	generator llm.LLM
	embedder  llm.LLM
}

func (s splitLLM) Complete(ctx context.Context, req llm.CompleteRequest) (string, error) {
	return s.generator.Complete(ctx, req)
}

func (s splitLLM) EmbedText(ctx context.Context, text string) ([]float32, error) {
	return s.embedder.EmbedText(ctx, text)
}
