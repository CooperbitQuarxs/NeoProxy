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

func neoPiStructuredErrorCases() []struct {
	name   string
	event  string
	status int
} {
	// None of the messages identifies a throttle or an overload. Classification
	// must use the structured status/code, not a textual-message heuristic.
	return []struct {
		name   string
		event  string
		status int
	}{
		{"error_status_429", `{"type":"error","error":{"status":429,"message":"Request rejected."}}`, http.StatusTooManyRequests},
		{"error_rate_limit_code", `{"type":"error","error":{"code":"rate_limit_exceeded","message":"Request rejected."}}`, http.StatusTooManyRequests},
		{"error_status_503", `{"type":"error","error":{"status":503,"message":"Request rejected."}}`, http.StatusServiceUnavailable},
		{"response_failed_status_429", `{"type":"response.failed","response":{"status":"failed","error":{"status":429,"message":"Request rejected."}}}`, http.StatusTooManyRequests},
		{"response_failed_rate_limit_code", `{"type":"response.failed","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"Request rejected."}}}`, http.StatusTooManyRequests},
		{"response_failed_status_503", `{"type":"response.failed","response":{"status":"failed","error":{"status":503,"message":"Request rejected."}}}`, http.StatusServiceUnavailable},
	}
}

const neoPiStructuredRejectedCreated = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"rejected-attempt\",\"status\":\"in_progress\",\"output\":[]}}\n\n"

// Both APIs must preserve the consumer-visible status after the one permitted
// replay is exhausted. A created-only attempt must not commit a stream result
// (and thereby prevent credential failover) before returning that error.
func TestNeoPiResponsesStructuredErrorsPreserveStatusBeforeCommit(t *testing.T) {
	for _, tc := range neoPiStructuredErrorCases() {
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
					_, _ = io.WriteString(w, neoPiStructuredRejectedCreated+"data: "+tc.event+"\n\n")
				}))
				defer server.Close()
				exec := NewNeoPiResponsesExecutor("meta", &config.Config{})
				auth := &cliproxyauth.Auth{ID: "structured-error", Provider: "meta", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "auth_kind": "apikey"}}
				req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi"}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				var consumerErr error
				if !streaming {
					response, err := exec.Execute(t.Context(), auth, req, opts)
					consumerErr = err
					if len(response.Payload) != 0 {
						t.Errorf("failed turn returned payload: %s", response.Payload)
					}
				} else {
					result, err := exec.ExecuteStream(t.Context(), auth, req, opts)
					consumerErr = err
					if result != nil {
						t.Error("created-only rejected attempt prematurely committed a stream result")
						for chunk := range result.Chunks {
							if len(chunk.Payload) != 0 {
								t.Errorf("rejected attempt emitted payload: %s", chunk.Payload)
							}
							if chunk.Err != nil {
								consumerErr = chunk.Err
							}
						}
					}
					if err == nil {
						t.Error("expected structured error from ExecuteStream before any stream commitment")
					}
				}
				if consumerErr == nil {
					t.Error("structured failure was reported as success")
				} else if got := statusCodeFromTestError(t, consumerErr); got != tc.status {
					t.Errorf("consumer status = %d, want %d; error: %v", got, tc.status, consumerErr)
				}
				if got := attempts.Load(); got != 2 {
					t.Errorf("upstream attempts = %d, want exactly one transparent replay (2)", got)
				}
			})
		}
	}
}

func TestNeoPiResponsesStructuredErrorsReplayCreatedOnlyAttemptTransparently(t *testing.T) {
	for _, tc := range neoPiStructuredErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			var attempts atomic.Int32
			replayStarted := make(chan struct{})
			releaseReplay := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseReplay) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				switch attempt {
				case 1:
					_, _ = io.WriteString(w, neoPiStructuredRejectedCreated+"data: "+tc.event+"\n\n")
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
			auth := &cliproxyauth.Auth{ID: "structured-replay", Provider: "meta", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "auth_kind": "apikey"}}
			req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi"}`)}
			type outcome struct {
				result *cliproxyexecutor.StreamResult
				err    error
			}
			returned := make(chan outcome, 1)
			go func() {
				result, err := exec.ExecuteStream(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
				returned <- outcome{result: result, err: err}
			}()

			// The server withholds every byte of the successful replay. Only the
			// rejected attempt's created/error frames have been available so far.
			select {
			case <-replayStarted:
			case got := <-returned:
				t.Fatalf("ExecuteStream returned before starting the transparent replay: result=%v error=%v attempts=%d", got.result != nil, got.err, attempts.Load())
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case got := <-returned:
				t.Fatalf("ExecuteStream returned before the replay emitted anything: result=%v error=%v", got.result != nil, got.err)
			default:
			}
			release()
			got := <-returned
			if got.err != nil {
				t.Fatalf("transparent replay failed: %v", got.err)
			}
			if got.result == nil {
				t.Fatal("transparent replay returned no stream")
			}
			var output strings.Builder
			for chunk := range got.result.Chunks {
				if chunk.Err != nil {
					t.Errorf("successful replay emitted error: %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			body := output.String()
			if strings.Contains(body, "rejected-attempt") || strings.Contains(body, "Request rejected.") || strings.Contains(body, `"type":"error"`) || strings.Contains(body, `"type":"response.failed"`) {
				t.Errorf("failed attempt leaked into consumer stream: %s", body)
			}
			if strings.Count(body, `"type":"response.created"`) != 1 || strings.Count(body, `"type":"response.completed"`) != 1 || !strings.Contains(body, "Hello from Muse.") {
				t.Errorf("consumer did not receive exactly the successful replay: %s", body)
			}
			if got := attempts.Load(); got != 2 {
				t.Errorf("upstream attempts = %d, want exactly one transparent replay (2)", got)
			}
		})
	}
}
