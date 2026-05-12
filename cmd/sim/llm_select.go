package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/afternet/go-vibebot/internal/llm"
	"github.com/afternet/go-vibebot/internal/llm/gemini"
)

// selectLLM returns the llm.LLM implementation requested by --llm along with
// its embedding model identifier (used to scope persisted vector rows).
func selectLLM(provider, geminiModel string) (llm.LLM, string, error) {
	switch provider {
	case "echo":
		return echoLLM{}, echoEmbeddingModelID, nil
	case "gemini":
		key := os.Getenv("GEMINI_API_KEY")
		if key == "" {
			return nil, "", errors.New("set GEMINI_API_KEY to use --llm=gemini")
		}
		g := gemini.New(key)
		if geminiModel != "" {
			g.Model = geminiModel
		}
		return g, gemini.EmbeddingModelID, nil
	default:
		return nil, "", fmt.Errorf("unknown --llm provider %q (want: echo, gemini)", provider)
	}
}
