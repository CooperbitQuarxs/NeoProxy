package executor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestNeoPiResponsesRequestFaultContract(t *testing.T) {
	for _, provider := range []string{"xai", "meta"} {
		for _, kind := range []string{"error", "response.failed"} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", provider, kind, streaming), func(t *testing.T) {
					frame := `{"type":"error","error":{"code":"invalid_request_error","message":"Correct the request"}}`
					wantText := "Error Code invalid_request_error: Correct the request"
					if kind == "response.failed" {
						frame = `{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_request_error","message":"Correct the request"}}}`
						wantText = "invalid_request_error: Correct the request"
					}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+frame+"\n\n")
					}))
					defer upstream.Close()
					var exec cliproxyauth.ProviderExecutor = NewXAIExecutor(&config.Config{})
					model := "grok-4.3"
					if provider == "meta" {
						exec = NewNeoPiResponsesExecutor("meta", &config.Config{})
						model = "muse-spark-1.3"
					}
					auth := &cliproxyauth.Auth{ID: t.Name(), Provider: provider, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "auth_kind": "apikey"}}
					req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","input":"hi"}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
					var consumerErr error
					if streaming {
						result, err := exec.ExecuteStream(t.Context(), auth, req, opts)
						consumerErr = err
						if result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									consumerErr = chunk.Err
								}
							}
						}
					} else {
						_, consumerErr = exec.Execute(t.Context(), auth, req, opts)
					}
					if consumerErr == nil {
						t.Fatal("expected upstream failure")
					}
					if got := consumerErr.Error(); got != wantText {
						t.Errorf("Error() = %q, want NeoPi text %q", got, wantText)
					}
					wrapped := fmt.Errorf("outer: %w", consumerErr)
					if !clienterror.IsRequestFault(clienterror.HTTPStatusFromError(wrapped), wrapped) {
						t.Errorf("invalid_request_error not classified as request fault: %v", consumerErr)
					}
					var carrier interface{ ResponseBody() []byte }
					if !errors.As(wrapped, &carrier) {
						t.Error("missing structured response body contract")
					} else if got := string(carrier.ResponseBody()); got != frame {
						t.Errorf("ResponseBody() = %s, want original event %s", got, frame)
					}
				})
			}
		}
	}
}
