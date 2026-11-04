package executor

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// neoPiApplyHeaders merges configured model headers using NeoPi's enforced-key
// policy. Inbound proxy authentication is never forwarded to the provider.
func neoPiApplyHeaders(headers http.Header, auth *cliproxyauth.Auth, incoming http.Header, credential *anthropicnpi.Credential, base string, ctx context.Context) {
	oauth := credential.APIKey == ""
	configured := make(http.Header)
	if auth != nil {
		request := (&http.Request{Header: configured}).WithContext(ctx)
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes, incoming)
	}
	for name, values := range configured {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "x-api-key" || lower == "user-agent" {
			continue
		}
		switch lower {
		case "accept", "accept-encoding", "connection", "content-type", "anthropic-version", "anthropic-beta", "anthropic-dangerous-direct-browser-access", "x-app", "x-claude-code-session-id", "x-client-request-id", "cf-aig-authorization":
			continue
		case "x-stainless-arch", "x-stainless-lang", "x-stainless-os", "x-stainless-package-version", "x-stainless-retry-count", "x-stainless-runtime", "x-stainless-runtime-version", "x-stainless-timeout":
			if oauth {
				continue
			}
		}
		headers[name] = append([]string(nil), values...)
	}
	if oauth {
		headers.Set("Authorization", "Bearer "+credential.AccessToken)
		headers.Del("X-Api-Key")
		ua := configured.Get("User-Agent")
		if ua == "" {
			ua = incoming.Get("User-Agent")
		}
		if strings.HasPrefix(strings.ToLower(ua), "claude-cli") {
			headers.Set("User-Agent", ua)
		}
	} else {
		headers.Del("Authorization")
		headers.Del("X-Api-Key")
		if neoPiOfficialEndpoint(base) {
			headers.Set("X-Api-Key", credential.APIKey)
		} else if credential.APIKey != "N/A" {
			headers.Set("Authorization", "Bearer "+credential.APIKey)
		}
		if value := configured.Get("Authorization"); value != "" {
			headers.Set("Authorization", value)
		}
		if value := configured.Get("User-Agent"); value != "" {
			headers.Set("User-Agent", value)
		}
	}
	if value := configured.Get("X-Api-Key"); value != "" {
		headers.Set("X-Api-Key", value)
	}
	if slow := incoming.Get("anthropic-usage-limit"); slow != "" {
		headers.Set("anthropic-usage-limit", slow)
	}
}

func (e *ClaudeExecutor) SupportsApplyPatch() bool { return e != nil }
