package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeStaticKeyHeaderPolicy(t *testing.T) {
	for _, tc := range []struct{ name, base, authorization, apiKey string }{
		{"official", "https://api.anthropic.com", "", "fake-static"},
		{"custom", "https://gateway.invalid", "Bearer fake-static", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fake-static", "base_url": tc.base, "header:User-Agent": "configured-client", "header:X-Custom": "preserved"}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":64}`)}
			prepared, err := NewClaudeExecutor(nil).prepare(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.headers.Get("Authorization") != tc.authorization || prepared.headers.Get("X-Api-Key") != tc.apiKey || prepared.headers.Get("User-Agent") != "configured-client" || prepared.headers.Get("X-Custom") != "preserved" {
				t.Fatalf("incorrect credential/model headers: %#v", prepared.headers)
			}
			if strings.Contains(prepared.url, "beta=true") || gjson.GetBytes(prepared.body, "metadata").Exists() || gjson.GetBytes(prepared.body, "system").Exists() || gjson.GetBytes(prepared.body, "tools").Exists() {
				t.Fatalf("API key inherited OAuth framing: %s %s", prepared.url, prepared.body)
			}
			auth.Attributes["header:Authorization"] = "Custom supplied"
			auth.Attributes["header:X-Api-Key"] = "explicit-key"
			prepared, err = NewClaudeExecutor(nil).prepare(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.headers.Get("Authorization") != "Custom supplied" || prepared.headers.Get("X-Api-Key") != "explicit-key" {
				t.Fatalf("explicit model credential headers were lost: %#v", prepared.headers)
			}
		})
	}
}

func TestClaudeCountTokensAppliesFinalPayloadRulesOnce(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		t.Run(map[bool]string{false: "api-key", true: "oauth"}[oauth], func(t *testing.T) {
			var captured []byte
			var path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured, _ = io.ReadAll(r.Body)
				path = r.URL.RequestURI()
				_, _ = io.WriteString(w, `{"input_tokens":12}`)
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "fake-static", "base_url": server.URL}}
			if oauth {
				auth.Attributes = nil
				auth.Metadata = map[string]any{"access_token": "fake-oauth", "neopi_install_id": "test-install", "base_url": server.URL}
			}
			cfg := &config.Config{Payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"messages.0", "system", "metadata"}}}, Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"max_tokens": 1, "stream": false}}}}}
			request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(`{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"second"},{"role":"user","content":"third"}],"max_tokens":64}`)}
			response, err := NewClaudeExecutor(cfg).CountTokens(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if err != nil {
				t.Fatal(err)
			}
			wantPath := "/v1/messages/count_tokens"
			if oauth {
				wantPath += "?beta=true"
			}
			if path != wantPath || gjson.GetBytes(response.Payload, "input_tokens").Int() != 12 {
				t.Fatalf("count result/path: %s %s", path, response.Payload)
			}
			if gjson.GetBytes(captured, "messages.#").Int() != 2 || gjson.GetBytes(captured, "max_tokens").Int() != 1 || gjson.GetBytes(captured, "stream").Type != gjson.False || gjson.GetBytes(captured, "system").Exists() || gjson.GetBytes(captured, "metadata").Exists() {
				t.Fatalf("count shaping ran after user rules or applied them twice: %s", captured)
			}
		})
	}
}

func TestClaudeNeutralDiscoveryProfiles(t *testing.T) {
	for _, id := range []string{"claude-unfamiliar", "claude-opus-6", "claude-sonnet-6", "claude-haiku-6", "claude-fable-6", "claude-mythos-6"} {
		t.Run(id, func(t *testing.T) {
			// Force the source-rule fallback, rather than the exact capture lookup.
			actual := neoPiResolveProfile(id + "-new")
			want := neoPiProfiles[id]
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("neutral discovery profile differs from source: got %+v want %+v", actual, want)
			}
		})
	}
	if got, want := neoPiResolveProfile("claude-opus-5-5-20261001[1m]"), neoPiProfiles["claude-opus-5-5"]; !reflect.DeepEqual(got, want) {
		t.Fatal("dated/context model lost canonical profile")
	}
	body, _, err := projectNeoPiRequest([]byte(`{"messages":[{"role":"user","content":"hello"}]}`), "custom-gateway-model", "https://gateway.invalid", "test", neoPiWireIdentity{APIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(body, "thinking").Exists() || gjson.GetBytes(body, "model").String() != "custom-gateway-model" {
		t.Fatalf("unknown model inherited reasoning or changed its address: %s", body)
	}
}
