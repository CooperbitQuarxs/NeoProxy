package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A local Ollama row sends the Responses request NeoPi's own transport sends
// for the same scenario (testdata/neopi_ollama, captured by
// capture_neopi_github_copilot.ts over rows NeoPi's Ollama manager discovered
// from /v1/models and /api/show): method, path, body bytes and headers.
func TestOllamaReplaysNeoPiResponsesRequests(t *testing.T) {
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
	capture := loadGitHubCopilotCapture(t, "testdata/neopi_ollama/openai-responses.json.gz")
	infos, err := registry.NeoPiOllamaListing("", registry.NewNeoPiOllamaShows(), func(endpoint string, body []byte, _ bool) (int, []byte, error) {
		switch {
		case strings.HasSuffix(endpoint, "/v1/models"):
			return 200, []byte(capture.Listing), nil
		case strings.HasSuffix(endpoint, "/api/show"):
			var request struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				return 0, nil, err
			}
			if show, ok := capture.Shows[request.Model]; ok {
				return 200, []byte(show), nil
			}
		}
		return 404, []byte("not found"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]*registry.ModelInfo{}
	for _, info := range infos {
		rows[info.ID] = info
	}
	exec := NewNeoPiCompletionsExecutor(registry.NeoPiOllamaProvider, &config.Config{}, NeoPiCompletionsProfile{})
	auth := &cliproxyauth.Auth{ID: "ollama-replay", Provider: registry.NeoPiOllamaProvider, Attributes: map[string]string{"api_key": capture.APIKey}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			info := rows[fixture.Model]
			if info == nil || info.NeoPi == nil || info.NeoPi.API != "openai-responses" {
				t.Fatalf("no discovered Responses row %s", fixture.Model)
			}
			requested, err := url.Parse(fixture.RequestedURL)
			if err != nil {
				t.Fatal(err)
			}
			// The loopback server stands in for the Ollama server; the row
			// keeps its path.
			row := info.NeoPi.Clone()
			rowBase, err := url.Parse(row.BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			row.BaseURL = server.URL + rowBase.Path
			var downstream bytes.Buffer
			if err = json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			sent.body = nil
			_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:    fixture.Model,
				Payload:  downstream.Bytes(),
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.NeoPiCompletionsModelInfo(row, registry.NeoPiOllamaProvider)},
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: downstream.Bytes(),
				Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: capture.SessionID}})
			if sent.body == nil {
				t.Fatal("executor sent no request")
			}
			if sent.method != fixture.Method || sent.path != requested.RequestURI() {
				t.Errorf("request = %s %s, want %s %s", sent.method, sent.path, fixture.Method, requested.RequestURI())
			}
			if string(sent.body) != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", sent.body, fixture.RawBody)
			}
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

// An account naming its server as Ollama documents it (http://host:11434,
// without /v1) is asked on /v1, where NeoPi's normalizeOllamaBaseUrl puts
// every configured base.
func TestOllamaConfiguredBaseGainsV1(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	infos, err := registry.NeoPiOllamaListing("", registry.NewNeoPiOllamaShows(), func(endpoint string, body []byte, _ bool) (int, []byte, error) {
		if body == nil && strings.HasSuffix(endpoint, "/v1/models") {
			return 200, []byte(`{"data":[{"id":"llama3.2:3b"}]}`), nil
		}
		return 404, []byte("not found"), nil
	})
	if err != nil || len(infos) != 1 {
		t.Fatalf("listing: %v (%d rows)", err, len(infos))
	}
	exec := NewNeoPiCompletionsExecutor(registry.NeoPiOllamaProvider, &config.Config{}, NeoPiCompletionsProfile{})
	auth := &cliproxyauth.Auth{ID: "ollama-lan", Provider: registry.NeoPiOllamaProvider, Attributes: map[string]string{"api_key": "k", "base_url": server.URL}}
	payload := []byte(`{"model":"llama3.2:3b","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`)
	_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "llama3.2:3b", Payload: payload,
		Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": infos[0]},
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload})
	if path != "/v1/responses" {
		t.Fatalf("request path %q, want /v1/responses", path)
	}
}
