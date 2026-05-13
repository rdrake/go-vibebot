// Package httpjson is the shared JSON-over-HTTP plumbing used by the
// REST-based llm.LLM providers. Provider packages wrap the returned
// errors with their own prefix so caller-facing messages stay namespaced.
package httpjson

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Post marshals body as JSON and POSTs it to url. Non-2xx responses still
// return the body so callers can decode provider-specific error shapes.
// A nil client falls back to http.DefaultClient.
func Post(ctx context.Context, client *http.Client, url string, headers map[string]string, body any) ([]byte, int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return raw, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}
	return raw, resp.StatusCode, nil
}
