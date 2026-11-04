package helps

import (
	"maps"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// KimiMessagesAuth projects Kimi's bearer onto the API-key branch of the shared
// Messages transport. It must not acquire Anthropic OAuth identity or mutate
// the engine's credential while serving a different request dialect.
func KimiMessagesAuth(auth *coreauth.Auth, token string) *coreauth.Auth {
	if auth == nil {
		return nil
	}
	next := auth.Clone()
	next.Attributes = maps.Clone(auth.Attributes)
	if next.Attributes == nil {
		next.Attributes = make(map[string]string)
	}
	next.Attributes["base_url"] = ResolveKimiClaudeBaseURL(auth)
	next.Attributes["api_key"] = token
	next.Metadata = maps.Clone(auth.Metadata)
	delete(next.Metadata, "access_token")
	return next
}

// KimiMessagesRequest retains Kimi's own Messages semantics without applying
// Anthropic's cache anchors or model capability projection.
func KimiMessagesRequest(body []byte, requestedModel, upstreamModel string) ([]byte, error) {
	body, err := sjson.SetBytes(body, "model", upstreamModel)
	if err != nil {
		return nil, err
	}
	if !gjson.GetBytes(body, "max_tokens").Exists() {
		maxTokens := 1024
		if info := registry.LookupModelInfo(thinking.ParseSuffix(requestedModel).ModelName, "kimi"); info != nil && info.MaxCompletionTokens > 0 {
			maxTokens = info.MaxCompletionTokens
		}
		body, err = sjson.SetBytes(body, "max_tokens", maxTokens)
		if err != nil {
			return nil, err
		}
	}
	return sjson.SetBytes(body, "stream", true)
}
