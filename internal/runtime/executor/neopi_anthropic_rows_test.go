package executor

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
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// neoPiAnthropicRowsCapture is one testdata/neopi_anthropic/<provider>.json.gz:
// NeoPi's OWN streamSimple over every bundled row the provider serves over
// Anthropic Messages, against a loopback server, one request per scenario,
// beside the downstream Messages request a client sends NeoProxy for it.
type neoPiAnthropicRowsCapture struct {
	Provider  string `json:"provider"`
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		BaseURL      string            `json:"baseUrl"`
		Method       string            `json:"method"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

// A provider's Anthropic-routed bundled rows go through NeoPiCompletionsExecutor
// to NeoPi's Anthropic Messages port on each row's endpoint, and every
// captured NeoPi request is sent byte for byte: method, path, headers and body.
func TestNeoPiAnthropicRowsReplayNeoPiRequests(t *testing.T) {
	files, err := filepath.Glob("testdata/neopi_anthropic/*.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no captures")
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
	for _, file := range files {
		capture := loadNeoPiAnthropicRowsCapture(t, file)
		t.Run(capture.Provider, func(t *testing.T) {
			rows := map[string]*registry.ModelInfo{}
			for _, info := range registry.NeoPiCompletionsBundledModels(capture.Provider, capture.Provider) {
				rows[info.ID] = info
			}
			exec := NewNeoPiCompletionsExecutor(capture.Provider, &config.Config{}, NeoPiCompletionsProfile{})
			auth := &cliproxyauth.Auth{ID: capture.Provider + "-replay", Provider: capture.Provider, Attributes: map[string]string{"api_key": capture.APIKey}}
			for _, fixture := range capture.Fixtures {
				t.Run(fixture.Name, func(t *testing.T) {
					info := rows[fixture.Model]
					if info == nil || info.NeoPi == nil {
						t.Fatalf("no bundled row %s", fixture.Model)
					}
					if info.NeoPi.API != "anthropic-messages" || info.NeoPi.BaseURL != fixture.BaseURL {
						t.Fatalf("row %s: %s at %s, NeoPi anthropic-messages at %s", fixture.Model, info.NeoPi.API, info.NeoPi.BaseURL, fixture.BaseURL)
					}
					rowURL, err := url.Parse(fixture.BaseURL)
					if err != nil {
						t.Fatal(err)
					}
					// The row as registered, served from the capture server.
					routed := *info
					routed.NeoPi = info.NeoPi.Clone()
					routed.NeoPi.BaseURL = server.URL + rowURL.Path
					var downstream bytes.Buffer
					if err := json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					sent.body = nil
					_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
						Model:    fixture.Model,
						Payload:  downstream.Bytes(),
						Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": &routed},
					}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: downstream.Bytes(),
						Headers: http.Header{"X-Claude-Code-Session-Id": {capture.SessionID}}})
					if sent.body == nil {
						t.Fatal("executor sent no request")
					}
					if sent.method != fixture.Method {
						t.Errorf("method = %s, want %s", sent.method, fixture.Method)
					}
					requested, err := url.Parse(fixture.RequestedURL)
					if err != nil {
						t.Fatal(err)
					}
					if sent.path != requested.RequestURI() {
						t.Errorf("path = %s, want %s", sent.path, requested.RequestURI())
					}
					if string(sent.body) != fixture.RawBody {
						t.Errorf("body:\n got %s\nwant %s", sent.body, fixture.RawBody)
					}
					// host names the capture server; content-length follows the
					// body compared above.
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
		})
	}
}

// An endpoint the account names itself, NeoPi's provider baseUrl override,
// moves every bundled row, Anthropic Messages rows of a mixed Token Plan
// included; an Anthropic endpoint named with its /v1 drops it before
// /v1/messages (normalizeAnthropicBaseUrl).
func TestNeoPiAnthropicRowsFollowAccountBaseURL(t *testing.T) {
	for _, c := range []struct{ provider, base, path string }{
		{"minimax", "/anthropic", "/anthropic/v1/messages"},
		{"minimax", "/anthropic/v1", "/anthropic/v1/messages"},
		{"minimax-code", "/gateway", "/gateway/v1/messages"},
	} {
		var path string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			w.WriteHeader(http.StatusBadRequest)
		}))
		exec := NewNeoPiCompletionsExecutor(c.provider, &config.Config{}, NeoPiCompletionsProfile{})
		auth := &cliproxyauth.Auth{ID: c.provider + "-account", Provider: c.provider, Attributes: map[string]string{"api_key": "sk-minimax", "base_url": server.URL + c.base}}
		payload := []byte(`{"model":"MiniMax-M3","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
		_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "MiniMax-M3", Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload})
		server.Close()
		if path != c.path {
			t.Errorf("%s at %s: request path = %q, want %q", c.provider, c.base, path, c.path)
		}
	}
}

func loadNeoPiAnthropicRowsCapture(t *testing.T, path string) neoPiAnthropicRowsCapture {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture neoPiAnthropicRowsCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatalf("%s holds no fixtures", path)
	}
	return capture
}
