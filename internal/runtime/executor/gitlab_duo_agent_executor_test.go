package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/duoworkflow"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// duoAgentSocket is a scripted workflow socket: it opens at once and
// answers each send with the next step's frames.
type duoAgentSocket struct {
	mu      sync.Mutex
	steps   [][]string
	sent    []string
	deliver func(duoworkflow.SocketEvent)
	queue   chan duoworkflow.SocketEvent
}

func (s *duoAgentSocket) Send(data string) error {
	s.mu.Lock()
	s.sent = append(s.sent, data)
	var frames []string
	if step := len(s.sent) - 1; step < len(s.steps) {
		frames = s.steps[step]
	}
	s.mu.Unlock()
	for _, frame := range frames {
		s.queue <- duoworkflow.SocketEvent{Type: "message", Data: frame}
	}
	return nil
}

func (s *duoAgentSocket) Close() { s.queue <- duoworkflow.SocketEvent{Type: "close", Code: 1005} }

func (s *duoAgentSocket) frames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

func duoAgentCheckpoint(status string, log ...map[string]string) string {
	snapshot, _ := json.Marshal(map[string]any{"channel_values": map[string]any{"ui_chat_log": log}})
	frame, _ := json.Marshal(map[string]any{"newCheckpoint": map[string]any{"checkpoint": string(snapshot), "status": status}})
	return string(frame)
}

// TestGitLabDuoAgentExecutorServesAToolLoop runs NeoPi's tool loop through
// the executor: the workflow's tool action answers an OpenAI chat as a tool
// call, the tool result resumes the same workflow socket, and the
// configured payload rules reach the start request as its last change.
func TestGitLabDuoAgentExecutorServesAToolLoop(t *testing.T) {
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/groups":
			_, _ = io.WriteString(w, `[{"id":70,"full_path":"team","path":"team"}]`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v4/groups/70/projects":
			_, _ = io.WriteString(w, `[{"id":4242,"path_with_namespace":"team/app"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/ai/duo_workflows/direct_access":
			_, _ = io.WriteString(w, `{"gitlab_rails":{"token":"wf-token"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v4/ai/duo_workflows/workflows":
			_, _ = io.WriteString(w, `{"id":1001}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/graphql":
			_, _ = io.WriteString(w, `{"data":{"aiChatAvailableModels":{"pinnedModel":null}}}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer gitlab.Close()

	ask := duoAgentCheckpoint("RUNNING",
		map[string]string{"message_type": "request", "content": "What's the weather?"},
		map[string]string{"message_type": "agent", "message_sub_type": "reasoning", "message_id": "r1", "content": "Let me look."})
	answered := duoAgentCheckpoint("FINISHED",
		map[string]string{"message_type": "request", "content": "What's the weather?"},
		map[string]string{"message_type": "agent", "message_sub_type": "reasoning", "message_id": "r1", "content": "Let me look."},
		map[string]string{"message_type": "tool", "content": "Sunny"},
		map[string]string{"message_type": "agent", "message_id": "a1", "content": "It is sunny."})
	var sockets []*duoAgentSocket
	var socketsMu sync.Mutex
	dial := gitlabDuoAgentDial
	runtime := gitlabDuoAgentRuntime
	gitlabDuoAgentRuntime = duoworkflow.NewRuntime()
	gitlabDuoAgentDial = func(context.Context, *config.Config, *cliproxyauth.Auth) duoworkflow.Dialer {
		return func(url string, headers map[string]string, deliver func(duoworkflow.SocketEvent)) duoworkflow.Conn {
			socket := &duoAgentSocket{deliver: deliver, queue: make(chan duoworkflow.SocketEvent, 64), steps: [][]string{
				{ask, `{"requestID":"req-7","runMCPTool":{"toolName":"lookup","args":"{\"query\":\"weather\"}"}}`},
				{answered},
			}}
			socketsMu.Lock()
			sockets = append(sockets, socket)
			socketsMu.Unlock()
			go func() {
				for event := range socket.queue {
					deliver(event)
				}
			}()
			socket.queue <- duoworkflow.SocketEvent{Type: "open"}
			return socket
		}
	}
	t.Cleanup(func() { gitlabDuoAgentDial, gitlabDuoAgentRuntime = dial, runtime })

	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"clientVersion": "9.9"}}}}}
	exec := NewGitLabDuoAgentExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "gitlab-duo-agent-test", Provider: "gitlab-duo-agent", Attributes: map[string]string{"api_key": "glpat-test"}}
	info := registry.GitLabDuoAgentModelInfo(registry.GitLabDuoAgentRow("claude_sonnet_4_6_vertex", "Claude Sonnet 4.6", gitlab.URL, 1_000_000))
	request := func(body string) cliproxyexecutor.Request {
		return cliproxyexecutor.Request{Model: "claude_sonnet_4_6_vertex", Payload: []byte(body), Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info}}
	}
	tools := `"tools":[{"type":"function","function":{"name":"lookup","description":"Look a thing up.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}]`
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}

	first, err := exec.Execute(context.Background(), auth, request(`{"model":"claude_sonnet_4_6_vertex","messages":[{"role":"system","content":"You are NeoPi."},{"role":"user","content":"What's the weather?"}],`+tools+`}`), opts)
	if err != nil {
		t.Fatal(err)
	}
	message := gjson.GetBytes(first.Payload, "choices.0.message")
	if call := message.Get("tool_calls.0"); call.Get("id").String() != "req-7" || call.Get("function.name").String() != "lookup" || call.Get("function.arguments").String() != `{"query":"weather"}` ||
		gjson.GetBytes(first.Payload, "choices.0.finish_reason").String() != "tool_calls" || message.Get("reasoning_content").String() != "Let me look." {
		t.Fatalf("tool call turn: %s", first.Payload)
	}
	socketsMu.Lock()
	if len(sockets) != 1 {
		t.Fatalf("opened %d sockets, want 1", len(sockets))
	}
	socket := sockets[0]
	socketsMu.Unlock()
	start := gjson.Parse(socket.frames()[0]).Get("startRequest")
	if start.Get("clientVersion").String() != "9.9" || start.Get("goal").String() != "What's the weather?" || start.Get("mcpTools.0.name").String() != "lookup" {
		t.Fatalf("start request: %s", socket.frames()[0])
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, request(`{"model":"claude_sonnet_4_6_vertex","stream":true,"messages":[{"role":"system","content":"You are NeoPi."},{"role":"user","content":"What's the weather?"},{"role":"assistant","content":null,"tool_calls":[{"id":"req-7","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"weather\"}"}}]},{"role":"tool","tool_call_id":"req-7","content":"Sunny"}],`+tools+`}`), opts)
	if err != nil {
		t.Fatal(err)
	}
	var text, finish strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		data := strings.TrimPrefix(strings.TrimSpace(string(chunk.Payload)), "data: ")
		text.WriteString(gjson.Get(data, "choices.0.delta.content").String())
		finish.WriteString(gjson.Get(data, "choices.0.finish_reason").String())
	}
	if text.String() != "It is sunny." || finish.String() != "stop" {
		t.Fatalf("resumed turn: text %q, finish %q", text.String(), finish.String())
	}
	frames := socket.frames()
	if len(frames) != 2 || frames[1] != `{"actionResponse":{"requestID":"req-7","plainTextResponse":{"response":"Sunny"}}}` {
		t.Fatalf("resume frames: %v", frames)
	}
	socketsMu.Lock()
	defer socketsMu.Unlock()
	if len(sockets) != 1 {
		t.Fatalf("the resume opened %d sockets, want the same one", len(sockets))
	}
}
