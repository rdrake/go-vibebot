package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	uriCharacters = "world://characters"
	uriPlaces     = "world://places"
	uriLogStatic  = "world://log"
	uriLogTmpl    = "world://log{?since,scene}"
	mimeJSON      = "application/json"
)

func (a *Adapter) charactersHandler(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	refs, err := a.api.Characters(ctx)
	if err != nil {
		return nil, fmt.Errorf("characters: %w", err)
	}
	body, err := json.Marshal(refs)
	if err != nil {
		return nil, fmt.Errorf("characters: marshal: %w", err)
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{
			URI:      uriCharacters,
			MIMEType: mimeJSON,
			Text:     string(body),
		}},
	}, nil
}

func (a *Adapter) placesHandler(ctx context.Context, _ *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	refs, err := a.api.Places(ctx)
	if err != nil {
		return nil, fmt.Errorf("places: %w", err)
	}
	body, err := json.Marshal(refs)
	if err != nil {
		return nil, fmt.Errorf("places: marshal: %w", err)
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{
			URI:      uriPlaces,
			MIMEType: mimeJSON,
			Text:     string(body),
		}},
	}, nil
}

func (a *Adapter) logResourceHandler(ctx context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
	requestedURI := ""
	if req != nil && req.Params != nil {
		requestedURI = req.Params.URI
	}
	since, scene, err := parseLogQuery(requestedURI)
	if err != nil {
		return nil, err
	}
	entries, err := a.api.Log(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("log: %w", err)
	}
	out := struct {
		Since   string         `json:"since"`
		Entries []LogEntryJSON `json:"entries"`
	}{
		Since:   since.String(),
		Entries: make([]LogEntryJSON, 0, len(entries)),
	}
	for _, e := range entries {
		if scene != "" && string(e.SceneID) != scene {
			continue
		}
		out.Entries = append(out.Entries, LogEntryJSON{
			Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
			SceneID:   string(e.SceneID),
			Actor:     e.Actor,
			Kind:      e.Kind,
			Text:      e.Text,
		})
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("log: marshal: %w", err)
	}
	uriOut := requestedURI
	if uriOut == "" {
		uriOut = uriLogStatic
	}
	return &mcpsdk.ReadResourceResult{
		Contents: []*mcpsdk.ResourceContents{{
			URI:      uriOut,
			MIMEType: mimeJSON,
			Text:     string(body),
		}},
	}, nil
}

// parseLogQuery extracts ?since and ?scene from the resolved URI. Missing
// since uses 1h. Empty scene means no filter. Unparseable since returns
// an error so the resource read fails (not a tool-error path).
func parseLogQuery(raw string) (time.Duration, string, error) {
	since := time.Hour
	scene := ""
	if raw == "" {
		return since, scene, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return 0, "", fmt.Errorf("log: invalid uri %q: %w", raw, err)
	}
	q := u.Query()
	if s := q.Get("since"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return 0, "", fmt.Errorf("log: invalid since %q: %w", s, err)
		}
		since = d
	}
	if s := q.Get("scene"); s != "" {
		scene = s
	}
	return since, scene, nil
}
