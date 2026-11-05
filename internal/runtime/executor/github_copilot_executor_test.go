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
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// githubCopilotCapture is testdata/neopi_github_copilot/<api>.json.gz:
// NeoPi's OWN streamSimple over every bundled Copilot model on one API,
// against a loopback server, beside the downstream request a client sends
// NeoProxy for each scenario.
type githubCopilotCapture struct {
	Wire      string `json:"wire"`
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		Downstream   json.RawMessage   `json:"downstream"`
		RequestedURL string            `json:"requestedURL"`
		Method       string            `json:"method"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
	} `json:"fixtures"`
}

// The Copilot executor sends, for every bundled model on each API, the
// request NeoPi's own transport sends for the same scenario: method, path,
// body bytes and headers.
func TestGitHubCopilotReplaysNeoPiRequests(t *testing.T) {
	formats := map[string]sdktranslator.Format{
		"anthropic-messages": sdktranslator.FormatClaude,
		"openai-completions": sdktranslator.FormatOpenAI,
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
	bundled, err := registry.GitHubCopilotBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, wire := range []string{"anthropic-messages", "openai-completions", "openai-responses"} {
		capture := loadGitHubCopilotCapture(t, "testdata/neopi_github_copilot/"+wire+".json.gz")
		t.Run(wire, func(t *testing.T) {
			exec := NewGitHubCopilotExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: "github-copilot-replay", Provider: "github-copilot", Metadata: map[string]any{"type": "github-copilot", "access_token": capture.APIKey}}
			for _, fixture := range capture.Fixtures {
				t.Run(fixture.Name, func(t *testing.T) {
					var row *registry.NeoPiModel
					for _, candidate := range bundled {
						if candidate.ID == fixture.Model {
							row = candidate.Clone()
						}
					}
					if row == nil {
						t.Fatalf("no bundled row %s", fixture.Model)
					}
					requested, err := url.Parse(fixture.RequestedURL)
					if err != nil {
						t.Fatal(err)
					}
					row.BaseURL = server.URL
					var downstream bytes.Buffer
					if err = json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					sent.body = nil
					_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
						Model:    fixture.Model,
						Payload:  downstream.Bytes(),
						Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitHubCopilotModelInfo(row)},
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

func loadGitHubCopilotCapture(t *testing.T, path string) githubCopilotCapture {
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
	var capture githubCopilotCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatalf("%s holds no fixtures", path)
	}
	return capture
}
