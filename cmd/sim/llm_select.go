package main

import (
	"errors"
	"fmt"

	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/llm/gemini"
)

// selectLLM returns the llm.LLM implementation requested by --llm along with
// its embedding model identifier (used to scope persisted vector rows).
//
// configKey is the API key resolved from runtimeOptions (config file or
// --gemini-api-key flag, with GEMINI_API_KEY env already merged in).
func selectLLM(provider, geminiModel, configKey string) (llm.LLM, string, error) {
	switch provider {
	case "echo":
		return echoLLM{}, echoEmbeddingModelID, nil
	case "gemini":
		if configKey == "" {
			return nil, "", errors.New("gemini provider requires an API key (set GEMINI_API_KEY, --gemini-api-key, or gemini_api_key in config)")
		}
		g := gemini.New(configKey)
		if geminiModel != "" {
			g.Model = geminiModel
		}
		return g, gemini.EmbeddingModelID, nil
	default:
		return nil, "", fmt.Errorf("unknown --llm provider %q (want: echo, gemini)", provider)
	}
}
