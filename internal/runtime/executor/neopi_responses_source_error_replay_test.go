package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// These envelopes preserve the messages/status captured by the real NeoPi
// streamOpenAIResponses oracle against loopback SSE. In particular,
// the server_error has no type or retry hint in its message, and throttles put
// their error under status_details rather than response.error. The source
// reports the latter as "429 synthetic rate limit (rate_limit_exceeded)" for
// both response.failed and a failed terminal response.done.
func neoPiSourceErrorReplayCases() []struct {
	name    string
	event   string
	code    string
	message string
	status  int
} {
	return []struct {
		name    string
		event   string
		code    string
		message string
		status  int
	}{
		{
			name:    "response_failed_server_error",
			event:   `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"synthetic server failure"}}}`,
			code:    "server_error",
			message: "synthetic server failure",
		},
		{
			name:    "response_failed_status_details_rate_limit",
			event:   `{"type":"response.failed","response":{"status":"failed","status_details":{"error":{"code":"rate_limit_exceeded","message":"synthetic rate limit"}}}}`,
			code:    "rate_limit_exceeded",
			message: "synthetic rate limit",
			status:  http.StatusTooManyRequests,
		},
		{
			name:    "response_done_status_details_rate_limit",
			event:   `{"type":"response.done","response":{"status":"failed","status_details":{"error":{"code":"rate_limit_exceeded","message":"synthetic rate limit"}}}}`,
			code:    "rate_limit_exceeded",
			message: "synthetic rate limit",
			status:  http.StatusTooManyRequests,
		},
	}
}

const neoPiSourceErrorCreated = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"source-rejected-attempt\",\"status\":\"in_progress\",\"output\":[]}}\n\n"

// Withhold every byte of the replay until its request has arrived. This makes
// premature stream commitment observable without sleeps or network timing.
func TestNeoPiResponsesSourceErrorsReplayOnceBeforeSafeOutput(t *testing.T) {
	for _, tc := range neoPiSourceErrorReplayCases() {
		for _, streaming := range []bool{false, true} {
			mode := "execute"
			if streaming {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				var attempts atomic.Int32
				replayStarted := make(chan struct{})
				releaseReplay := make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseReplay) }) }
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					switch attempts.Add(1) {
					case 1:
						_, _ = io.WriteString(w, neoPiSourceErrorCreated+"data: "+tc.event+"\n\n")
					case 2:
						close(replayStarted)
						select {
						case <-releaseReplay:
							_, _ = io.WriteString(w, neoPiResponsesTestSSE)
						case <-r.Context().Done():
						}
					default:
						http.Error(w, "unexpected extra replay", http.StatusBadRequest)
					}
				}))
				defer server.Close()
				defer cancel()
				defer release()
				exec := NewNeoPiResponsesExecutor("meta", &config.Config{})
				auth := &cliproxyauth.Auth{ID: "source-error-replay", Provider: "meta", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "auth_kind": "apikey"}}
				req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi"}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				type outcome struct {
					result  *cliproxyexecutor.StreamResult
					payload []byte
					err     error
				}
				returned := make(chan outcome, 1)
				go func() {
					if streaming {
						result, err := exec.ExecuteStream(ctx, auth, req, opts)
						returned <- outcome{result: result, err: err}
					} else {
						response, err := exec.Execute(ctx, auth, req, opts)
						returned <- outcome{payload: response.Payload, err: err}
					}
				}()
				select {
				case <-replayStarted:
				case got := <-returned:
					t.Fatalf("returned before replay started: stream=%t error=%v attempts=%d", got.result != nil, got.err, attempts.Load())
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				select {
				case got := <-returned:
					t.Fatalf("returned before safe replay output: stream=%t error=%v", got.result != nil, got.err)
				default:
				}
				release()
				got := <-returned
				if got.err != nil {
					t.Fatalf("replay failed: %v", got.err)
				}
				body := string(got.payload)
				if streaming {
					if got.result == nil {
						t.Fatal("successful replay returned no stream")
					}
					var output strings.Builder
					for chunk := range got.result.Chunks {
						if chunk.Err != nil {
							t.Errorf("successful replay emitted error: %v", chunk.Err)
						}
						output.Write(chunk.Payload)
					}
					body = output.String()
					if strings.Count(body, `"type":"response.created"`) != 1 || strings.Count(body, `"type":"response.completed"`) != 1 {
						t.Errorf("consumer did not receive exactly one successful stream: %s", body)
					}
				}
				if !strings.Contains(body, "Hello from Muse.") {
					t.Errorf("safe replay output missing: %s", body)
				}
				if strings.Contains(body, "source-rejected-attempt") || strings.Contains(body, tc.message) || strings.Contains(body, tc.code) || strings.Contains(body, `"type":"response.failed"`) || strings.Contains(body, `"type":"response.done"`) {
					t.Errorf("rejected attempt leaked to consumer: %s", body)
				}
				if got := attempts.Load(); got != 2 {
					t.Errorf("upstream attempts = %d, want exactly one replay (2)", got)
				}
			})
		}
	}
}

func TestNeoPiResponsesSourceErrorsPreserveFailureAfterOneReplay(t *testing.T) {
	for _, tc := range neoPiSourceErrorReplayCases() {
		for _, streaming := range []bool{false, true} {
			mode := "execute"
			if streaming {
				mode = "stream"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				var attempts atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, neoPiSourceErrorCreated+"data: "+tc.event+"\n\n")
				}))
				defer server.Close()
				exec := NewNeoPiResponsesExecutor("meta", &config.Config{})
				auth := &cliproxyauth.Auth{ID: "source-error-exhausted", Provider: "meta", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "auth_kind": "apikey"}}
				req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi"}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				var consumerErr error
				if streaming {
					result, err := exec.ExecuteStream(t.Context(), auth, req, opts)
					consumerErr = err
					if result != nil {
						t.Error("failed created-only attempt committed a consumer stream")
						for chunk := range result.Chunks {
							if len(chunk.Payload) != 0 {
								t.Errorf("failed attempt emitted payload: %s", chunk.Payload)
							}
							if chunk.Err != nil {
								consumerErr = chunk.Err
							}
						}
					}
					if err == nil {
						t.Error("expected error before stream commitment")
					}
				} else {
					response, err := exec.Execute(t.Context(), auth, req, opts)
					consumerErr = err
					if len(response.Payload) != 0 {
						t.Errorf("failed turn returned success payload: %s", response.Payload)
					}
				}
				if consumerErr == nil {
					t.Error("source failure reported as success")
				} else {
					if !strings.Contains(consumerErr.Error(), tc.message) || !strings.Contains(consumerErr.Error(), tc.code) {
						t.Errorf("source error details lost: %v", consumerErr)
					}
					if tc.status != 0 {
						if got := statusCodeFromTestError(t, consumerErr); got != tc.status {
							t.Errorf("consumer status = %d, want %d; error: %v", got, tc.status, consumerErr)
						}
					}
				}
				if got := attempts.Load(); got != 2 {
					t.Errorf("upstream attempts = %d, want exactly one replay (2)", got)
				}
			})
		}
	}
}
