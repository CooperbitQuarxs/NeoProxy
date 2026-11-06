package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// NeoPi's gateway makes the client's prompt_cache_key the request's
// sessionId ahead of the caller's own session (auth-gateway/server.ts), and
// factory-droid.ts sends deterministicUuid(sessionId) as x-session-id and, on
// the Responses wire, as prompt_cache_key. Without a client key the caller's
// session decides.
func TestFactoryDroidSessionPrefersClientPromptCacheKey(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want string
	}{
		{name: "client key", payload: `{"model":"gpt-6-astra","input":"hi","prompt_cache_key":"client-cache-key"}`, want: helps.FactoryDroidDeterministicUUID("client-cache-key")},
		{name: "caller session", payload: `{"model":"gpt-6-astra","input":"hi"}`, want: helps.FactoryDroidDeterministicUUID("caller-session")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var header http.Header
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header, body = r.Header.Clone(), nil
				body, _ = io.ReadAll(r.Body)
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer server.Close()
			entry := registry.FactoryDroidModelFor("gpt-6-astra")
			if entry == nil {
				t.Fatal("no registry model gpt-6-astra")
			}
			row := registry.BuildFactoryDroidModel(entry, registry.FactoryDroidScope{Region: "global", InferenceRegion: "global"}, "global", entry.Policy.Rotation)
			row.BaseURL = server.URL + "/api"
			auth := &cliproxyauth.Auth{ID: "factory-droid-session", Provider: "factory-droid", Metadata: map[string]any{"type": "factory-droid", "access_token": "token"}}
			req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(tc.payload), Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.FactoryDroidModelInfo(row)}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: []byte(tc.payload),
				Headers: http.Header{"X-Claude-Code-Session-Id": {"caller-session"}}}
			_, _ = NewFactoryDroidExecutor(&config.Config{}).Execute(context.Background(), auth, req, opts)
			if header == nil {
				t.Fatal("executor sent no request")
			}
			if got := header.Get("X-Session-Id"); got != tc.want {
				t.Errorf("x-session-id = %q, want %q", got, tc.want)
			}
			if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != tc.want {
				t.Errorf("prompt_cache_key = %q, want %q", got, tc.want)
			}
		})
	}
}
