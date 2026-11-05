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
	"strings"
	"sync"
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
			Provider string               `json:"provider"`
			Scenario string               `json:"scenario"`
			Replies  []neoPiCapturedReply `json:"replies"`
			// Roster and Live answer ClinePass's two hosts.
			Roster   *neoPiCapturedReply    `json:"roster"`
			Live     *neoPiCapturedReply    `json:"live"`
			Requests []neoPiCapturedRequest `json:"requests"`
			// Keyless results ran NeoPi's manager without a key.
			Keyless bool `json:"keyless"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, result := range capture.Results {
		// Each provider's first scenario, and its keyless one.
		key, name := capture.APIKey, result.Provider
		if result.Keyless {
			key, name = "", result.Provider+"/no key"
		} else if seen[result.Provider] {
			continue
		}
		seen[result.Provider] = true
		if result.Roster != nil {
			t.Run(result.Provider, func(t *testing.T) {
				testNeoPiClinePassDiscoveryRequests(t, capture.APIKey, result.Roster, result.Live, result.Requests)
			})
			continue
		}
		t.Run(name, func(t *testing.T) {
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
				"api_key": key, "base_url": server.URL + base.Path,
			}}
			// Without a key NeoPi serves a keyed provider's bundled roster
			// unfetched; the port refuses discovery before any request.
			if _, err := fetchNeoPiCompletionsModels(result.Provider)(context.Background(), &config.Config{}, auth); err != nil && !(result.Keyless && len(result.Requests) == 0) {
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

type neoPiCapturedReply struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// testNeoPiClinePassDiscoveryRequests compares ClinePass's two discovery
// requests: the roster with Cline's client identity and no key, and
// OpenRouter's catalog with no headers of NeoPi's own.
func testNeoPiClinePassDiscoveryRequests(t *testing.T, key string, roster, live *neoPiCapturedReply, want []neoPiCapturedRequest) {
	var mu sync.Mutex
	got := map[string]http.Header{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got[r.URL.Path] = r.Header.Clone()
		mu.Unlock()
		reply := roster
		if r.URL.Path == "/api/v1/models" {
			reply = live
		}
		w.WriteHeader(reply.Status)
		_, _ = w.Write([]byte(reply.Body))
	}))
	defer server.Close()
	modelsURL, liveURL := neoPiClinePassModelsURL, neoPiClinePassLiveCatalogURL
	neoPiClinePassModelsURL = server.URL + "/api/v1/ai/cline/recommended-models"
	neoPiClinePassLiveCatalogURL = server.URL + "/api/v1/models"
	defer func() { neoPiClinePassModelsURL, neoPiClinePassLiveCatalogURL = modelsURL, liveURL }()
	auth := &coreauth.Auth{ID: "a", Provider: "cline-pass", Attributes: map[string]string{"api_key": key}}
	if _, err := fetchNeoPiCompletionsModels("cline-pass")(context.Background(), &config.Config{}, auth); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("requests: port sent %d, NeoPi %d", len(got), len(want))
	}
	for _, request := range want {
		header, ok := got[request.Path]
		if !ok {
			t.Errorf("port never requested %s", request.Path)
			continue
		}
		for name, value := range request.Headers {
			if header.Get(name) != value {
				t.Errorf("%s header %s: port %q, NeoPi %q", request.Path, name, header.Get(name), value)
			}
		}
		if _, keyed := request.Headers["authorization"]; !keyed && header.Get("Authorization") != "" {
			t.Errorf("%s: port sent the account key", request.Path)
		}
	}
}

// Charm Hyper's manager lists its public /v1/models with or without a key and
// sends no Authorization without one; a keyed-only provider still refuses.
func TestNeoPiCompletionsDiscoveryWithoutKey(t *testing.T) {
	var mu sync.Mutex
	var paths, authorizations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths, authorizations = append(paths, r.URL.Path), append(authorizations, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"hyper-fixture","object":"model"}]}`))
	}))
	defer server.Close()

	hyper := &coreauth.Auth{ID: "h", Provider: "charm-hyper", Attributes: map[string]string{"base_url": server.URL + "/v1"}}
	if _, err := fetchNeoPiCompletionsModels("charm-hyper")(context.Background(), &config.Config{}, hyper); err != nil {
		t.Fatalf("keyless Hyper discovery: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/v1/models" || authorizations[0] != "" {
		t.Fatalf("keyless Hyper requests = %v, Authorization %q", paths, authorizations)
	}

	groq := &coreauth.Auth{ID: "g", Provider: "groq", Attributes: map[string]string{"base_url": server.URL + "/openai/v1"}}
	if _, err := fetchNeoPiCompletionsModels("groq")(context.Background(), &config.Config{}, groq); err == nil || !strings.Contains(err.Error(), "no api key") {
		t.Fatalf("keyless groq discovery error = %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("keyless groq still requested: %v", paths)
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

// An existing openai-compatibility entry keeps its configured models when it
// moves onto NeoPi's executor: each is listed under its alias and display
// name and carries NeoPi's record for its upstream name, so the request still
// goes out under that name with NeoPi's wire policy.
func TestNeoPiCompletionsKeepConfiguredModels(t *testing.T) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: "cerebras",
		Models: []config.OpenAICompatibilityModel{
			{Name: "gpt-oss-120b", Alias: "my-oss", DisplayName: "My OSS"},
			{Name: "zz-private-model"},
		},
	}}}
	auth := &coreauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "cerebras"}}
	roster := registry.NeoPiCompletionsBundledModels("cerebras", "cerebras")
	got := map[string]*ModelInfo{}
	for _, model := range neoPiConfiguredCompletionsModels(cfg, auth, "cerebras", roster) {
		got[model.ID] = model
	}
	if len(got) != len(roster)+2 {
		t.Fatalf("got %d models, want the %d-row roster plus 2 configured", len(got), len(roster))
	}
	aliased := got["my-oss"]
	bundled := registry.NeoPiCompletionsModel("cerebras", "gpt-oss-120b")
	if aliased == nil || aliased.DisplayName != "My OSS" || aliased.NeoPi == nil || aliased.NeoPi.ID != "gpt-oss-120b" ||
		!reflect.DeepEqual(aliased.NeoPi.CompletionsCompat, bundled.CompletionsCompat) {
		t.Fatalf("aliased model = %+v", aliased)
	}
	private := got["zz-private-model"]
	unknown := registry.NeoPiCompletionsModel("cerebras", "zz-private-model")
	if private == nil || private.NeoPi == nil || private.NeoPi.ID != "zz-private-model" ||
		!reflect.DeepEqual(private.NeoPi.CompletionsCompat, unknown.CompletionsCompat) {
		t.Fatalf("configured unknown model = %+v", private)
	}
}
