package executor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A subscription quota rejection is credential exhaustion, not a transient
// Responses failure. Preserve the original event through the Meta mapper so
// callers receive both credential scope and the provider's absolute reset.
func TestNeoPiResponsesSubscriptionQuotaPreservesCredentialScopeAndReset(t *testing.T) {
	for _, mode := range []string{"nonstream", "stream_before_output", "stream_after_output"} {
		t.Run(mode, func(t *testing.T) {
			resetAt := time.Unix(time.Now().Add(time.Hour).Unix(), 0)
			event := fmt.Sprintf(`{"type":"error","error":{"code":429,"message":"subscription quota exhausted","resets_at":%d}}`, resetAt.Unix())
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"quota-turn\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
				if mode == "stream_after_output" {
					_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"quota-message\",\"output_index\":0,\"content_index\":0,\"delta\":\"Partial answer\"}\n\n")
				}
				_, _ = io.WriteString(w, "data: "+event+"\n\n")
			}))
			defer server.Close()
			exec := NewNeoPiResponsesExecutor("meta", &config.Config{})
			auth := &cliproxyauth.Auth{ID: "subscription-quota", Provider: "meta", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL, "auth_kind": "apikey"}}
			req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			started := time.Now()
			var consumerErr error
			if mode == "nonstream" {
				response, err := exec.Execute(t.Context(), auth, req, opts)
				consumerErr = err
				if len(response.Payload) != 0 {
					t.Errorf("quota rejection returned a successful payload: %s", response.Payload)
				}
			} else {
				result, err := exec.ExecuteStream(t.Context(), auth, req, opts)
				consumerErr = err
				if mode == "stream_before_output" && (err == nil || result != nil) {
					t.Error("quota rejection before output must return an error without committing a stream")
				}
				if mode == "stream_after_output" && (err != nil || result == nil) {
					t.Errorf("quota rejection after output must arrive on the committed stream: %v", err)
				}
				if result != nil {
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							consumerErr = chunk.Err
						}
					}
				}
			}
			finished := time.Now()
			if got := attempts.Load(); got != 1 {
				t.Errorf("upstream attempts = %d, want 1 (subscription quota must not replay)", got)
			}
			if consumerErr == nil {
				t.Fatal("subscription quota was reported as success")
			}
			if got := statusCodeFromTestError(t, consumerErr); got != http.StatusTooManyRequests {
				t.Errorf("consumer status = %d, want 429; error: %v", got, consumerErr)
			}
			var scoped interface{ IsCredentialScoped() bool }
			if !errors.As(consumerErr, &scoped) || !scoped.IsCredentialScoped() {
				t.Errorf("subscription quota lost credential scope: %T: %v", consumerErr, consumerErr)
			}
			var retry interface{ RetryAfter() *time.Duration }
			if !errors.As(consumerErr, &retry) || retry.RetryAfter() == nil {
				t.Errorf("subscription quota lost resets_at retry hint: %T: %v", consumerErr, consumerErr)
			} else {
				// The mapper samples time.Now internally. Bracket that sample rather
				// than sleeping or comparing against a guessed fixed tolerance.
				got := *retry.RetryAfter()
				if min, max := resetAt.Sub(finished), resetAt.Sub(started); got < min || got > max {
					t.Errorf("RetryAfter = %s, want resets_at-derived duration in [%s, %s]", got, min, max)
				}
			}
			if !strings.Contains(consumerErr.Error(), "subscription quota exhausted") {
				t.Errorf("consumer error lost provider quota message: %v", consumerErr)
			}
		})
	}
}
