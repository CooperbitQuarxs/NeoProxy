package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestFetchXAIModelsListsTheProviderEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"grok-4.7","name":"Grok 4.7","context_window":256000},{"id":" "},{"id":"grok-4.7-build-fast"}]}`)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{
		ID:         "gb",
		Provider:   GrokBuildProvider,
		Attributes: map[string]string{"base_url": server.URL + "/v1"},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}
	models, err := FetchXAIModels(context.Background(), &config.Config{}, auth)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/models" || gotAuth != "Bearer oauth-token" {
		t.Fatalf("request = %s with %q", gotPath, gotAuth)
	}
	if len(models) != 2 || models[0].ID != "grok-4.7" || models[0].Name != "Grok 4.7" || models[0].ContextWindow != 256000 || models[1].ID != "grok-4.7-build-fast" {
		t.Fatalf("models = %+v", models)
	}
	if _, err = FetchXAIModels(context.Background(), &config.Config{}, &cliproxyauth.Auth{Provider: GrokBuildProvider}); err == nil {
		t.Fatal("listing without a token succeeded")
	}
}

func TestGrokBuildExecutorIdentity(t *testing.T) {
	if NewGrokBuildExecutor(nil).Identifier() != GrokBuildProvider || NewXAIExecutor(nil).Identifier() != "xai" {
		t.Fatal("executor identifiers")
	}
}
