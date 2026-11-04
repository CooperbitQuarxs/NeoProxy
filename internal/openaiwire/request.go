package openaiwire

import (
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// NeoPiUserAgent is USER_AGENT of the NeoPi build these ports follow: Shayna's
// running nightly (a842643e, 18.14.0).
const NeoPiUserAgent = "npi/18.14.0"

// firstEventTimeoutSeconds is NeoPi's default first-event watchdog (300s),
// announced to the server as X-Stainless-Timeout.
const firstEventTimeoutSeconds = "300"

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
	if model.Provider == "xai" || model.Provider == "xai-oauth" {
		header.Set("User-Agent", NeoPiUserAgent)
	}
	header.Set("Authorization", "Bearer "+accessToken)
	header.Set("X-Stainless-Timeout", firstEventTimeoutSeconds)
	return &Request{URL: baseURL + "/responses", Header: header, Body: BuildResponses(model, ctx, opts)}
}
