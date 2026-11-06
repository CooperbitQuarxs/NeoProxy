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

// A llama.cpp row sends the request NeoPi's own transport sends for the same
// scenario (testdata/neopi_llama.cpp, captured by
// capture_neopi_github_copilot.ts over rows NeoPi's runtime discovery built
// from /models and /props): a Qwen row on Chat Completions, the rest on
// Responses; method, path, body bytes and headers.
func TestLlamaCppReplaysNeoPiRequests(t *testing.T) {
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
	for _, wire := range []string{"openai-completions", "openai-responses"} {
		capture := loadGitHubCopilotCapture(t, "testdata/neopi_llama.cpp/"+wire+".json.gz")
		infos, err := registry.NeoPiLlamaCppListing(context.Background(), []string{"http://127.0.0.1:8080"}, func(_ context.Context, endpoint string) (int, []byte, error) {
			switch {
			case strings.HasSuffix(endpoint, "/models"):
				return 200, []byte(capture.Listing), nil
			case strings.HasSuffix(endpoint, "/props"):
				return 200, []byte(capture.Props), nil
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
		exec := NewNeoPiCompletionsExecutor(registry.NeoPiLlamaCppProvider, &config.Config{}, NeoPiCompletionsProfile{})
		auth := &cliproxyauth.Auth{ID: "llama-replay", Provider: registry.NeoPiLlamaCppProvider, Attributes: map[string]string{"api_key": capture.APIKey}}
		format := sdktranslator.FormatOpenAI
		if wire == "openai-responses" {
			format = sdktranslator.FormatOpenAIResponse
		}
		if len(capture.Fixtures) == 0 {
			t.Fatalf("%s: capture holds no fixtures", wire)
		}
		for _, fixture := range capture.Fixtures {
			t.Run(wire+"/"+fixture.Name, func(t *testing.T) {
				info := rows[fixture.Model]
				if info == nil || info.NeoPi == nil || info.NeoPi.API != wire {
					t.Fatalf("no discovered %s row %s", wire, fixture.Model)
				}
				requested, err := url.Parse(fixture.RequestedURL)
				if err != nil {
					t.Fatal(err)
				}
				// The loopback server stands in for llama-server; the row keeps
				// its path.
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
					Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.NeoPiCompletionsModelInfo(row, registry.NeoPiLlamaCppProvider)},
				}, cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: downstream.Bytes(),
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
}
