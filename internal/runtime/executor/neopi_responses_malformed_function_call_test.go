package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// The pinned NeoPi capture reports attempts=1 and
// errorMessage="malformed_function_call: synthetic function failure" for this
// response.failed envelope. A successful second response must not hide it.
func TestNeoPiResponsesMalformedFunctionCallDoesNotReplay(t *testing.T) {
	for _, provider := range []string{"meta", "xai"} {
		for _, streaming := range []bool{false, true} {
			mode := "execute"
			if streaming {
				mode = "stream"
			}
			t.Run(provider+"/"+mode, func(t *testing.T) {
				var attempts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					if attempts.Add(1) == 1 {
						_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"malformed-attempt\",\"status\":\"in_progress\",\"output\":[]}}\n\n"+
							"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"malformed_function_call\",\"message\":\"synthetic function failure\"}}}\n\n")
						return
					}
					_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"unexpected-replay\",\"status\":\"in_progress\",\"output\":[]}}\n\n"+
						"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"unexpected-replay\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"unexpected replay succeeded\"}]}]}}\n\n")
				}))
				defer server.Close()

				meta := NewNeoPiResponsesExecutor("meta", &config.Config{})
				execute, executeStream := meta.Execute, meta.ExecuteStream
				model := "muse-spark-1.3"
				if provider == "xai" {
					xai := NewXAIExecutor(&config.Config{})
					execute, executeStream = xai.Execute, xai.ExecuteStream
					model = "grok-4.3"
				}
				auth := &cliproxyauth.Auth{ID: "malformed-function-" + provider, Provider: provider, Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "auth_kind": "apikey"}}
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","input":"hi"}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: streaming}
				var consumerErr error
				var output strings.Builder
				if streaming {
					result, err := executeStream(t.Context(), auth, req, opts)
					consumerErr = err
					if result != nil {
						for chunk := range result.Chunks {
							output.Write(chunk.Payload)
							if chunk.Err != nil {
								consumerErr = chunk.Err
							}
						}
					}
				} else {
					response, err := execute(t.Context(), auth, req, opts)
					consumerErr = err
					output.Write(response.Payload)
				}
				if consumerErr == nil {
					t.Error("malformed function failure reported as success")
				} else if consumerErr.Error() != "malformed_function_call: synthetic function failure" {
					t.Errorf("consumer error lost source format: %v", consumerErr)
				}
				if body := output.String(); strings.Contains(body, "response.completed") || strings.Contains(body, "unexpected replay succeeded") || strings.Contains(body, "unexpected-replay") {
					t.Errorf("malformed function failure exposed successful completion: %s", body)
				}
				if got := attempts.Load(); got != 1 {
					t.Errorf("upstream attempts = %d, want 1 (malformed function failures must not replay)", got)
				}
			})
		}
	}
}
