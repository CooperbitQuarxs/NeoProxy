package openaiwire

import (
	"net/http"
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

// firstEventTimeoutSeconds preserves NeoPi's X-Stainless-Timeout header.
// It is a server hint, not a locally enforced response deadline.
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
	header.Set("User-Agent", NeoPiUserAgent)
	header.Set("Authorization", "Bearer "+accessToken)
	header.Set("X-Stainless-Timeout", firstEventTimeoutSeconds)
	return &Request{URL: baseURL + "/responses", Header: header, Body: BuildResponses(model, ctx, opts)}
}
