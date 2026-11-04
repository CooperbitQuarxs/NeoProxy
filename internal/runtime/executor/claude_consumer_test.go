package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// Consumer tests share a real Messages/SSE capture rather than the removed
// executor's cloaking harness. No provider request can leave this local server.
func executeClaudeContextManagementRequest(t *testing.T, cfg *config.Config, payload []byte, stream bool) []byte {
	t.Helper()
	fixture := neoPiLoadFixture(t)
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		captured, err = io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, fixture.Response)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "claude", Metadata: map[string]any{"access_token": fixture.Token, "neopi_install_id": fixture.InstallID, "account_uuid": fixture.AccountID, "base_url": server.URL}}
	executor := NewClaudeExecutor(cfg)
	model := gjson.GetBytes(payload, "model").String()
	if model == "" {
		model = "claude-opus-5-5"
	}
	request := cliproxyexecutor.Request{Model: model, Payload: payload}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{"X-Claude-Code-Session-Id": []string{fixture.SessionID}}}
	if stream {
		result, err := executor.ExecuteStream(context.Background(), auth, request, options)
		if err != nil {
			t.Fatal(err)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
		}
	} else if _, err := executor.Execute(context.Background(), auth, request, options); err != nil {
		t.Fatal(err)
	}
	return captured
}
