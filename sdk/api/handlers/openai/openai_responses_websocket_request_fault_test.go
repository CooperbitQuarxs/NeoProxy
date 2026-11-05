package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestNeoPiResponsesWebSocketRequestFault(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, provider := range []string{"xai", "meta"} {
		for _, kind := range []string{"error", "response.failed"} {
			t.Run(provider+"/"+kind, func(t *testing.T) {
				frame := `{"type":"error","error":{"code":"invalid_request_error","message":"Correct the request"}}`
				if kind == "response.failed" {
					frame = `{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_request_error","message":"Correct the request"}}}`
				}
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
					t.Fatalf("request fault concealed by WebSocket disconnect: %v (upstream attempts %d)", err, attempts.Load())
				}
				t.Logf("error payload: %s", payload)
				if gjson.GetBytes(payload, "type").String() != "error" || gjson.GetBytes(payload, "error.code").String() != "invalid_request_error" || gjson.GetBytes(payload, "error.message").String() != "Correct the request" {
					t.Fatalf("request fault details lost: %s", payload)
				}
			})
		}
	}
}
