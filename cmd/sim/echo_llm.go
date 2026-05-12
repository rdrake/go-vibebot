package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/afternet/go-vibebot/internal/llm"
)

// echoLLM is a non-network LLM used only to make the walking skeleton
// runnable without API credentials. It synthesizes plausible-sounding
// text from the prompt so the pipeline can be observed end-to-end.
//
// It lives in cmd/sim — not internal/llm — to keep the substrate stub-free.
type echoLLM struct{}

func (echoLLM) Complete(_ context.Context, req llm.CompleteRequest) (string, error) {
	role := "(unknown)"
	if req.System != "" {
		// Extract the "You are X." segment for flavor.
		if i := strings.Index(req.System, "You are "); i >= 0 {
			tail := req.System[i+len("You are "):]
			if j := strings.IndexAny(tail, ".,\n"); j > 0 {
				role = tail[:j]
			}
		}
	}
	last := ""
	if n := len(req.Messages); n > 0 {
		last = req.Messages[n-1].Content
	}
	last = strings.TrimSpace(strings.ReplaceAll(last, "\n", " "))
	if len(last) > 160 {
		last = last[:160] + "…"
	}
	return fmt.Sprintf("%s mutters: %q", role, last), nil
}

func (echoLLM) EmbedText(_ context.Context, text string) ([]float32, error) {
	// Deterministic 8-d "embedding" derived from FNV hash bytes, so tests
	// and ranking experiments can sanity-check shape without a real model.
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	sum := h.Sum64()
	out := make([]float32, 8)
	for i := range out {
		out[i] = float32(byte(sum>>(8*uint(i)))) / 255.0
	}
	return out, nil
}
