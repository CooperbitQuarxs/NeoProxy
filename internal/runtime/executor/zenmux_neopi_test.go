package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// zenmuxCapture is testdata/zenmux_npi_capture.json.gz: NeoPi's OWN
// streamSimple over the zenmux rows it serves over Anthropic Messages
// (bundled, and resolved by its model manager from
// testdata/zenmux_models.json) against a loopback server, one request per
// scenario, beside the downstream request a client sends NeoProxy for it.
type zenmuxCapture struct {
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Source       string            `json:"source"`
		Model        string            `json:"model"`
		BaseURL      string            `json:"baseUrl"`
		Method       string            `json:"method"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

// The ZenMux executor hands each Anthropic-routed row to NeoPi's Anthropic
// Messages port on the row's /api/anthropic endpoint and sends every
// captured NeoPi request byte for byte: method, path, headers (Bearer auth,
// the betas each request needs) and body (each thinking mode, the resolved
// compat's Claude Code instruction and forced-choice rules, images, tool
// results, replayed thinking).
func TestZenMuxAnthropicRowsReplayNeoPiRequests(t *testing.T) {
	file, err := os.Open("testdata/zenmux_npi_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture zenmuxCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("capture has no fixtures")
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
	listing, err := os.ReadFile("testdata/zenmux_models.json")
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := registry.NeoPiCompletionsListing("zenmux", "https://zenmux.ai/api/v1", listing)
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]map[string]*registry.ModelInfo{"bundled": {}, "discovered": {}}
	for source, infos := range map[string][]*registry.ModelInfo{"bundled": registry.NeoPiCompletionsBundledModels("zenmux", "zenmux"), "discovered": discovered} {
		for _, info := range infos {
			models[source][info.ID] = info
		}
	}
	exec := NewNeoPiCompletionsExecutor("zenmux", &config.Config{}, NeoPiCompletionsProfile{})
	auth := &cliproxyauth.Auth{ID: "zenmux-replay", Provider: "zenmux", Attributes: map[string]string{"api_key": capture.APIKey}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			info := models[fixture.Source][fixture.Model]
			if info == nil || info.NeoPi == nil {
				t.Fatalf("no %s row %s", fixture.Source, fixture.Model)
			}
			if info.NeoPi.API != "anthropic-messages" || info.NeoPi.BaseURL != fixture.BaseURL {
				t.Fatalf("row %s: %s at %s, NeoPi anthropic-messages at %s", fixture.Model, info.NeoPi.API, info.NeoPi.BaseURL, fixture.BaseURL)
			}
			// The row as registered, served from the capture server.
			routed := *info
			routed.NeoPi = info.NeoPi.Clone()
			routed.NeoPi.BaseURL = server.URL + strings.TrimPrefix(fixture.BaseURL, "https://zenmux.ai")
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
			if want := strings.TrimPrefix(fixture.RequestedURL, "https://zenmux.ai"); sent.path != want {
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

// zenmuxSend runs one Messages request for model through the ZenMux executor
// for auth and reports the path and body the upstream received.
func zenmuxSend(t *testing.T, auth *cliproxyauth.Auth, model string, payload string) (string, []byte) {
	t.Helper()
	var path string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	auth.Attributes["base_url"] = strings.ReplaceAll(auth.Attributes["base_url"], "{server}", server.URL)
	exec := NewNeoPiCompletionsExecutor("zenmux", &config.Config{}, NeoPiCompletionsProfile{})
	_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: model, Payload: []byte(payload)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: []byte(payload)})
	return path, body
}

// An endpoint the account names itself is NeoPi's provider baseUrl override:
// every row still on a bundled endpoint follows it, Claude rows included, and
// an Anthropic endpoint named with its /v1 drops it before /v1/messages.
func TestZenMuxAccountEndpointOverridesBundledRows(t *testing.T) {
	payload := `{"model":"%s","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	for _, c := range []struct{ model, base, path string }{
		{"anthropic/claude-haiku-5.5", "{server}/api/v1", "/api/v1/messages"},
		{"anthropic/claude-haiku-5.5", "{server}/gateway/", "/gateway/v1/messages"},
		{"openai/chat-latest", "{server}/api/v1", "/api/v1/chat/completions"},
	} {
		auth := &cliproxyauth.Auth{ID: "zenmux-account", Provider: "zenmux", Attributes: map[string]string{"api_key": "sk-zenmux", "base_url": c.base}}
		if path, _ := zenmuxSend(t, auth, c.model, strings.Replace(payload, "%s", c.model, 1)); path != c.path {
			t.Errorf("%s at %s: path = %q, want %q", c.model, c.base, path, c.path)
		}
	}
}

// A replayed assistant turn whose only blocks are unsigned thinking is
// emptied on ZenMux's signing route and, as in NeoPi, left out.
func TestZenMuxDropsAssistantTurnEmptiedBySigningReplay(t *testing.T) {
	for name, assistant := range map[string]string{
		"unsigned":          `[{"type":"thinking","thinking":"unfinished reasoning","signature":""}]`,
		"unsigned-redacted": `[{"type":"thinking","thinking":"unfinished reasoning"},{"type":"redacted_thinking","data":"opaque"}]`,
	} {
		auth := &cliproxyauth.Auth{ID: "zenmux-account", Provider: "zenmux", Attributes: map[string]string{"api_key": "sk-zenmux", "base_url": "{server}/api/v1"}}
		payload := `{"model":"anthropic/claude-haiku-5.5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"First prompt"},{"role":"assistant","content":` + assistant + `},{"role":"user","content":"Continue"}]}`
		_, body := zenmuxSend(t, auth, "anthropic/claude-haiku-5.5", payload)
		var sent struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatalf("%s: %v (%s)", name, err, body)
		}
		for _, message := range sent.Messages {
			if message.Role == "assistant" {
				t.Errorf("%s: upstream received the emptied assistant turn: %s", name, body)
			}
		}
	}
}
