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
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
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
	Kind string `json:"kind"`
	// Label names the provider in failure messages; empty means the login's name.
	Label string `json:"label"`
	// BaseURL and Model address a chat-completions probe; MaxTokensField and
	// MaxTokens override its one-token "max_tokens" field.
	BaseURL        string `json:"baseUrl"`
	Model          string `json:"model"`
	MaxTokensField string `json:"maxTokensField"`
	MaxTokens      *int   `json:"maxTokens"`
	// TolerateModelDenied accepts a 401 whose error code is invalid_model:
	// the key is good, the probe model is not on its plan.
	TolerateModelDenied bool `json:"tolerateModelDenied"`
	// URL addresses a models-endpoint probe; BaseURLEnv names an environment
	// variable whose value replaces its origin and path prefix, followed by
	// BaseURLPath (default `/models`).
	URL         string `json:"url"`
	BaseURLEnv  string `json:"baseUrlEnv"`
	BaseURLPath string `json:"baseUrlPath"`
	// Optional trusts the key unless the probe proves an auth failure.
	Optional bool `json:"optional"`
	// HeadersHook names the computed headers a models-endpoint probe sends
	// (registry/hooks/api-key.ts API_KEY_HEADERS_HOOKS).
	HeadersHook string `json:"headersHook"`
}

// Login is one provider's key entry.
type Login struct {
	// Name is the provider's display name in NeoPi's auth rule.
	Name string `json:"name"`
	// Normalize is the rule's `normalize` step ("strip-bearer").
	Normalize string `json:"normalize"`
	// Probe is nil for providers without a validation probe.
	Probe *Validation `json:"validate"`
}

// loginsJSON is NeoPi's compiled `login "api-key"` rules for the providers
// NeoProxy serves through NeoPi's Chat Completions provider, exported by
// testdata/export_neopi_logins.ts. A provider whose keys come only from the
// environment (Groq, Mistral) carries its name alone and takes keys unprobed.
// The Token Plan's region and cookie prompts live in package alibabatokenplan.
//
//go:embed logins.json
var loginsJSON []byte

var logins = func() map[string]Login {
	var export struct {
		Logins map[string]Login `json:"logins"`
	}
	if err := json.Unmarshal(loginsJSON, &export); err != nil {
		panic(fmt.Sprintf("neopikeys: logins.json: %v", err))
	}
	return export.Logins
}()

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
	if l.Normalize == "strip-bearer" && key != "" {
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
		err = validateChatCompletions(ctx, client, label, v, key)
	case ModelsEndpoint:
		target := v.URL
		if v.BaseURLEnv != "" {
			if base := strings.TrimSpace(lookupEnv(v.BaseURLEnv)); base != "" {
				path := v.BaseURLPath
				if path == "" {
					path = "/models"
				}
				target = strings.TrimRight(base, "/") + path
			}
		}
		headers, hookErr := validationHeaders(v.HeadersHook)
		if hookErr != nil {
			return hookErr
		}
		err = validateModelsEndpoint(ctx, client, label, target, key, headers)
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

func validateChatCompletions(ctx context.Context, client *http.Client, label string, v *Validation, key string) error {
	// validateOpenAICompatibleApiKey: a one-token "ping" at temperature 0.
	field, maxTokens := v.MaxTokensField, 1
	if field == "" {
		field = "max_tokens"
	}
	if v.MaxTokens != nil {
		maxTokens = *v.MaxTokens
	}
	body := `{"model":` + jsonString(v.Model) + `,"messages":[{"role":"user","content":"ping"}],` + jsonString(field) + `:` + fmt.Sprint(maxTokens) + `,"temperature":0}`
	req, err := http.NewRequest(http.MethodPost, v.BaseURL+"/chat/completions", strings.NewReader(body))
	if err != nil {
		return err
	}
	setBunDefaults(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	err = probe(ctx, client, req, label)
	var httpErr *HTTPError
	if v.TolerateModelDenied && errors.As(err, &httpErr) && httpErr.Status == http.StatusUnauthorized && httpErr.envelopeCode() == "invalid_model" {
		return nil
	}
	return err
}

// ErrCoreWeaveProject is requireCoreWeaveProjectHeaders' ConfigurationError:
// CoreWeave bills every request to a W&B project the environment must name.
var ErrCoreWeaveProject = errors.New("CoreWeave Serverless Inference requires OpenAI-Project. Set COREWEAVE_PROJECT=<team>/<project> before running /login coreweave. To persist it, add export COREWEAVE_PROJECT=<team>/<project> to your shell startup file (for example ~/.zshrc, ~/.bashrc, or your shell's profile/rc file).")

// validationHeaders resolves a probe's headers hook.
func validationHeaders(hook string) (map[string]string, error) {
	switch hook {
	case "":
		return nil, nil
	case "coreweave-project":
		project := openaiwire.ResolveCoreWeaveProject(lookupEnv)
		if project == "" {
			return nil, ErrCoreWeaveProject
		}
		return map[string]string{openaiwire.CoreWeaveProjectHeader: project}, nil
	case "cline-pass-client":
		return openaiwire.ClinePassClientHeaders(""), nil
	}
	return nil, fmt.Errorf("neopikeys: unknown validation headers hook %q", hook)
}

// ValidateModelsEndpoint is validateApiKeyAgainstModelsEndpoint: a GET of the
// provider's model listing with the key as a bearer token.
func ValidateModelsEndpoint(ctx context.Context, client *http.Client, label, modelsURL, key string) error {
	return validateModelsEndpoint(ctx, client, label, modelsURL, key, nil)
}

// validateModelsEndpoint sends the probe with the hook's headers over Bun's
// defaults and under the bearer key.
func validateModelsEndpoint(ctx context.Context, client *http.Client, label, modelsURL, key string, headers map[string]string) error {
	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		return err
	}
	setBunDefaults(req)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
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

// envelopeCode is OpenAIHttpError.parseEnvelope's code: error.code, else
// error.type, when the body carries an error object.
func (e *HTTPError) envelopeCode() string {
	errorObject := gjson.Get(e.Details, "error")
	if !errorObject.IsObject() {
		return ""
	}
	if code := errorObject.Get("code"); code.Type == gjson.String {
		return code.String()
	}
	if kind := errorObject.Get("type"); kind.Type == gjson.String {
		return kind.String()
	}
	return ""
}
