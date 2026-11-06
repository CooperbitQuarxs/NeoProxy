package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The real discovery fetch for a llama.cpp account asks llama-server's
// /models beside its /props, both with the account's key as a bearer, and
// with none when the account holds NeoPi's no-auth key or any local login's
// keyless marker (isDiscoveryBearerApiKey). A vLLM account holding its own
// marker likewise lists without one.
func TestNeoPiLocalServerDiscoveryBearer(t *testing.T) {
	var mu sync.Mutex
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/models", "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"Qwen3-8B-Q4_K_M"}]}`))
		case "/props":
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":8192}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	for _, tc := range []struct {
		provider, key, base string
		want                []string
	}{
		{registry.NeoPiLlamaCppProvider, "sk-llama", server.URL, []string{"/models Bearer sk-llama", "/props Bearer sk-llama"}},
		{registry.NeoPiLlamaCppProvider, registry.NeoPiLlamaCppKeylessMarker, server.URL, []string{"/models ", "/props "}},
		{registry.NeoPiLlamaCppProvider, "N/A", server.URL, []string{"/models ", "/props "}},
		{registry.NeoPiLlamaCppProvider, "vllm-local", server.URL, []string{"/models ", "/props "}},
		{"vllm", "vllm-local", server.URL + "/v1", []string{"/v1/models "}},
	} {
		got = nil
		auth := &coreauth.Auth{ID: tc.provider, Provider: tc.provider, Attributes: map[string]string{"api_key": tc.key, "base_url": tc.base}}
		models, err := fetchNeoPiCompletionsModels(tc.provider)(context.Background(), &config.Config{}, auth)
		if err != nil {
			t.Fatalf("%s key %q: %v", tc.provider, tc.key, err)
		}
		if len(models) == 0 {
			t.Errorf("%s key %q: no models discovered", tc.provider, tc.key)
		}
		sort.Strings(got)
		if len(got) != len(tc.want) {
			t.Fatalf("%s key %q: requests %q, want %q", tc.provider, tc.key, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s key %q: requests %q, want %q", tc.provider, tc.key, got, tc.want)
				break
			}
		}
	}
}

// A server that answers /models with a failure status has rejected the
// request even when its error body is cut short: NeoPi checks response.ok
// before reading, so discovery reports the HTTP failure (which ends it, with
// no fallback) rather than an unreadable answer.
func TestNeoPiLlamaCppAnsweredFailureKeepsStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":8192}}`))
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 401 Unauthorized\r\nContent-Length: 100\r\n\r\nno")
		_ = buf.Flush()
		_ = conn.Close()
	}))
	defer server.Close()
	auth := &coreauth.Auth{ID: "llama", Provider: registry.NeoPiLlamaCppProvider, Attributes: map[string]string{"api_key": "sk-llama", "base_url": server.URL}}
	_, err := fetchNeoPiCompletionsModels(registry.NeoPiLlamaCppProvider)(context.Background(), &config.Config{}, auth)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401 from") {
		t.Fatalf("err = %v; want the server's HTTP 401", err)
	}
}
