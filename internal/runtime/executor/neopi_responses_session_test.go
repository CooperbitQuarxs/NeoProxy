package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NeoPi's gateway takes the client's prompt_cache_key as the session over
// any other session the caller carries (auth-gateway/server.ts
// buildStreamOptions); the proxy's own sessions apply only without one.
func TestNeoPiResponsesRowsPreferClientPromptCacheKey(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	capture := loadGitHubCopilotCapture(t, "testdata/neopi_commandcode/openai-responses.json.gz")
	fixture := capture.Fixtures[0]
	row := registry.NeoPiCompletionsModel("commandcode", fixture.Model)
	if row == nil || row.API != "openai-responses" {
		t.Fatalf("no bundled Responses row %s", fixture.Model)
	}
	base, err := url.Parse(row.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	row.BaseURL = server.URL + base.Path
	exec := NewNeoPiCompletionsExecutor("commandcode", &config.Config{}, NeoPiCompletionsProfile{})
	auth := &cliproxyauth.Auth{ID: "commandcode-session", Provider: "commandcode", Attributes: map[string]string{"api_key": capture.APIKey}}
	for name, withKey := range map[string]bool{"client key": true, "no client key": false} {
		t.Run(name, func(t *testing.T) {
			downstream := []byte(fixture.Downstream)
			if withKey {
				if downstream, err = sjson.SetBytes(downstream, "prompt_cache_key", "client-cache-key"); err != nil {
					t.Fatal(err)
				}
			}
			body = nil
			_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:    fixture.Model,
				Payload:  downstream,
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.NeoPiCompletionsModelInfo(row, "commandcode")},
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: downstream,
				Headers:  http.Header{"X-Claude-Code-Session-Id": {"caller-session"}},
				Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "execution-session"}})
			if body == nil {
				t.Fatal("executor sent no request")
			}
			want := "caller-session"
			if withKey {
				want = "client-cache-key"
			}
			if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != want {
				t.Fatalf("prompt_cache_key = %q, want %q", got, want)
			}
		})
	}
}
