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

// Command Code's executor sends, for every bundled row NeoPi routes over
// Anthropic Messages or Responses, the request NeoPi's own transport sends
// for the same scenario (testdata/neopi_commandcode, captured by
// capture_neopi_github_copilot.ts): method, path, body bytes and headers.
// Its Chat Completions rows replay with the other completions providers.
func TestCommandCodeReplaysNeoPiRoutedRequests(t *testing.T) {
	formats := map[string]sdktranslator.Format{
		"anthropic-messages": sdktranslator.FormatClaude,
		"openai-responses":   sdktranslator.FormatOpenAIResponse,
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
	for _, wire := range []string{"anthropic-messages", "openai-responses"} {
		capture := loadGitHubCopilotCapture(t, "testdata/neopi_commandcode/"+wire+".json.gz")
		t.Run(wire, func(t *testing.T) {
			exec := NewNeoPiCompletionsExecutor("commandcode", &config.Config{}, NeoPiCompletionsProfile{})
			auth := &cliproxyauth.Auth{ID: "commandcode-replay", Provider: "commandcode", Attributes: map[string]string{"api_key": capture.APIKey}}
			for _, fixture := range capture.Fixtures {
				t.Run(fixture.Name, func(t *testing.T) {
					row := registry.NeoPiCompletionsModel("commandcode", fixture.Model)
					if row == nil || row.API != wire {
						t.Fatalf("no bundled %s row %s", wire, fixture.Model)
					}
					requested, err := url.Parse(fixture.RequestedURL)
					if err != nil {
						t.Fatal(err)
					}
					// The loopback server stands in for the host; the row keeps its path.
					base, err := url.Parse(row.BaseURL)
					if err != nil {
						t.Fatal(err)
					}
					row.BaseURL = server.URL + base.Path
					var downstream bytes.Buffer
					if err = json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					sent.body = nil
					_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
						Model:    fixture.Model,
						Payload:  downstream.Bytes(),
						Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.NeoPiCompletionsModelInfo(row, "commandcode")},
					}, cliproxyexecutor.Options{SourceFormat: formats[wire], OriginalRequest: downstream.Bytes(),
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
		})
	}
}
