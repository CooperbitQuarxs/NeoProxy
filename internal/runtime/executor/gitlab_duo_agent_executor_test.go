package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/duoworkflow"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// duoAgentSocket is a scripted workflow socket: it opens at once and
// answers each send with the next step's frames. A failing socket refuses
// sends, as one that closed does.
type duoAgentSocket struct {
	mu      sync.Mutex
	steps   [][]string
	sent    []string
	failing bool
	deliver func(duoworkflow.SocketEvent)
	queue   chan duoworkflow.SocketEvent
}

func (s *duoAgentSocket) Send(data string) error {
	s.mu.Lock()
	if s.failing {
		s.mu.Unlock()
		return errors.New("socket is not open")
	}
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

func (s *duoAgentSocket) fail() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = true
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

// duoAgentFakes stands up a GitLab instance and a fresh runtime whose
// sockets answer with steps; sockets lists the sockets opened so far, and
// requests the instance's requests as "METHOD path".
func duoAgentFakes(t *testing.T, steps [][]string) (gitlabURL string, sockets func() []*duoAgentSocket, requests func() []string) {
	t.Helper()
	var seen []string
	var seenMu sync.Mutex
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		seenMu.Unlock()
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
	t.Cleanup(gitlab.Close)
	var opened []*duoAgentSocket
	var mu sync.Mutex
	dial := gitlabDuoAgentDial
	runtime := gitlabDuoAgentRuntime
	gitlabDuoAgentRuntime = duoworkflow.NewRuntime()
	gitlabDuoAgentDial = func(context.Context, *config.Config, *cliproxyauth.Auth) duoworkflow.Dialer {
		return func(url string, headers map[string]string, deliver func(duoworkflow.SocketEvent)) duoworkflow.Conn {
			socket := &duoAgentSocket{deliver: deliver, queue: make(chan duoworkflow.SocketEvent, 64), steps: steps}
			mu.Lock()
			opened = append(opened, socket)
			mu.Unlock()
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
	return gitlab.URL, func() []*duoAgentSocket {
			mu.Lock()
			defer mu.Unlock()
			return append([]*duoAgentSocket(nil), opened...)
		}, func() []string {
			seenMu.Lock()
			defer seenMu.Unlock()
			return append([]string(nil), seen...)
		}
}

// duoAgentWeather is a workflow that asks for the lookup tool, then
// answers from its result.
func duoAgentWeather() [][]string {
	ask := duoAgentCheckpoint("RUNNING",
		map[string]string{"message_type": "request", "content": "What's the weather?"},
		map[string]string{"message_type": "agent", "message_sub_type": "reasoning", "message_id": "r1", "content": "Let me look."})
	answered := duoAgentCheckpoint("FINISHED",
		map[string]string{"message_type": "request", "content": "What's the weather?"},
		map[string]string{"message_type": "agent", "message_sub_type": "reasoning", "message_id": "r1", "content": "Let me look."},
		map[string]string{"message_type": "tool", "content": "Sunny"},
		map[string]string{"message_type": "agent", "message_id": "a1", "content": "It is sunny."})
	return [][]string{
		{ask, `{"requestID":"req-7","runMCPTool":{"toolName":"lookup","args":"{\"query\":\"weather\"}"}}`},
		{answered},
	}
}

const (
	duoAgentTools    = `"tools":[{"type":"function","function":{"name":"lookup","description":"Look a thing up.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}]`
	duoAgentAsk      = `{"model":"claude_sonnet_4_6_vertex","messages":[{"role":"system","content":"You are NeoPi."},{"role":"user","content":"What's the weather?"}],` + duoAgentTools + `}`
	duoAgentAnswered = `{"model":"claude_sonnet_4_6_vertex","stream":true,"messages":[{"role":"system","content":"You are NeoPi."},{"role":"user","content":"What's the weather?"},{"role":"assistant","content":null,"tool_calls":[{"id":"req-7","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"weather\"}"}}]},{"role":"tool","tool_call_id":"req-7","content":"Sunny"}],` + duoAgentTools + `}`
)

func duoAgentRequest(gitlabURL, body string) cliproxyexecutor.Request {
	info := registry.GitLabDuoAgentModelInfo(registry.GitLabDuoAgentRow("claude_sonnet_4_6_vertex", "Claude Sonnet 4.6", gitlabURL, 1_000_000))
	return cliproxyexecutor.Request{Model: "claude_sonnet_4_6_vertex", Payload: []byte(body), Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info}}
}

// TestGitLabDuoAgentExecutorServesAToolLoop runs NeoPi's tool loop through
// the executor: the workflow's tool action answers an OpenAI chat as a tool
// call, the tool result resumes the same workflow socket, and the
// configured payload rules reach the start request and the resumed tool
// answer as their last change.
func TestGitLabDuoAgentExecutorServesAToolLoop(t *testing.T) {
	gitlabURL, opened, _ := duoAgentFakes(t, duoAgentWeather())
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"clientVersion": "9.9"}}}}}
	exec := NewGitLabDuoAgentExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "gitlab-duo-agent-test", Provider: "gitlab-duo-agent", Attributes: map[string]string{"api_key": "glpat-test"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}

	first, err := exec.Execute(context.Background(), auth, duoAgentRequest(gitlabURL, duoAgentAsk), opts)
	if err != nil {
		t.Fatal(err)
	}
	message := gjson.GetBytes(first.Payload, "choices.0.message")
	if call := message.Get("tool_calls.0"); call.Get("id").String() != "req-7" || call.Get("function.name").String() != "lookup" || call.Get("function.arguments").String() != `{"query":"weather"}` ||
		gjson.GetBytes(first.Payload, "choices.0.finish_reason").String() != "tool_calls" || message.Get("reasoning_content").String() != "Let me look." {
		t.Fatalf("tool call turn: %s", first.Payload)
	}
	sockets := opened()
	if len(sockets) != 1 {
		t.Fatalf("opened %d sockets, want 1", len(sockets))
	}
	socket := sockets[0]
	start := gjson.Parse(socket.frames()[0]).Get("startRequest")
	if start.Get("clientVersion").String() != "9.9" || start.Get("goal").String() != "What's the weather?" || start.Get("mcpTools.0.name").String() != "lookup" {
		t.Fatalf("start request: %s", socket.frames()[0])
	}

	stream, err := exec.ExecuteStream(context.Background(), auth, duoAgentRequest(gitlabURL, duoAgentAnswered), opts)
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
	resumed := gjson.Get(frames[len(frames)-1], "actionResponse")
	if len(frames) != 2 || resumed.Get("requestID").String() != "req-7" || resumed.Get("plainTextResponse.response").String() != "Sunny" ||
		resumed.Get("clientVersion").String() != "9.9" {
		t.Fatalf("resume frames: %v", frames)
	}
	if n := len(opened()); n != 1 {
		t.Fatalf("the resume opened %d sockets, want the same one", n)
	}
}

// TestGitLabDuoAgentExecutorScopesWorkflows keeps one account's or caller's
// live workflow from another: the same session key under a different
// account or client key starts its own workflow, and the waiting one
// receives nothing.
func TestGitLabDuoAgentExecutorScopesWorkflows(t *testing.T) {
	cases := []struct {
		name      string
		authID    string
		clientKey string
	}{
		{name: "another account", authID: "gitlab-duo-agent-other", clientKey: "client-a"},
		{name: "another caller", authID: "gitlab-duo-agent-test", clientKey: "client-b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gitlabURL, opened, _ := duoAgentFakes(t, duoAgentWeather())
			exec := NewGitLabDuoAgentExecutor(&config.Config{})
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
			owner := &cliproxyauth.Auth{ID: "gitlab-duo-agent-test", Provider: "gitlab-duo-agent", Attributes: map[string]string{"api_key": "glpat-test"}}
			ask := duoAgentAsk[:len(duoAgentAsk)-1] + `,"prompt_cache_key":"shared"}`
			if _, err := exec.Execute(usage.WithAPIKey(context.Background(), "client-a"), owner, duoAgentRequest(gitlabURL, ask), opts); err != nil {
				t.Fatal(err)
			}
			other := &cliproxyauth.Auth{ID: tc.authID, Provider: "gitlab-duo-agent", Attributes: map[string]string{"api_key": "glpat-test"}}
			answered := duoAgentAnswered[:len(duoAgentAnswered)-1] + `,"prompt_cache_key":"shared"}`
			if _, err := exec.Execute(usage.WithAPIKey(context.Background(), tc.clientKey), other, duoAgentRequest(gitlabURL, answered), opts); err != nil {
				t.Fatal(err)
			}
			sockets := opened()
			if len(sockets) != 2 {
				t.Fatalf("opened %d sockets, want a workflow of its own", len(sockets))
			}
			if frames := sockets[0].frames(); len(frames) != 1 {
				t.Fatalf("the waiting workflow received %d frames: %v", len(frames), frames)
			}
		})
	}
}

// TestGitLabDuoAgentExecutorFailsAResumeTheSocketRefuses ends a turn whose
// tool answer the preserved socket cannot take as NeoPi's resume does: the
// turn fails, the workflow is stopped, and the next turn starts afresh.
func TestGitLabDuoAgentExecutorFailsAResumeTheSocketRefuses(t *testing.T) {
	gitlabURL, opened, requests := duoAgentFakes(t, duoAgentWeather())
	exec := NewGitLabDuoAgentExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "gitlab-duo-agent-test", Provider: "gitlab-duo-agent", Attributes: map[string]string{"api_key": "glpat-test"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	if _, err := exec.Execute(context.Background(), auth, duoAgentRequest(gitlabURL, duoAgentAsk), opts); err != nil {
		t.Fatal(err)
	}
	opened()[0].fail()
	// Without the failure the turn would wait on an answer to a frame
	// that was never sent; the bound keeps a regression from hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := exec.Execute(ctx, auth, duoAgentRequest(gitlabURL, duoAgentAnswered), opts)
	if err == nil || !strings.Contains(err.Error(), "GitLab Duo Workflow WebSocket error") {
		t.Fatalf("resume on a refusing socket: %v", err)
	}
	if !slices.Contains(requests(), "PATCH /api/v4/ai/duo_workflows/workflows/1001") {
		t.Fatalf("the workflow was not stopped: %v", requests())
	}
	if _, err = exec.Execute(context.Background(), auth, duoAgentRequest(gitlabURL, duoAgentAnswered), opts); err != nil {
		t.Fatal(err)
	}
	if n := len(opened()); n != 2 {
		t.Fatalf("the next turn opened %d sockets, want a fresh workflow", n)
	}
}

// TestGitLabDuoAgentExecutorQueuedTurnLeavesOnCancel lets a turn waiting
// behind its session's running turn leave when its request ends, leaving
// the running turn's workflow alone.
func TestGitLabDuoAgentExecutorQueuedTurnLeavesOnCancel(t *testing.T) {
	// The workflow never answers, so the first turn runs until cancelled.
	gitlabURL, opened, _ := duoAgentFakes(t, nil)
	exec := NewGitLabDuoAgentExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "gitlab-duo-agent-test", Provider: "gitlab-duo-agent", Attributes: map[string]string{"api_key": "glpat-test"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	ask := duoAgentAsk[:len(duoAgentAsk)-1] + `,"prompt_cache_key":"shared"}`
	running, stopRunning := context.WithCancel(context.Background())
	defer stopRunning()
	first := make(chan error, 1)
	go func() {
		_, err := exec.Execute(running, auth, duoAgentRequest(gitlabURL, ask), opts)
		first <- err
	}()
	// The bounds only keep a regression from hanging the suite.
	deadline := time.Now().Add(10 * time.Second)
	for sockets := opened(); len(sockets) == 0 || len(sockets[0].frames()) == 0; sockets = opened() {
		if time.Now().After(deadline) {
			t.Fatal("the first turn never started its workflow")
		}
		time.Sleep(time.Millisecond)
	}
	queued, leave := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, err := exec.Execute(queued, auth, duoAgentRequest(gitlabURL, ask), opts)
		second <- err
	}()
	leave()
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("the cancelled turn succeeded")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled turn kept waiting for the running one")
	}
	if frames := opened()[0].frames(); len(opened()) != 1 || len(frames) != 1 {
		t.Fatalf("the cancelled turn touched the workflow: %d sockets, frames %v", len(opened()), frames)
	}
	select {
	case err := <-first:
		t.Fatalf("the running turn ended early: %v", err)
	default:
	}
	stopRunning()
	<-first
}

// TestGitLabDuoAgentExecutorEndedRequestLeavesTheSession keeps a request
// that already ended from the session even when the session is free to
// admit it: the waiting tool answer still resumes the same workflow.
func TestGitLabDuoAgentExecutorEndedRequestLeavesTheSession(t *testing.T) {
	gitlabURL, opened, _ := duoAgentFakes(t, duoAgentWeather())
	exec := NewGitLabDuoAgentExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "gitlab-duo-agent-test", Provider: "gitlab-duo-agent", Attributes: map[string]string{"api_key": "glpat-test"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	if _, err := exec.Execute(context.Background(), auth, duoAgentRequest(gitlabURL, duoAgentAsk), opts); err != nil {
		t.Fatal(err)
	}
	ended, end := context.WithCancel(context.Background())
	end()
	// Admission between a free slot and an ended request is a coin toss;
	// enough tosses make one that lands on admission all but certain.
	for range 32 {
		if _, err := exec.Execute(ended, auth, duoAgentRequest(gitlabURL, duoAgentAnswered), opts); err == nil {
			t.Fatal("an ended request succeeded")
		}
	}
	if _, err := exec.Execute(context.Background(), auth, duoAgentRequest(gitlabURL, duoAgentAnswered), opts); err != nil {
		t.Fatal(err)
	}
	sockets := opened()
	if len(sockets) != 1 {
		t.Fatalf("opened %d sockets, want the waiting workflow resumed", len(sockets))
	}
	if frames := sockets[0].frames(); len(frames) != 2 || gjson.Get(frames[1], "actionResponse.requestID").String() != "req-7" {
		t.Fatalf("frames: %v", frames)
	}
}

// TestGitLabDuoAgentExecutorUsesTheConfiguredInstance keeps a key
// configured for a GitLab instance on that instance: its discovery, and a
// turn on the seed row the account serves before discovery answers.
func TestGitLabDuoAgentExecutorUsesTheConfiguredInstance(t *testing.T) {
	gitlabURL, opened, _ := duoAgentFakes(t, duoAgentWeather())
	instance := strings.TrimPrefix(gitlabURL, "http://")
	var strays []string
	var straysMu sync.Mutex
	ctx := helps.WithTransportWrapper(context.Background(), func(http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == instance {
				return http.DefaultTransport.RoundTrip(r)
			}
			straysMu.Lock()
			strays = append(strays, r.URL.Host)
			straysMu.Unlock()
			return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}, Request: r}, nil
		})
	})
	auth := &cliproxyauth.Auth{ID: "gitlab-duo-agent-test", Provider: "gitlab-duo-agent", Attributes: map[string]string{"api_key": "glpat-test", "base_url": gitlabURL}}
	_, _ = FetchGitLabDuoAgentModels(ctx, &config.Config{}, auth)
	seed := registry.GitLabDuoAgentSeedModels()[0]
	request := cliproxyexecutor.Request{Model: seed.ID, Payload: []byte(duoAgentAsk), Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": seed}}
	if _, err := NewGitLabDuoAgentExecutor(&config.Config{}).Execute(ctx, auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatal(err)
	}
	straysMu.Lock()
	defer straysMu.Unlock()
	if len(strays) > 0 {
		t.Fatalf("requests left the configured instance for %v", strays)
	}
	if n := len(opened()); n != 1 {
		t.Fatalf("opened %d sockets, want 1", n)
	}
}
