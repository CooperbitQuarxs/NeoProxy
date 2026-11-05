package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/sjson"
)

// Replay a captured NeoPi request per provider through the full executor and
// an actual HTTP transport, not only the request builder.
func TestNeoPiCompletionsTransportParity(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "neopi_completions", "*.fixtures.json.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("captures = %v, error = %v", files, err)
	}
	for _, file := range files {
		capture := readNeoPiCompletionsCapture(t, file)
		t.Run(capture.Provider, func(t *testing.T) {
			fixture := capture.Fixtures[0]
			if fixture.Error != "" {
				t.Fatal(fixture.Error)
			}
			var method, path, body string
			var headers http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path, headers = r.Method, r.URL.Path, r.Header.Clone()
				data, _ := io.ReadAll(r.Body)
				body = string(data)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"capture\",\"object\":\"chat.completion.chunk\",\"model\":\"capture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			payload, err := sjson.SetBytes(fixture.Downstream, "prompt_cache_key", capture.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			profile := NeoPiCompletionsProfile{URL: func(*registry.NeoPiModel) string { return server.URL + "/chat/completions" }}
			exec := NewNeoPiCompletionsExecutor(capture.Provider, nil, profile)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": capture.APIKey}}
			_, err = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: fixture.Model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
			if err != nil {
				t.Fatal(err)
			}
			if method != "POST" || path != "/chat/completions" {
				t.Errorf("request = %s %s", method, path)
			}
			if body != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
			}
			for name, want := range fixture.Headers {
				if name == "host" || name == "content-length" {
					continue
				}
				if got := strings.Join(headers.Values(name), ", "); got != want {
					t.Errorf("header %s = %q, want %q", name, got, want)
				}
			}
		})
	}
}
