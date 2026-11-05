// Package xiaomi is NeoPi's Xiaomi MiMo key login (ai/src/registry/oauth/
// xiaomi.ts loginXiaomi): a pay-as-you-go key is probed on the standard host,
// and a Token Plan key (tp-/ttp-) on the regional clusters in SGP, AMS, CN
// order, moving on only when a cluster refuses the key or cannot be reached.
package xiaomi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/neopikeys"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// Provider is NeoPi's provider id; Name is PROVIDER_NAME.
	Provider = "xiaomi"
	Name     = "Xiaomi MiMo"
	// StandardBaseURL is STANDARD_API_BASE_URL.
	StandardBaseURL = "https://api.xiaomimimo.com/v1"
	// KeyPageURL is STANDARD_AUTH_URL.
	KeyPageURL = "https://platform.xiaomimimo.com/#/console/api-keys"
	// defaultModel is the provider's default model, the standard probe's.
	defaultModel = "mimo-v2.6-flash"
	// validationTimeout is VALIDATION_TIMEOUT_MS, fresh for every endpoint.
	validationTimeout = 15 * time.Second
	maxErrorBody      = 1 << 20
)

// tokenPlanRegions are the regional providers whose compiled chat-completions
// probes a Token Plan key walks, in NeoPi's fallback order.
var tokenPlanRegions = []string{"xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn"}

// IsTokenPlanKey is isXiaomiTokenPlanKey.
func IsTokenPlanKey(key string) bool {
	return strings.HasPrefix(key, "tp-") || strings.HasPrefix(key, "ttp-")
}

// NormalizeKey is the login's trim; an empty key is ApiKeyRequiredError.
func NormalizeKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return "", neopikeys.ErrKeyRequired
	}
	return key, nil
}

type endpoint struct{ baseURL, model string }

// endpoints are the probes for key, in order.
func endpoints(key string) ([]endpoint, error) {
	if !IsTokenPlanKey(key) {
		return []endpoint{{baseURL: StandardBaseURL, model: defaultModel}}, nil
	}
	out := make([]endpoint, 0, len(tokenPlanRegions))
	for _, region := range tokenPlanRegions {
		login, ok := neopikeys.LoginFor(region)
		if !ok || login.Probe == nil || login.Probe.Kind != neopikeys.ChatCompletions {
			return nil, fmt.Errorf("missing chat-completions validation policy for %s", region)
		}
		out = append(out, endpoint{baseURL: login.Probe.BaseURL, model: login.Probe.Model})
	}
	return out, nil
}

// ValidateKey is validateXiaomiApiKey. An endpoint that refuses the key (401)
// or cannot be reached hands over to the next; any other answer ends the walk.
// A failed answer is a *neopikeys.HTTPError carrying NeoPi's message.
func ValidateKey(ctx context.Context, client *http.Client, key string) error {
	probes, err := endpoints(key)
	if err != nil {
		return err
	}
	var last error
	for _, probe := range probes {
		status, details, err := send(ctx, client, probe, key)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			last = err
			continue
		case status >= 200 && status < 300:
			return nil
		}
		failure := &neopikeys.HTTPError{Provider: Name, Status: status, Details: details}
		if status != http.StatusUnauthorized {
			return failure
		}
		last = failure
	}
	if last == nil {
		last = errors.New(Name + " API key validation failed")
	}
	return last
}

// send posts NeoPi's one-token probe under a fresh validation deadline.
func send(ctx context.Context, client *http.Client, probe endpoint, key string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, validationTimeout)
	defer cancel()
	body := `{"model":` + string(jsjson.Marshal(probe.model)) + `,"max_tokens":1,"messages":[{"role":"user","content":"ping"}]}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, probe.baseURL+"/chat/completions", strings.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	// Bun's fetch defaults beside the probe's own two headers.
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", "Bun/1.3.14")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("%s API key validation: %w", Name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return resp.StatusCode, "", nil
	}
	details := ""
	if decoded, errDecode := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding")); errDecode == nil {
		raw, _ := io.ReadAll(io.LimitReader(decoded, maxErrorBody))
		_ = decoded.Close()
		details = strings.TrimSpace(string(raw))
	}
	return resp.StatusCode, details, nil
}
