package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cloudflare"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// cloudflareRedirectHop records the gateway credential each hop received.
type cloudflareRedirectHop struct {
	mu   sync.Mutex
	seen []string
}

func (h *cloudflareRedirectHop) record(r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, r.Header.Get(cloudflareAuthHeader)+"|"+r.Header.Get("X-Api-Key")+"|"+r.Header.Get("Authorization"))
}

func (h *cloudflareRedirectHop) hits() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.seen...)
}

// A redirect away from the gateway's origin never carries the gateway token,
// on any later hop either (net/http recopies the first request's headers on
// every hop), while a same-origin redirect keeps it. Covered for Chat
// Completions rows, Anthropic Messages rows and arbitrary HttpRequest calls.
func TestNeoPiCloudflareRedirectDropsGatewayToken(t *testing.T) {
	var openAIRow, anthropicRow *registry.ModelInfo
	for _, info := range registry.NeoPiCompletionsBundledModels(cloudflare.Provider, cloudflare.Provider) {
		switch {
		case info.NeoPi == nil:
		case openAIRow == nil && info.NeoPi.API == "openai-completions":
			openAIRow = info
		case anthropicRow == nil && info.NeoPi.API == "anthropic-messages":
			anthropicRow = info
		}
	}
	if openAIRow == nil || anthropicRow == nil {
		t.Fatal("Cloudflare catalog lacks an openai-completions or anthropic-messages row")
	}
	const token = "cfut_redirect_secret"
	credential := cloudflare.Serialize(cloudflare.Credential{Token: token, AccountID: "acct", GatewayID: "gw"})

	type call func(t *testing.T, exec *NeoPiCompletionsExecutor, auth *cliproxyauth.Auth, base string)
	execute := func(row *registry.ModelInfo, format sdktranslator.Format, payload string) call {
		return func(t *testing.T, exec *NeoPiCompletionsExecutor, auth *cliproxyauth.Auth, base string) {
			routed := *row
			routed.NeoPi = row.NeoPi.Clone()
			routed.NeoPi.BaseURL = base
			body := []byte(strings.ReplaceAll(payload, "{model}", row.ID))
			_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model: row.ID, Payload: body,
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": &routed},
			}, cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: body})
		}
	}
	httpRequest := func(t *testing.T, exec *NeoPiCompletionsExecutor, auth *cliproxyauth.Auth, base string) {
		req, err := http.NewRequest(http.MethodGet, base+"/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := exec.HttpRequest(context.Background(), auth, req)
		if err == nil {
			_ = response.Body.Close()
		}
	}
	calls := map[string]call{
		"chat-completions":  execute(openAIRow, sdktranslator.FormatOpenAI, `{"model":"{model}","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		"anthropic-message": execute(anthropicRow, sdktranslator.FormatClaude, `{"model":"{model}","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
		"http-request":      httpRequest,
	}
	for name, send := range calls {
		t.Run(name+"/cross-origin", func(t *testing.T) {
			// gateway -> first foreign origin -> second foreign origin.
			var second, first cloudflareRedirectHop
			secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				second.record(r)
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer secondServer.Close()
			firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				first.record(r)
				http.Redirect(w, r, secondServer.URL+"/collect", http.StatusTemporaryRedirect)
			}))
			defer firstServer.Close()
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(cloudflareAuthHeader) != "Bearer "+token {
					t.Errorf("gateway request lacks the token: %q", r.Header.Get(cloudflareAuthHeader))
				}
				http.Redirect(w, r, firstServer.URL+"/collect", http.StatusTemporaryRedirect)
			}))
			defer gateway.Close()
			exec := NewNeoPiCompletionsExecutor(cloudflare.Provider, &config.Config{}, NeoPiCompletionsProfile{})
			auth := &cliproxyauth.Auth{ID: "cf", Provider: cloudflare.Provider, Attributes: map[string]string{"api_key": credential}}
			send(t, exec, auth, gateway.URL+"/v1/acct/gw/openai")
			for label, hop := range map[string]*cloudflareRedirectHop{"first": &first, "second": &second} {
				hits := hop.hits()
				if len(hits) != 1 {
					t.Fatalf("%s foreign origin saw %d requests, want 1 (the redirect is followed)", label, len(hits))
				}
				if strings.Contains(hits[0], token) {
					t.Errorf("%s foreign origin received the gateway token: %s", label, hits[0])
				}
			}
		})
		t.Run(name+"/same-origin", func(t *testing.T) {
			var hop cloudflareRedirectHop
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/moved") {
					hop.record(r)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				http.Redirect(w, r, "/moved"+r.URL.Path, http.StatusTemporaryRedirect)
			}))
			defer gateway.Close()
			exec := NewNeoPiCompletionsExecutor(cloudflare.Provider, &config.Config{}, NeoPiCompletionsProfile{})
			auth := &cliproxyauth.Auth{ID: "cf", Provider: cloudflare.Provider, Attributes: map[string]string{"api_key": credential}}
			send(t, exec, auth, gateway.URL+"/v1/acct/gw/openai")
			hits := hop.hits()
			if len(hits) != 1 || !strings.HasPrefix(hits[0], "Bearer "+token+"|") {
				t.Fatalf("same-origin redirect hops = %q, want one carrying the gateway token", hits)
			}
		})
	}
}
