package anthropicnpi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// DiscoveredModel is the provider-owned roster; capability metadata is resolved
// separately from NeoPi's bundled catalog, just as anthropicModelManagerOptions.
type DiscoveredModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ModelDiscoveryError struct{ Status int }

func (e *ModelDiscoveryError) Error() string {
	return fmt.Sprintf("Claude models endpoint returned HTTP %d", e.Status)
}

// Models follows NeoPi's Anthropic discovery request, including OAuth tokens
// entered as API keys. The upstream endpoint is intentionally not paginated:
// NeoPi's shared OpenAI-compatible discovery makes exactly this one GET.
func (c *Client) Models(ctx context.Context, token, baseURL string) ([]DiscoveredModel, error) {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}
	if !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("invalid Claude models endpoint")
	}
	fetchHeaders(request)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	request.Header.Set("anthropic-beta", UsageBetas)
	if strings.Contains(token, "sk-ant-oat") {
		request.Header.Set("Authorization", "Bearer "+token)
	} else {
		request.Header.Set("x-api-key", token)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Claude models endpoint unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &ModelDiscoveryError{Status: response.StatusCode}
	}
	var payload any
	if err = decodeJSON(response, &payload); err != nil {
		return nil, fmt.Errorf("Claude models response is not valid JSON")
	}
	entries, ok := modelEntries(payload)
	if !ok {
		return nil, fmt.Errorf("Claude models response has no model list")
	}
	byID := make(map[string]DiscoveredModel, len(entries))
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, ok := entry["id"].(string)
		if !ok || id == "" {
			continue
		}
		name, hasName := entry["name"]
		if hasName && name != nil {
			if _, ok := name.(string); !ok {
				continue
			}
		}
		display := id
		if value, ok := name.(string); ok && value != "" {
			display = value
		}
		if value, ok := entry["display_name"].(string); ok {
			display = value
		}
		byID[id] = DiscoveredModel{ID: id, Name: display}
	}
	models := make([]DiscoveredModel, 0, len(byID))
	for _, model := range byID {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func modelEntries(node any) ([]any, bool) {
	if entries, ok := node.([]any); ok {
		return entries, true
	}
	if envelope, ok := node.(map[string]any); ok {
		for _, key := range []string{"data", "models", "result", "items"} {
			if entries, ok := modelEntries(envelope[key]); ok {
				return entries, true
			}
		}
	}
	return nil, false
}
