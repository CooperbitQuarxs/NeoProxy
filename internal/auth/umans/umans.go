// Package umans is NeoPi's Umans AI Coding Plan key (auth rule umans): a
// plain API key validated against the Anthropic Messages endpoint and sent as
// X-Api-Key, never as a bearer.
package umans

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	// Provider is NeoPi's provider and auth id.
	Provider = "umans"
	// Name is the auth rule's display name.
	Name = "Umans AI Coding Plan"
	// BaseURL is UMANS_BASE_URL, the Anthropic Messages base that identifies
	// a Umans account's openai-compatibility entry.
	BaseURL = "https://api.code.umans.ai"
	// KeyPageURL is the rule's auth-url.
	KeyPageURL = "https://app.umans.ai/billing"
	// ValidationModel is the model the rule's validate probe names.
	ValidationModel = "umans-coder"
	// WebSearchHeader selects Umans' gateway web search (native or exa).
	WebSearchHeader = "X-Umans-Websearch-Provider"

	// validationTimeout is VALIDATION_TIMEOUT_MS.
	validationTimeout = 15 * time.Second
	// userAgent is Bun's fetch default, which NeoPi's probe carries.
	userAgent = "Bun/1.3.14"
)

// IsBaseURL reports whether an openai-compatibility entry's base URL is the
// Umans endpoint, with or without its /v1 suffix.
func IsBaseURL(base string) bool {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	return strings.TrimSuffix(base, "/v1") == BaseURL
}

// WebSearch is the X-Umans-Websearch-Provider header NeoPi sends and whether
// Umans' gateway web search is on (getUmansWebSearchHeader over the caller's
// headers). A caller's header is forwarded as sent; when it names native or
// exa (case and spaces aside) the normalized value replaces it and the
// gateway is on. Without a caller header, UMANS_WEBSEARCH_PROVIDER decides,
// and only a valid value is sent.
func WebSearch(incoming http.Header) (value string, gateway bool) {
	if values := incoming.Values(WebSearchHeader); len(values) > 0 {
		if normalized := normalizeWebSearchProvider(values[0]); normalized != "" {
			return normalized, true
		}
		return values[0], false
	}
	if value, ok := os.LookupEnv("UMANS_WEBSEARCH_PROVIDER"); ok {
		if normalized := normalizeWebSearchProvider(value); normalized != "" {
			return normalized, true
		}
	}
	return "", false
}

func normalizeWebSearchProvider(value string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "native", "exa":
		return normalized
	}
	return ""
}

// ValidateKey is validateAnthropicCompatibleApiKey for the umans rule: a
// one-token Messages request for umans-coder at base with the key in
// x-api-key. It returns the HTTP status Umans answered; the body is drained
// unread because it can echo account details.
func ValidateKey(ctx context.Context, client *http.Client, base, key string) (int, error) {
	if client == nil {
		client = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, validationTimeout)
	defer cancel()
	body := `{"model":"` + ValidationModel + `","messages":[{"role":"user","content":"ping"}],"max_tokens":1}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")+"/v1/messages", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-api-key", key)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, nil
}
