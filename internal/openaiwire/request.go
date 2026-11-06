package openaiwire

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiversion"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// NeoPiUserAgent is USER_AGENT of the NeoPi build these ports follow. NeoPi's
// transportFetch (withInferenceUserAgent) sets it on every inference request
// that names no User-Agent of its own.
const NeoPiUserAgent = "npi/" + neopiversion.Version

// BunUserAgent is the User-Agent Bun's fetch sends when a request names none
// and does not go through NeoPi's inference transport: discovery, key
// validation and other side requests.
const BunUserAgent = "Bun/1.3.14"

// defaultStreamIdleTimeoutMs is DEFAULT_STREAM_IDLE_TIMEOUT_MS, the floor
// NeoPi's first-event deadline starts from.
const defaultStreamIdleTimeoutMs = 300_000

// firstEventTimeoutHeader is the X-Stainless-Timeout NeoPi announces for a
// row: its first-event timeout in seconds, never under the stream idle
// timeout, or none when the row's compat disables it. It is a server hint,
// not a locally enforced response deadline.
func firstEventTimeoutHeader(compat *registry.NeoPiCompat) string {
	timeout := defaultStreamIdleTimeoutMs
	if compat != nil && compat.StreamFirstEventTimeoutMs != nil {
		first := *compat.StreamFirstEventTimeoutMs
		if first <= 0 {
			return ""
		}
		timeout = max(timeout, first)
	}
	if compat != nil && compat.StreamIdleTimeoutMs > timeout {
		timeout = compat.StreamIdleTimeoutMs
	}
	return strconv.Itoa(timeout / 1000)
}

// Request is one shaped Responses POST.
type Request struct {
	URL    string
	Header http.Header
	Body   []byte
}

// ShapeResponses builds the request NeoPi's streamOpenAIResponses sends for a
// downstream turn: resolveOpenAIRequestSetup's headers, postOpenAIStream's
// framing headers and buildParams' body.
func ShapeResponses(model *registry.NeoPiModel, ctx Context, opts Options, accessToken string) *Request {
	baseURL := strings.TrimRight(model.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "text/event-stream")
	if name := model.Compat.PromptCacheSessionHeader; name != "" {
		if key := codexwire.NormalizePromptCacheKey(opts.SessionID); key != "" {
			header.Set(name, key)
		}
	}
	// applySessionHeader: the literal conversation id under the row's session
	// header (LiteLLM's x-litellm-session-id), unless already set.
	if name := model.Compat.SessionHeader; name != "" && opts.SessionID != "" && len(header.Values(name)) == 0 {
		header.Set(name, opts.SessionID)
	}
	header.Set("User-Agent", NeoPiUserAgent)
	header.Set("Authorization", "Bearer "+accessToken)
	if timeout := firstEventTimeoutHeader(model.Compat); timeout != "" {
		header.Set("X-Stainless-Timeout", timeout)
	}
	return &Request{URL: baseURL + "/responses", Header: header, Body: BuildResponses(model, ctx, opts)}
}
