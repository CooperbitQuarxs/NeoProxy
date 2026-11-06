package executor

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// neoPiCompletionsDefaultIdleTimeoutMs is DEFAULT_STREAM_IDLE_TIMEOUT_MS, the
// floor NeoPi's first-event deadline starts from.
const neoPiCompletionsDefaultIdleTimeoutMs = 300_000

// neoPiCompletionsHeaders is the OpenAI client's request headers as NeoPi sends
// them for a Chat Completions row: the account's configured headers first,
// then the ones NeoPi's client enforces. sessionID is NeoPi's
// promptCacheSessionId, which ClinePass carries as its task id;
// conversationID is its options.sessionId, which the row's session header
// carries.
func neoPiCompletionsHeaders(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, key string, row *registry.NeoPiModel, sessionID, conversationID string) http.Header {
	headers := make(http.Header)
	if auth != nil {
		request := (&http.Request{Header: headers}).WithContext(ctx)
		util.ApplyCustomHeadersFromAttrs(request, auth.Attributes, incoming)
	}
	if row != nil {
		switch row.Provider {
		case "cline-pass":
			// prependHeaders: the configured headers win over the mirrored identity.
			for name, value := range openaiwire.ClinePassClientHeaders(sessionID) {
				if headers.Get(name) == "" {
					headers.Set(name, value)
				}
			}
		case "coreweave":
			neoPiApplyCoreWeaveProjectHeader(headers, os.Getenv)
		}
		if row.CompletionsCompat != nil {
			neoPiApplySessionHeader(headers, row.CompletionsCompat.SessionHeader, conversationID)
		}
	}
	headers.Set("Accept", "text/event-stream")
	headers.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("Connection", "keep-alive")
	headers.Set("Content-Type", "application/json")
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", openaiwire.NeoPiUserAgent)
	}
	if timeout := neoPiCompletionsTimeoutHeader(row); timeout != "" {
		headers.Set("X-Stainless-Timeout", timeout)
	}
	return headers
}

// neoPiApplySessionHeader is applySessionHeader: the conversation id under
// the row's resolved session header (LiteLLM's x-litellm-session-id), unless
// the configured headers already carry that field; nothing without an id.
func neoPiApplySessionHeader(headers http.Header, name, sessionID string) {
	if name == "" || sessionID == "" || len(headers.Values(name)) > 0 {
		return
	}
	headers.Set(name, sessionID)
}

// neoPiCompletionsTimeoutHeader is the pre-response deadline NeoPi announces:
// its first-event timeout, which never undershoots the stream idle timeout.
func neoPiCompletionsTimeoutHeader(row *registry.NeoPiModel) string {
	timeout := neoPiCompletionsDefaultIdleTimeoutMs
	if row != nil && row.CompletionsCompat != nil && row.CompletionsCompat.StreamFirstEventTimeoutMs != nil {
		first := *row.CompletionsCompat.StreamFirstEventTimeoutMs
		if first <= 0 {
			return ""
		}
		timeout = max(timeout, first)
	}
	if row != nil && row.CompletionsCompat != nil && row.CompletionsCompat.StreamIdleTimeoutMs > timeout {
		timeout = row.CompletionsCompat.StreamIdleTimeoutMs
	}
	return strconv.Itoa(timeout / 1000)
}

// neoPiApplyCoreWeaveProjectHeader is applyCoreWeaveProjectHeader: a blank
// configured project header is dropped, a set one is kept, and otherwise the
// W&B project named by the environment is sent.
func neoPiApplyCoreWeaveProjectHeader(headers http.Header, getenv func(string) string) {
	if strings.TrimSpace(headers.Get(openaiwire.CoreWeaveProjectHeader)) != "" {
		return
	}
	headers.Del(openaiwire.CoreWeaveProjectHeader)
	if project := openaiwire.ResolveCoreWeaveProject(getenv); project != "" {
		headers.Set(openaiwire.CoreWeaveProjectHeader, project)
	}
}
