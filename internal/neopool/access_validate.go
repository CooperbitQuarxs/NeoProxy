package neopool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

// accessHTTPClient validates keys. It never follows redirects (a key must not be
// replayed to another host) and never surfaces upstream bodies, which can echo
// partial keys or private account details.
var accessHTTPClient = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

const maxValidatedModels = 2000

// listUpstreamModels proves a key works by listing the models it can use.
func listUpstreamModels(ctx context.Context, style, base, key string) ([]string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, accessErr(http.StatusBadRequest, "The base URL must be an http(s) address such as http://localhost:11434/v1, without credentials or a query string.")
	}
	var target string
	header := http.Header{}
	switch style {
	case "anthropic":
		target = base + "/v1/models?limit=1000"
		header.Set("x-api-key", key)
		header.Set("anthropic-version", "2023-06-01")
	case "gemini":
		target = base + "/v1beta/models?pageSize=1000"
		header.Set("x-goog-api-key", key)
	default:
		target = base + "/models"
		if key != "" {
			header.Set("Authorization", "Bearer "+key)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, accessErr(http.StatusBadRequest, "The base URL is not a valid address.")
	}
	req.Header = header
	req.Header.Set("Accept", "application/json")
	// Some providers sit behind bot protection that rejects Go's default
	// User-Agent with a 403 page (Nous Portal does), which would read as a bad key.
	req.Header.Set("User-Agent", "NeoProxy")
	resp, err := accessHTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout") {
			return nil, accessErr(http.StatusBadGateway, "%s did not answer within 20 seconds. Check the address and that the server is running, then try again.", parsed.Host)
		}
		return nil, accessErr(http.StatusBadGateway, "Could not reach %s. Check the address and that the server is running, then try again.", parsed.Host)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &AccessError{Status: http.StatusBadRequest, keyRejected: true, Message: fmt.Sprintf("The provider rejected this key (HTTP %d). Check that the whole key was pasted and that it is still active.", resp.StatusCode)}
	case resp.StatusCode == http.StatusNotFound:
		return nil, accessErr(http.StatusBadRequest, "%s has no model list at %s. Check the base URL; OpenAI-compatible servers usually end in /v1.", parsed.Host, parsed.Path+"/models")
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, accessErr(http.StatusBadRequest, "%s redirected the model list request (HTTP %d). Use the final address as the base URL.", parsed.Host, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, accessErr(http.StatusServiceUnavailable, "%s is rate-limiting requests right now (HTTP 429); the key itself was not rejected. Wait a minute and try again.", parsed.Host)
	case resp.StatusCode != http.StatusOK:
		return nil, accessErr(http.StatusBadGateway, "The provider answered HTTP %d when listing models. Try again in a moment, or check the provider's status page.", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, accessErr(http.StatusBadGateway, "The model list from %s could not be read. Try again in a moment.", parsed.Host)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"models"`
	}
	if err = json.Unmarshal(data, &body); err != nil {
		return nil, accessErr(http.StatusBadGateway, "%s answered, but not with a model list. Check that the base URL points at the API, not a web page.", parsed.Host)
	}
	seen := map[string]bool{}
	models := []string{}
	add := func(id string) {
		id = strings.TrimPrefix(strings.TrimSpace(id), "models/")
		if id != "" && !seen[id] && len(models) < maxValidatedModels {
			seen[id] = true
			models = append(models, id)
		}
	}
	for _, m := range body.Data {
		add(m.ID)
	}
	for _, m := range body.Models {
		add(firstNonEmpty(m.ID, m.Name))
	}
	if len(models) == 0 {
		return nil, accessErr(http.StatusBadRequest, "The provider accepted the key but listed no models for it. Check the key's project or permissions.")
	}
	sort.Strings(models)
	return models, nil
}

// validateCatalogKey lists the models a key can use at base and returns the
// endpoint that accepted it. A provider with regional endpoints is tried region
// by region (preferred first, when it is one of them), because its keys are
// region-locked. Models the provider lists but chat clients cannot use are
// dropped so they never reach the model list.
func validateCatalogKey(ctx context.Context, info ProviderInfo, base, preferred, key string) (string, []string, error) {
	candidates := []string{base}
	if regions := catalogRegions(info.ID); len(regions) > 0 {
		preferred = strings.TrimRight(strings.TrimSpace(preferred), "/")
		if preferred != "" && !slices.Contains(regions, preferred) {
			return "", nil, accessErr(http.StatusBadRequest, "%s keys work only at its regional endpoints (%s); %s is not one of them.", info.Name, strings.Join(regions, ", "), preferred)
		}
		candidates = make([]string, 0, len(regions))
		if preferred != "" {
			candidates = append(candidates, preferred)
		}
		for _, region := range regions {
			if region != preferred {
				candidates = append(candidates, region)
			}
		}
	}
	var models []string
	var err error
	for _, candidate := range candidates {
		if models, err = listUpstreamModels(ctx, info.APIKey.Style, candidate, key); err == nil {
			base = candidate
			break
		}
		var rejected *AccessError
		if !errors.As(err, &rejected) || !rejected.keyRejected {
			return "", nil, err
		}
	}
	if err != nil {
		if len(candidates) > 1 {
			return "", nil, accessErr(http.StatusBadRequest, "%s rejected this key in every region. Check that the whole key was pasted and that its subscription is active.", info.Name)
		}
		return "", nil, err
	}
	if len(info.skipModels) > 0 {
		models = slices.DeleteFunc(models, func(id string) bool {
			return slices.ContainsFunc(info.skipModels, func(prefix string) bool { return strings.HasPrefix(id, prefix) })
		})
		if len(models) == 0 {
			return "", nil, accessErr(http.StatusBadRequest, "%s accepted the key but lists no chat models for it.", info.Name)
		}
	}
	return base, models, nil
}
