package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	umansauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/umans"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// umansProfiles is testdata/umans_npi_profiles.json: NeoPi's OWN umans
// discovery and buildModel over testdata/umans_models_info.json.
type umansProfiles struct {
	APIKey           string `json:"apiKey"`
	DiscoveryRequest struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
	} `json:"discoveryRequest"`
	Models []registry.NeoPiModel `json:"models"`
}

func loadUmansProfiles(t *testing.T) umansProfiles {
	t.Helper()
	data, err := os.ReadFile("testdata/umans_npi_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var profiles umansProfiles
	if err = json.Unmarshal(data, &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles.Models) == 0 {
		t.Fatal("profiles have no models")
	}
	return profiles
}

// Discovery asks what NeoPi asks and resolves every /v1/models/info entry as
// NeoPi's mapper and buildModel do: name fallbacks, vision only for true,
// effort ladders and modes, requires-effort, default level, limit fallbacks
// onto the bundled row, and the resolved Anthropic compat, in id order.
func TestUmansDiscoveryMatchesNeoPi(t *testing.T) {
	profiles := loadUmansProfiles(t)
	payload, err := os.ReadFile("testdata/umans_models_info.json")
	if err != nil {
		t.Fatal(err)
	}
	var sent *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r.Clone(context.Background())
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": profiles.APIKey, "base_url": server.URL + "/v1/"}}
	infos, err := FetchUmansModels(context.Background(), &config.Config{}, auth)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Method != profiles.DiscoveryRequest.Method || sent.URL.Path != strings.TrimPrefix(profiles.DiscoveryRequest.URL, umansauth.BaseURL) {
		t.Errorf("discovery = %s %s, want %s %s", sent.Method, sent.URL.Path, profiles.DiscoveryRequest.Method, profiles.DiscoveryRequest.URL)
	}
	for name, value := range profiles.DiscoveryRequest.Headers {
		if got := sent.Header.Get(name); got != value {
			t.Errorf("discovery header %s = %q, want %q", name, got, value)
		}
	}
	if len(infos) != len(profiles.Models) {
		t.Fatalf("models = %d, want %d", len(infos), len(profiles.Models))
	}
	for i, want := range profiles.Models {
		want.BaseURL = server.URL
		got, _ := json.Marshal(infos[i].NeoPi)
		expected, _ := json.Marshal(want)
		if !bytes.Equal(got, expected) {
			t.Errorf("model %d:\n got %s\nwant %s", i, got, expected)
		}
	}
}

// umansCapture is testdata/umans_npi_capture.json.gz: NeoPi's OWN
// streamSimple over its bundled and discovered umans rows against a loopback
// server, one request per scenario, beside the downstream request and headers
// a client sends NeoProxy for it.
type umansCapture struct {
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name              string            `json:"name"`
		Source            string            `json:"source"`
		Model             string            `json:"model"`
		Method            string            `json:"method"`
		RequestedURL      string            `json:"requestedURL"`
		Headers           map[string]string `json:"headers"`
		RawBody           string            `json:"rawBody"`
		Downstream        json.RawMessage   `json:"downstream"`
		DownstreamHeaders map[string]string `json:"downstreamHeaders"`
		// ConfiguredHeaders are NeoPi's model headers; the account carries
		// them as configured header:* attributes.
		ConfiguredHeaders map[string]string `json:"configuredHeaders"`
	} `json:"fixtures"`
}

// The executor sends every captured NeoPi request byte for byte: method, URL,
// headers (X-Api-Key, never Authorization; the gateway web search header) and
// body (escaped built-in tool names, each thinking mode, images, tool
// results).
func TestUmansExecutorReplaysNeoPiRequests(t *testing.T) {
	file, err := os.Open("testdata/umans_npi_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture umansCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("capture has no fixtures")
	}
	t.Setenv("UMANS_WEBSEARCH_PROVIDER", "")
	if err = os.Unsetenv("UMANS_WEBSEARCH_PROVIDER"); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		method, path string
		headers      http.Header
		body         []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.method, sent.path, sent.headers = r.Method, r.URL.RequestURI(), r.Header.Clone()
		sent.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	payload, err := os.ReadFile("testdata/umans_models_info.json")
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]map[string]*registry.ModelInfo{"bundled": {}, "discovered": {}}
	for _, info := range registry.UmansBundledModels() {
		info.NeoPi.BaseURL = server.URL
		models["bundled"][info.ID] = info
	}
	for _, info := range registry.UmansModels(payload, server.URL) {
		models["discovered"][info.ID] = info
	}
	exec := NewUmansExecutor("openai-compatible-umans", &config.Config{})
	base := map[string]string{"api_key": capture.APIKey, "base_url": umansauth.BaseURL}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			info := models[fixture.Source][fixture.Model]
			if info == nil {
				t.Fatalf("no %s row %s", fixture.Source, fixture.Model)
			}
			var downstream bytes.Buffer
			if err := json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			headers := http.Header{"X-Claude-Code-Session-Id": {capture.SessionID}}
			for name, value := range fixture.DownstreamHeaders {
				headers.Set(name, value)
			}
			attrs := maps.Clone(base)
			for name, value := range fixture.ConfiguredHeaders {
				attrs["header:"+name] = value
			}
			auth := &cliproxyauth.Auth{ID: "umans-replay", Provider: "openai-compatible-umans", Attributes: attrs}
			sent.body = nil
			_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:    fixture.Model,
				Payload:  downstream.Bytes(),
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info},
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: downstream.Bytes(), Headers: headers})
			if sent.body == nil {
				t.Fatal("executor sent no request")
			}
			if sent.method != fixture.Method {
				t.Errorf("method = %s, want %s", sent.method, fixture.Method)
			}
			if want := strings.TrimPrefix(fixture.RequestedURL, umansauth.BaseURL); sent.path != want {
				t.Errorf("path = %s, want %s", sent.path, want)
			}
			if string(sent.body) != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", sent.body, fixture.RawBody)
			}
			// host names the capture server; content-length follows the body
			// compared above.
			want := map[string]string{}
			for name, value := range fixture.Headers {
				if name != "host" && name != "content-length" {
					want[name] = value
				}
			}
			got := map[string]string{}
			for name, values := range sent.headers {
				if name != "Content-Length" {
					got[strings.ToLower(name)] = strings.Join(values, ", ")
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("headers:\n got %v\nwant %v", got, want)
			}
		})
	}
}
