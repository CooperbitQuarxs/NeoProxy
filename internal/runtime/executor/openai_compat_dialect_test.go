package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type dialectHit struct {
	path    string
	header  http.Header
	body    []byte
	rawPath string
}

// newDialectUpstream answers like a multi-dialect gateway (OpenCode): chat
// completions, Responses, Anthropic Messages and Gemini generateContent.
func newDialectUpstream(t *testing.T, hits *[]dialectHit) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*hits = append(*hits, dialectHit{path: r.URL.Path, rawPath: r.URL.RequestURI(), header: r.Header.Clone(), body: body})
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"glm","choices":[{"index":0,"message":{"role":"assistant","content":"from chat"},"finish_reason":"stop"}]}`))
		case strings.HasSuffix(r.URL.Path, "/messages"):
			if gjson.GetBytes(body, "stream").Bool() {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"qwen\",\"content\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n" +
					"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
					"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"from stream\"}}\n\n" +
					"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
					"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
					"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"m1","type":"message","role":"assistant","model":"qwen","content":[{"type":"text","text":"from messages"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
				"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"i1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"from responses\"}]}}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"))
		case strings.Contains(r.URL.Path, ":generateContent"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"from gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func dialectFixture(serverURL string) (*OpenAICompatExecutor, *cliproxyauth.Auth) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:          "opencode-go",
		BaseURL:       serverURL + "/zen/go/v1",
		SessionHeader: "x-opencode-session",
		Models: []config.OpenAICompatibilityModel{
			{Name: "glm-5.3-flash"},
			{Name: "qwen3.8-flash", Dialect: "messages"},
			{Name: "grok-4.7", Dialect: "responses"},
			{Name: "gemini-4-argon", Dialect: "generate-content", BaseURL: serverURL + "/inference/go/google/v1beta"},
		},
	}}}
	auth := &cliproxyauth.Auth{ID: "auth-1", Provider: "opencode-go", Attributes: map[string]string{
		"base_url": serverURL + "/zen/go/v1", "api_key": "test-key", "compat_name": "opencode-go", "provider_key": "opencode-go",
	}}
	return NewOpenAICompatExecutor("opencode-go", cfg), auth
}

func TestOpenAICompatExecutorRoutesEachModelToItsDialect(t *testing.T) {
	var hits []dialectHit
	server := newDialectUpstream(t, &hits)
	defer server.Close()
	executor, auth := dialectFixture(server.URL)

	cases := []struct {
		model, path, text string
		check             func(t *testing.T, hit dialectHit)
	}{
		{"glm-5.3-flash", "/zen/go/v1/chat/completions", "from chat", func(t *testing.T, hit dialectHit) {
			if hit.header.Get("Authorization") != "Bearer test-key" {
				t.Fatalf("chat auth = %q", hit.header.Get("Authorization"))
			}
		}},
		{"qwen3.8-flash", "/zen/go/v1/messages", "from messages", func(t *testing.T, hit dialectHit) {
			if hit.header.Get("x-api-key") != "test-key" || hit.header.Get("Authorization") != "" || hit.header.Get("anthropic-version") == "" {
				t.Fatalf("messages must authenticate with x-api-key only: %v", hit.header)
			}
			if gjson.GetBytes(hit.body, "stream").Bool() || gjson.GetBytes(hit.body, "model").String() != "qwen3.8-flash" {
				t.Fatalf("messages body = %s", hit.body)
			}
		}},
		{"grok-4.7", "/zen/go/v1/responses", "from responses", func(t *testing.T, hit dialectHit) {
			if hit.header.Get("Authorization") != "Bearer test-key" {
				t.Fatalf("responses auth = %q", hit.header.Get("Authorization"))
			}
			if !gjson.GetBytes(hit.body, "stream").Bool() || gjson.GetBytes(hit.body, "max_output_tokens").Int() != 16 {
				t.Fatalf("responses body must stream and keep the output limit: %s", hit.body)
			}
			if gjson.GetBytes(hit.body, "reasoning").Exists() {
				t.Fatalf("an unrequested reasoning effort must not be invented: %s", hit.body)
			}
		}},
		{"gemini-4-argon", "/inference/go/google/v1beta/models/gemini-4-argon:generateContent", "from gemini", func(t *testing.T, hit dialectHit) {
			if hit.header.Get("x-goog-api-key") != "test-key" || gjson.GetBytes(hit.body, "model").Exists() {
				t.Fatalf("gemini request: headers=%v body=%s", hit.header, hit.body)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			hits = nil
			resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   tc.model,
				Payload: []byte(`{"model":"` + tc.model + `","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if len(hits) != 1 || hits[0].path != tc.path {
				t.Fatalf("upstream hits = %+v, want one at %s", hits, tc.path)
			}
			if hits[0].header.Get("x-opencode-session") == "" {
				t.Fatalf("session header missing")
			}
			tc.check(t, hits[0])
			if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != tc.text {
				t.Fatalf("content = %q, want %q; payload=%s", got, tc.text, resp.Payload)
			}
		})
	}
}

func TestOpenAICompatExecutorStreamsMessagesDialect(t *testing.T) {
	var hits []dialectHit
	server := newDialectUpstream(t, &hits)
	defer server.Close()
	executor, auth := dialectFixture(server.URL)

	for _, source := range []string{"openai", "claude"} {
		t.Run(source, func(t *testing.T) {
			payload := `{"model":"qwen3.8-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"stream":true}`
			result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "qwen3.8-flash", Payload: []byte(payload)},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(source), Stream: true})
			if err != nil {
				t.Fatalf("ExecuteStream: %v", err)
			}
			var out strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream error: %v", chunk.Err)
				}
				out.Write(chunk.Payload)
			}
			if !strings.Contains(out.String(), "from stream") {
				t.Fatalf("stream output lacks the text: %s", out.String())
			}
			if source == "claude" && !strings.Contains(out.String(), "message_stop") {
				t.Fatalf("a Claude client must receive the events unchanged: %s", out.String())
			}
		})
	}
}

func TestResolveCompatDialectKeepsModelBaseOnEntryHost(t *testing.T) {
	compat := &config.OpenAICompatibility{Name: "gw", Models: []config.OpenAICompatibilityModel{
		{Name: "same", Dialect: "responses", BaseURL: "https://gw.example/inference/v1"},
		{Name: "other", BaseURL: "https://evil.example/v1"},
	}}
	route, err := resolveCompatDialect(compat, "https://gw.example/v1", cliproxyexecutor.Request{Model: "same"}, cliproxyexecutor.Options{})
	if err != nil || route.dialect != config.DialectResponses || route.baseURL != "https://gw.example/inference/v1" || route.format != sdktranslator.FormatCodex {
		t.Fatalf("same-host route = %+v, %v", route, err)
	}
	if _, err = resolveCompatDialect(compat, "https://gw.example/v1", cliproxyexecutor.Request{Model: "other"}, cliproxyexecutor.Options{}); err == nil {
		t.Fatalf("a model base URL on another host must be refused")
	}
	route, err = resolveCompatDialect(compat, "https://gw.example/v1", cliproxyexecutor.Request{Model: "same"}, cliproxyexecutor.Options{Alt: "responses/compact"})
	if err != nil || route.dialect != config.DialectChatCompletions {
		t.Fatalf("compact requests keep the chat path: %+v, %v", route, err)
	}
}
