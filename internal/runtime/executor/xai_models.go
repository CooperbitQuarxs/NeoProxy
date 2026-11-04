package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// XAIListedModel is one entry of an xAI /v1/models listing.
type XAIListedModel struct {
	ID string
	// Name and ContextWindow are reported by the Grok CLI chat-proxy only.
	Name          string
	ContextWindow int
}

// FetchXAIModels lists the models an xAI OAuth credential can use at the
// endpoint its provider routes chat to: the official API for xai, the Grok CLI
// chat-proxy (with its identity headers) for grok-build.
func FetchXAIModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]XAIListedModel, error) {
	token, _ := xaiCreds(auth)
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("xai models: credential has no access token")
	}
	url := strings.TrimSuffix(xaiChatBaseURL(auth), "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	applyXAIChatHeaders(req, auth, token, false, "")
	resp, err := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xai models: %s returned HTTP %d", url, resp.StatusCode)
	}
	var listing struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextWindow int    `json:"context_window"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&listing); err != nil {
		return nil, fmt.Errorf("xai models: decode %s: %w", url, err)
	}
	out := make([]XAIListedModel, 0, len(listing.Data))
	for _, m := range listing.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			out = append(out, XAIListedModel{ID: id, Name: strings.TrimSpace(m.Name), ContextWindow: m.ContextWindow})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("xai models: %s listed no models", url)
	}
	return out, nil
}
