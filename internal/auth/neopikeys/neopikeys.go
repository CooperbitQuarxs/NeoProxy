// Package neopikeys is NeoPi's API-key entry for the providers NeoProxy serves
// through NeoPi's OpenAI Chat Completions provider: the compiled `login
// "api-key"` rules (catalog/src/compat/rules/auth/*.kdl, engine/api-key.ts) and
// the three validation probes of registry/api-key-validation.ts.
//
// A pasted key is trimmed, optionally stripped of a leading "Bearer", probed
// once against the provider and kept as typed. Groq and Mistral have no login
// in NeoPi (their keys come from the environment), so their keys are accepted
// without a probe.
package neopikeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// userAgent is Bun's fetch default, which NeoPi's validation requests carry.
	userAgent = "Bun/1.3.14"
	// validationTimeout is VALIDATION_TIMEOUT_MS.
	validationTimeout = 15 * time.Second
	// maxErrorBody bounds the error body read for the failure message.
	maxErrorBody = 1 << 20
)

// Probe kinds of the compiled `validate` rule that NeoProxy's providers use.
const (
	// ChatCompletions is validateOpenAICompatibleApiKey.
	ChatCompletions = "chat-completions"
	// ModelsEndpoint is validateApiKeyAgainstModelsEndpoint.
	ModelsEndpoint = "models-endpoint"
)

// Validation is one compiled `validate` rule.
type Validation struct {
	Kind string
	// Label names the provider in failure messages; empty means the login's name.
	Label string
	// BaseURL and Model address a chat-completions probe.
	BaseURL string
	Model   string
	// URL addresses a models-endpoint probe; BaseURLEnv names an environment
	// variable whose value replaces its origin and path prefix (`/models` is
	// appended).
	URL        string
	BaseURLEnv string
	// Optional trusts the key unless the probe proves an auth failure.
	Optional bool
}

// Login is one provider's key entry.
type Login struct {
	// Name is the provider's display name in NeoPi's auth rule.
	Name string
	// StripBearer is `normalize "strip-bearer"`.
	StripBearer bool
	// Probe is nil for providers without a validation probe.
	Probe *Validation
}

// logins holds the compiled auth rules of the NeoPi-served providers. The
// Token Plan's region and cookie prompts live in package alibabatokenplan.
var logins = map[string]Login{
	"cerebras": {Name: "Cerebras", Probe: &Validation{Kind: ChatCompletions, Label: "Cerebras", BaseURL: "https://api.cerebras.ai/v1", Model: "gpt-oss-120b"}},
	"deepseek": {Name: "DeepSeek", StripBearer: true, Probe: &Validation{Kind: ModelsEndpoint, Label: "deepseek", URL: "https://api.deepseek.com/v1/models"}},
	// The control-plane model listing needs a valid key but no deployment; the
	// inference listing answers 500 for accounts without deployments (issue #3219).
	"fireworks":   {Name: "Fireworks", Probe: &Validation{Kind: ModelsEndpoint, Label: "Fireworks", URL: "https://api.fireworks.ai/v1/accounts/fireworks/models?filter=supports_serverless%3Dtrue&pageSize=1"}},
	"groq":        {Name: "Groq"},
	"huggingface": {Name: "Hugging Face Inference", Probe: &Validation{Kind: ChatCompletions, Label: "Hugging Face", BaseURL: "https://router.huggingface.co/v1", Model: "openai/gpt-oss-120b"}},
	"mistral":     {Name: "Mistral"},
	"moonshot":    {Name: "Moonshot (Kimi API)", Probe: &Validation{Kind: ModelsEndpoint, Label: "moonshot", URL: "https://api.moonshot.ai/v1/models", BaseURLEnv: "MOONSHOT_BASE_URL"}},
	"nanogpt":     {Name: "NanoGPT", Probe: &Validation{Kind: ModelsEndpoint, URL: "https://nano-gpt.com/api/v1/models"}},
	// A real auth rejection is fatal; any other failure trusts the key.
	"nvidia":   {Name: "NVIDIA", Probe: &Validation{Kind: ChatCompletions, Label: "nvidia", BaseURL: "https://integrate.api.nvidia.com/v1", Model: "nvidia/llama-3.1-nemotron-70b-instruct", Optional: true}},
	"together": {Name: "Together", Probe: &Validation{Kind: ModelsEndpoint, Label: "together", URL: "https://api.together.xyz/v1/models"}},
}

// LoginFor reports NeoPi's key entry for a catalogue provider.
func LoginFor(provider string) (Login, bool) {
	login, ok := logins[provider]
	return login, ok
}

// Errors of the key-entry normalisation.
var (
	// ErrKeyRequired is ApiKeyRequiredError: nothing was pasted.
	ErrKeyRequired = errors.New("API key is required")
	// ErrEmptyAfterBearer is the strip-bearer rule's failure.
	ErrEmptyAfterBearer = errors.New("API key is empty after stripping Bearer prefix")
)

var bearerPrefix = regexp.MustCompile(`(?i)^bearer\b\s*`)

// NormalizeKey is the login's trim and `normalize` step.
func (l Login) NormalizeKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if l.StripBearer && key != "" {
		if key = bearerPrefix.ReplaceAllString(key, ""); key == "" {
			return "", fmt.Errorf("%s %w", l.Name, ErrEmptyAfterBearer)
		}
	}
	if key == "" {
		return "", ErrKeyRequired
	}
	return key, nil
}

// HTTPError is NeoPi's ProviderHttpError for a failed probe.
type HTTPError struct {
	Provider string
	Status   int
	Details  string
}

func (e *HTTPError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("%s API key validation failed (%d): %s", e.Provider, e.Status, e.Details)
	}
	return fmt.Sprintf("%s API key validation failed (%d)", e.Provider, e.Status)
}

// authFailurePattern is AUTH_FAILURE_PATTERN of the error classifier.
var authFailurePattern = regexp.MustCompile(`(?i)\b(?:401|403|unauthorized|forbidden|authentication|auth[_ ]?unavailable|no auth available|(?:invalid|no)[_ ]?api[_ ]?key)\b`)

// AuthFailed is classify(error) & Flag.AuthFailed for a probe failure: a 401 or
// 403, or auth wording in the message.
func (e *HTTPError) AuthFailed() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden || authFailurePattern.MatchString(e.Error())
}

// IsAuthFailure reports whether err is a probe failure NeoPi classifies as an
// authentication failure.
func IsAuthFailure(err error) bool {
	var httpErr *HTTPError
	return errors.As(err, &httpErr) && httpErr.AuthFailed()
}

// lookupEnv reads the environment variable behind a BaseURLEnv; tests replace it.
var lookupEnv = os.Getenv

// Validate runs the login's probe for key. Providers without a probe pass. An
// optional probe fails only on an authentication failure; any other failure,
// including an unreachable host, trusts the key.
func (l Login) Validate(ctx context.Context, client *http.Client, key string) error {
	v := l.Probe
	if v == nil {
		return nil
	}
	label := v.Label
	if label == "" {
		label = l.Name
	}
	var err error
	switch v.Kind {
	case ChatCompletions:
		err = validateChatCompletions(ctx, client, label, v.BaseURL, v.Model, key)
	case ModelsEndpoint:
		target := v.URL
		if v.BaseURLEnv != "" {
			if base := strings.TrimSpace(lookupEnv(v.BaseURLEnv)); base != "" {
				target = strings.TrimRight(base, "/") + "/models"
			}
		}
		err = ValidateModelsEndpoint(ctx, client, label, target, key)
	default:
		return fmt.Errorf("neopikeys: unknown validation kind %q", v.Kind)
	}
	if err != nil && v.Optional && !IsAuthFailure(err) {
		return nil
	}
	return err
}

// setBunDefaults are the headers Bun's fetch adds to NeoPi's validation
// requests.
func setBunDefaults(req *http.Request) {
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", userAgent)
}

func validateChatCompletions(ctx context.Context, client *http.Client, label, baseURL, model, key string) error {
	// validateOpenAICompatibleApiKey: a one-token "ping" at temperature 0.
	body := `{"model":` + jsonString(model) + `,"messages":[{"role":"user","content":"ping"}],"max_tokens":1,"temperature":0}`
	req, err := http.NewRequest(http.MethodPost, baseURL+"/chat/completions", strings.NewReader(body))
	if err != nil {
		return err
	}
	setBunDefaults(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	return probe(ctx, client, req, label)
}

// ValidateModelsEndpoint is validateApiKeyAgainstModelsEndpoint: a GET of the
// provider's model listing with the key as a bearer token.
func ValidateModelsEndpoint(ctx context.Context, client *http.Client, label, modelsURL, key string) error {
	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		return err
	}
	setBunDefaults(req)
	req.Header.Set("Authorization", "Bearer "+key)
	return probe(ctx, client, req, label)
}

// probe sends req under VALIDATION_TIMEOUT_MS and turns a non-2xx answer into
// an HTTPError carrying the response body.
func probe(ctx context.Context, client *http.Client, req *http.Request, label string) error {
	ctx, cancel := context.WithTimeout(ctx, validationTimeout)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("%s API key validation: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil
	}
	var details string
	if decoded, decodeErr := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding")); decodeErr == nil {
		raw, _ := io.ReadAll(io.LimitReader(decoded, maxErrorBody))
		_ = decoded.Close()
		details = strings.TrimSpace(string(raw))
	}
	return &HTTPError{Provider: label, Status: resp.StatusCode, Details: details}
}

func jsonString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
