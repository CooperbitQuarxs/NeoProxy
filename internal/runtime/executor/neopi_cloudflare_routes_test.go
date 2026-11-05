package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cloudflare"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type cloudflareRouteCapture struct {
	url, model string
}

func (c *cloudflareRouteCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	c.url, c.model = r.URL.String(), gjson.GetBytes(body, "model").String()
	return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
}

// NeoPi's prepareModel routes every request's row, so a configured model the
// bundle lacks takes its namespace's transport, gateway route and wire id
// exactly as a bundled or discovered one does.
func TestNeoPiCloudflareRoutesConfiguredUnbundledModels(t *testing.T) {
	credential := cloudflare.Serialize(cloudflare.Credential{Token: "cfut_route", AccountID: "acct", GatewayID: "gw"})
	for _, c := range []struct{ model, url, wire string }{
		{"openai/ft:gpt-4.1:org:private:123", "https://gateway.ai.cloudflare.com/v1/acct/gw/openai/chat/completions", "ft:gpt-4.1:org:private:123"},
		{"anthropic/claude-new-9.1", "https://gateway.ai.cloudflare.com/v1/acct/gw/anthropic/v1/messages", "claude-new-9-1"},
		{"workers-ai/@cf/meta/llama-new", "https://gateway.ai.cloudflare.com/v1/acct/gw/compat/chat/completions", "workers-ai/@cf/meta/llama-new"},
	} {
		t.Run(c.model, func(t *testing.T) {
			for _, info := range registry.NeoPiCompletionsBundledModels(cloudflare.Provider, cloudflare.Provider) {
				if info.ID == c.model {
					t.Fatalf("%s is bundled; the case needs an unbundled id", c.model)
				}
			}
			capture := &cloudflareRouteCapture{}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(capture))
			exec := NewNeoPiCompletionsExecutor(cloudflare.Provider, &config.Config{}, NeoPiCompletionsProfile{})
			auth := &cliproxyauth.Auth{ID: "cf", Provider: cloudflare.Provider, Attributes: map[string]string{"api_key": credential}}
			payload := []byte(`{"model":"` + c.model + `","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
			_, _ = exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: c.model, Payload: payload},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
			if capture.url != c.url || capture.model != c.wire {
				t.Fatalf("request went to %q as model %q, want %q as %q", capture.url, capture.model, c.url, c.wire)
			}
		})
	}
}
