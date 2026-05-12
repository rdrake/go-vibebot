package mcp

import (
	"context"
	"encoding/json"
	"fmt"

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
