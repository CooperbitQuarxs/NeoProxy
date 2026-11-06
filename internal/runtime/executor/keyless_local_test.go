package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A local server's account without a real key (an empty key, or the
// keyless marker NeoPi's login stores for an empty answer) requests with no
// Authorization, as NeoPi's agent requests its implicit local providers
// (kNoAuth). A real key is still sent as a bearer, and a hosted provider
// without a key is still refused before anything is sent.
func TestKeylessLocalServersSendNoAuthorization(t *testing.T) {
	type sent struct {
		path, authorization string
		hasAuthorization    bool
	}
	var got *sent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = &sent{path: r.URL.Path, authorization: r.Header.Get("Authorization")}
		_, got.hasAuthorization = r.Header["Authorization"]
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	noEnv := func(string) string { return "" }
	rowsFor := func(t *testing.T, provider string) map[string]*registry.ModelInfo {
		t.Helper()
		var infos []*registry.ModelInfo
		var err error
		switch provider {
		case registry.NeoPiOllamaProvider:
			infos, err = registry.NeoPiOllamaListing("", noEnv, func(endpoint string, body []byte) (int, []byte, error) {
				if body == nil {
					return 200, []byte(`{"models":[{"model":"llama3.2:3b"}]}`), nil
				}
				return 404, nil, nil
			})
		case registry.NeoPiLlamaCppProvider:
			infos, err = registry.NeoPiLlamaCppListing(context.Background(), []string{"http://127.0.0.1:8080"}, func(_ context.Context, endpoint string) (int, []byte, error) {
				if strings.HasSuffix(endpoint, "/models") {
					return 200, []byte(`{"data":[{"id":"Qwen3-8B-Q4_K_M"},{"id":"Llama-3.2-3B"}]}`), nil
				}
				return 404, nil, nil
			})
		default:
			infos, err = registry.NeoPiCompletionsListing(provider, registry.NeoPiCompletionsListingBase(provider), []byte(`{"data":[{"id":"Qwen/Qwen3-32B"}]}`))
		}
		if err != nil {
			t.Fatalf("%s listing: %v", provider, err)
		}
		rows := map[string]*registry.ModelInfo{}
		for _, info := range infos {
			rows[info.ID] = info
		}
		return rows
	}
	send := func(t *testing.T, provider, key string, info *registry.ModelInfo) (*sent, error) {
		t.Helper()
		row := info.NeoPi.Clone()
		row.BaseURL = server.URL + "/v1"
		exec := NewNeoPiCompletionsExecutor(provider, &config.Config{}, NeoPiCompletionsProfile{})
		auth := &cliproxyauth.Auth{ID: provider + "-account", Provider: provider, Attributes: map[string]string{"api_key": key}}
		format, payload := sdktranslator.FormatOpenAI, `{"model":"`+row.ID+`","messages":[{"role":"user","content":"hi"}],"stream":true}`
		if row.API == "openai-responses" {
			format, payload = sdktranslator.FormatOpenAIResponse, `{"model":"`+row.ID+`","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`
		}
		got = nil
		_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model: row.ID, Payload: []byte(payload),
			Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.NeoPiCompletionsModelInfo(row, provider)},
		}, cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: []byte(payload)})
		return got, err
	}
	cases := []struct {
		provider, model, marker string
	}{
		{registry.NeoPiOllamaProvider, "llama3.2:3b", ""},
		{registry.NeoPiLlamaCppProvider, "Qwen3-8B-Q4_K_M", registry.NeoPiLlamaCppKeylessMarker},
		{registry.NeoPiLlamaCppProvider, "Llama-3.2-3B", registry.NeoPiLlamaCppKeylessMarker},
		{"vllm", "Qwen/Qwen3-32B", "vllm-local"},
		{"lm-studio", "Qwen/Qwen3-32B", "lm-studio-local"},
	}
	for _, tc := range cases {
		info := rowsFor(t, tc.provider)[tc.model]
		if info == nil {
			t.Fatalf("%s: no row %s", tc.provider, tc.model)
		}
		keys := []string{""}
		if tc.marker != "" {
			keys = append(keys, tc.marker)
		}
		for _, key := range keys {
			request, _ := send(t, tc.provider, key, info)
			if request == nil {
				t.Errorf("%s %s key %q (%s): no request sent", tc.provider, tc.model, key, info.NeoPi.API)
			} else if request.hasAuthorization {
				t.Errorf("%s %s key %q (%s): sent Authorization %q", tc.provider, tc.model, key, info.NeoPi.API, request.authorization)
			}
		}
		if request, _ := send(t, tc.provider, "sk-real", info); request == nil || request.authorization != "Bearer sk-real" {
			t.Errorf("%s %s real key: sent %+v, want Bearer sk-real", tc.provider, tc.model, request)
		}
	}
	// A hosted provider keeps refusing an account without a key.
	hosted := registry.NeoPiCompletionsBundledModels("cerebras", "cerebras")
	if len(hosted) == 0 {
		t.Fatal("no bundled cerebras rows")
	}
	if request, err := send(t, "cerebras", "", hosted[0]); request != nil || err == nil {
		t.Errorf("cerebras without a key: sent %+v, err %v; want refusal before sending", request, err)
	}
}
