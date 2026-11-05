package claude

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestClaudeNeoPiCarrierClientSafety(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, kind := range []string{"error", "response.failed", "response.failed/status_details", "response.completed", "response.incomplete", "response.done"} {
		for _, provider := range []string{"xai", "meta"} {
			for _, streaming := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", provider, kind, streaming), func(t *testing.T) {
					node := `{"code":"invalid_request_error","message":"Correct the request: Bearer review-message-secret","api_key":"sk-review-secret","metadata":{"authorization":"Bearer review-secret","refresh_token":"review-refresh-secret","nested":[{"client_secret":"review-child-secret"}]}}`
					frame := `{"type":"error","error":` + node + `}`
					if kind != "error" {
						event := kind
						location := "error"
						status := "failed"
						if kind == "response.failed/status_details" {
							event = "response.failed"
							location = "status_details"
							node = `{"error":` + node + `}`
						}
						if kind == "response.incomplete" {
							status = "cancelled"
						}
						frame = fmt.Sprintf(`{"type":%q,"response":{"status":%q,%q:%s}}`, event, status, location, node)
					}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+frame+"\n\n")
					}))
					defer upstream.Close()
					cfg := &config.Config{}
					manager := coreauth.NewManager(nil, nil, nil)
					manager.SetConfig(cfg)
					manager.RegisterExecutor(runtimeexecutor.NewXAIExecutor(cfg))
					manager.RegisterExecutor(runtimeexecutor.NewNeoPiResponsesExecutor("meta", cfg))
					model := "grok-4.3"
					if provider == "meta" {
						model = "muse-spark-1.3"
					}
					auth := &coreauth.Auth{ID: t.Name(), Provider: provider, Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "auth_kind": "apikey"}}
					if _, err := manager.Register(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
					defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
					h := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
					router := gin.New()
					router.POST("/v1/messages", h.ClaudeMessages)
					payload := fmt.Sprintf(`{"model":%q,"max_tokens":64,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, model, streaming)
					request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(payload))
					request.Header.Set("Content-Type", "application/json")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					body := response.Body.String()
					if response.Code != http.StatusBadGateway {
						t.Errorf("status=%d, want 502", response.Code)
					}
					if got := gjson.Get(body, "type").String(); got != "error" {
						t.Errorf("type=%q, want error", got)
					}
					if got := gjson.Get(body, "error.type").String(); got != "api_error" {
						t.Errorf("error.type=%q, want api_error", got)
					}
					if got := gjson.Get(body, "error.message").String(); got != "invalid_request_error: Correct the request: Bearer [REDACTED]" {
						t.Errorf("message=%q, want safe code/message", got)
					}
					for _, secret := range []string{"sk-review-secret", "review-secret", "review-refresh-secret", "review-child-secret", "review-message-secret"} {
						if strings.Contains(body, secret) {
							t.Errorf("raw secret %q disclosed to Claude client", secret)
						}
					}
					if strings.Contains(body, "response.failed") || strings.Contains(body, "api_key") || strings.Contains(body, "refresh_token") {
						t.Error("raw event envelope or metadata exposed")
					}

				})
			}
		}
	}
}

func TestClaudeUnknownCarrierNeverSerializesEvent(t *testing.T) {
	for _, body := range []string{
		`{"type":"response.done","response":{"status":"failed","metadata":{"api_key":"sk-review-secret"}}}`,
		`{"type":"response.failed","response":{"error":{"metadata":{"refresh_token":"review-refresh-secret"}}}}`,
		`{"type":"response.failed","response":{"error":`,
	} {
		raw := responseBodyOnlyClaudeError{body: []byte(body)}
		got := (&ClaudeCodeAPIHandler{}).toClaudeError(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: raw})
		if got.Error.Type != "api_error" || got.Error.Message != http.StatusText(http.StatusBadGateway) {
			t.Errorf("unrecognized carrier must use safe status fallback: %+v", got.Error)
		}
	}
}

func TestClaudeCarrierMessageUsesSharedTextBounds(t *testing.T) {
	raw := responseBodyOnlyClaudeError{body: []byte(`{"type":"response.failed","response":{"error":{"code":"invalid_request_error","message":"Bearer review-secret ` + strings.Repeat("x", 4096) + `"}}}`)}
	got := (&ClaudeCodeAPIHandler{}).toClaudeError(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: raw})
	if strings.Contains(got.Error.Message, "review-secret") || len([]rune(got.Error.Message)) != 2049 || !strings.HasSuffix(got.Error.Message, "…") {
		t.Errorf("carrier message must use existing redaction and 2048-rune limit: length=%d", len([]rune(got.Error.Message)))
	}
}
