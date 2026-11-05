package cliproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type neoPiCapturedRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   map[string]string `json:"query"`
	Headers map[string]string `json:"headers"`
}

// TestNeoPiCompletionsDiscoveryRequests replays NeoPi's own discovery against
// a loopback server and compares the requests the port sends.
func TestNeoPiCompletionsDiscoveryRequests(t *testing.T) {
	raw, err := os.ReadFile("../../internal/registry/testdata/neopi_completions_discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		APIKey  string `json:"apiKey"`
		Results []struct {
			Provider string `json:"provider"`
			Scenario string `json:"scenario"`
			Replies  []struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
			} `json:"replies"`
			Requests []neoPiCapturedRequest `json:"requests"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, result := range capture.Results {
		if seen[result.Provider] {
			continue
		}
		seen[result.Provider] = true
		t.Run(result.Provider, func(t *testing.T) {
			call := 0
			var got []neoPiCapturedRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query := map[string]string{}
				for name, values := range r.URL.Query() {
					query[name] = values[0]
				}
				got = append(got, neoPiCapturedRequest{Method: r.Method, Path: r.URL.Path, Query: query, Headers: map[string]string{
					"accept": r.Header.Get("Accept"), "authorization": r.Header.Get("Authorization"),
				}})
				reply := result.Replies[min(call, len(result.Replies)-1)]
				call++
				w.WriteHeader(reply.Status)
				_, _ = w.Write([]byte(reply.Body))
			}))
			defer server.Close()
			base, err := url.Parse(registry.NeoPiCompletionsListingBase(result.Provider))
			if err != nil {
				t.Fatal(err)
			}
			auth := &coreauth.Auth{ID: "a", Provider: result.Provider, Attributes: map[string]string{
				"api_key": capture.APIKey, "base_url": server.URL + base.Path,
			}}
			if _, err := fetchNeoPiCompletionsModels(result.Provider)(context.Background(), &config.Config{}, auth); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(result.Requests) {
				t.Fatalf("requests: port sent %d, NeoPi %d", len(got), len(result.Requests))
			}
			for i, want := range result.Requests {
				want.Headers = map[string]string{"accept": want.Headers["accept"], "authorization": want.Headers["authorization"]}
				if !reflect.DeepEqual(got[i], want) {
					t.Errorf("request %d: port %+v, NeoPi %+v", i, got[i], want)
				}
			}
		})
	}
}

func TestNeoPiCompletionsIdentity(t *testing.T) {
	compat := &coreauth.Auth{ID: "c", Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "cerebras"}}
	key, catalog, ok := neoPiCompletionsIdentity(compat)
	if !ok || key != "openai-compatible-cerebras" || catalog != "cerebras" {
		t.Fatalf("compat identity = %q, %q, %v", key, catalog, ok)
	}
	region := &coreauth.Auth{ID: "r", Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "alibaba-token-plan-2", "provider_key": "openai-compatible-alibaba-token-plan-2"}}
	if key, catalog, ok = neoPiCompletionsIdentity(region); !ok || key != "openai-compatible-alibaba-token-plan-2" || catalog != "alibaba-token-plan" {
		t.Fatalf("regional identity = %q, %q, %v", key, catalog, ok)
	}
	native := &coreauth.Auth{ID: "n", Provider: "groq"}
	if key, catalog, ok = neoPiCompletionsIdentity(native); !ok || key != "groq" || catalog != "groq" {
		t.Fatalf("native identity = %q, %q, %v", key, catalog, ok)
	}
	if _, _, ok = neoPiCompletionsIdentity(&coreauth.Auth{ID: "x", Provider: "kimi"}); ok {
		t.Fatal("kimi is not a completions account")
	}
}
