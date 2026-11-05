package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// Before discovery answers, a request runs on NeoPi's catalog row, moved to
// the account's configured endpoint as NeoPi's provider baseUrl override
// moves it; SingularityAPI's endpoint gains its /v1 segment.
func TestNeoPiCompletionsCatalogRowUsesAccountEndpoint(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	exec := NewNeoPiCompletionsExecutor("openai-compatible-singularityapi-dev", nil, NeoPiCompletionsProfile{Provider: "singularityapi-dev"})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-sapi-key", "base_url": server.URL + "/"}}
	req := cliproxyexecutor.Request{Model: "deepseek-v4-flash", Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)}
	if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/chat/completions" {
		t.Errorf("request path = %q, want the account endpoint's /v1/chat/completions", path)
	}
}
