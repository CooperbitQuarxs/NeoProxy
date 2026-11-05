package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func TestNeoPiResponsesWebSocketErrorRedaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"xai", "meta"} {
		for _, kind := range []string{"error", "response.failed"} {
			t.Run(provider+"/"+kind, func(t *testing.T) {
				node := responsesWebsocketSensitiveErrorNode()
				frame := `{"type":"error","error":` + node + `}`
				if kind == "response.failed" {
					frame = `{"type":"response.failed","response":{"status":"failed","error":` + node + `}}`
				}
				hook := &responsesWebsocketErrorLogHook{messages: make(chan string, 1)}
				logger := log.StandardLogger()
				previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
				previousLevel := logger.GetLevel()
				logger.AddHook(hook)
				logger.SetLevel(log.InfoLevel)
				defer logger.ReplaceHooks(previousHooks)
				defer logger.SetLevel(previousLevel)
				var attempts atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts.Add(1)
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
				h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
				router := gin.New()
				router.GET("/v1/responses", h.ResponsesWebsocket)
				downstream := httptest.NewServer(router)
				defer downstream.Close()
				c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				if err = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"`+model+`","input":[{"role":"user","content":"hi"}]}`)); err != nil {
					t.Fatal(err)
				}
				_, payload, err := c.ReadMessage()
				if err != nil {
					t.Fatalf("request fault must be exposed: %v", err)
				}
				assertResponsesWebsocketErrorRedaction(t, payload, "Correct the request")
				select {
				case message := <-hook.messages:
					marker := strings.Index(message, "payload=")
					if marker < 0 {
						t.Fatalf("downstream log missing payload: %s", message)
					}
					assertResponsesWebsocketErrorRedaction(t, []byte(message[marker+len("payload="):]), "Correct the request")
				case <-time.After(5 * time.Second):
					t.Fatal("missing downstream_out info log")
				}

			})
		}
	}
}

// Capture the actual downstream info log without racing a shared bytes.Buffer.
type responsesWebsocketErrorLogHook struct{ messages chan string }

func (*responsesWebsocketErrorLogHook) Levels() []log.Level { return []log.Level{log.InfoLevel} }
func (h *responsesWebsocketErrorLogHook) Fire(entry *log.Entry) error {
	if strings.Contains(entry.Message, "responses websocket: downstream_out") {
		select {
		case h.messages <- entry.Message:
		default:
		}
	}
	return nil
}

func assertResponsesWebsocketErrorRedaction(t *testing.T, payload []byte, wantMessage string) {
	t.Helper()
	for _, secret := range []string{"sk-review-secret", "review-secret", "review-refresh-secret", "review-note-secret", "review-child-secret"} {
		if strings.Contains(string(payload), secret) {
			t.Errorf("upstream secret %q leaked in client-visible output", secret)
		}
	}
	if gjson.GetBytes(payload, "type").String() != "error" || gjson.GetBytes(payload, "error.code").String() != "invalid_request_error" || gjson.GetBytes(payload, "error.message").String() != wantMessage {
		t.Errorf("ordinary error identity lost: %s", payload)
	}
	for _, path := range []string{"error.api_key", "error.metadata.authorization", "error.metadata.refresh_token", "error.metadata.children.0.client_secret"} {
		if got := gjson.GetBytes(payload, path).String(); got != "[REDACTED]" {
			t.Errorf("%s = %q, want redaction", path, got)
		}
	}
	if got := gjson.GetBytes(payload, "error.metadata.note").String(); got != "Bearer [REDACTED]" {
		t.Errorf("Bearer text = %q, want redaction", got)
	}
	if got := len([]rune(gjson.GetBytes(payload, "error.trace").String())); got != handlers.UpstreamErrorMessageLimit+1 {
		t.Errorf("trace length = %d, want bounded text (%d)", got, handlers.UpstreamErrorMessageLimit+1)
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(fmt.Errorf("invalid client-visible JSON: %w", err))
	}
}

func responsesWebsocketSensitiveErrorNode() string {
	return `{"code":"invalid_request_error","message":"Correct the request","api_key":"sk-review-secret","metadata":{"authorization":"Bearer review-secret","refresh_token":"review-refresh-secret","note":"Bearer review-note-secret","children":[{"client_secret":"review-child-secret"}]},"trace":"` + strings.Repeat("x", handlers.UpstreamErrorMessageLimit+128) + `"}`
}

type responsesWebsocketRawBodyError struct{ body string }

func (e responsesWebsocketRawBodyError) Error() string        { return e.body }
func (e responsesWebsocketRawBodyError) ResponseBody() []byte { return []byte(e.body) }

func TestResponsesWebsocketSanitizedCarrierDoesNotRestoreSecrets(t *testing.T) {
	node := strings.Replace(responsesWebsocketSensitiveErrorNode(), "Correct the request", "Correct the request: Bearer review-secret", 1)
	raw := responsesWebsocketRawBodyError{body: `{"error":` + node + `}`}
	safe := sanitizeResponsesStreamErrorMessage(&interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: raw})
	if strings.Contains(safe.Error.Error(), "review-secret") || strings.Contains(safe.Error.Error(), "review-refresh-secret") {
		t.Fatal("existing error sanitizer failed to redact probe")
	}
	if !clienterror.IsRequestFault(safe.StatusCode, safe.Error) {
		t.Fatal("sanitized wrapper lost internal request-fault classification")
	}
	payload, err := buildResponsesWebsocketErrorPayload(safe)
	if err != nil {
		t.Fatal(err)
	}
	assertResponsesWebsocketErrorRedaction(t, payload, "Correct the request: Bearer [REDACTED]")
	// Sanitizing a client-visible copy must not overwrite the internal carrier.
	if got := clienterror.ErrorBody(safe.Error); got != raw.body {
		t.Error("raw classification body was modified")
	}
}
