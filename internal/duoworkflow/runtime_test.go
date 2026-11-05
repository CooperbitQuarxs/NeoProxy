package duoworkflow

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/tidwall/gjson"
)

type runtimeCapture struct {
	Model struct {
		ID      string `json:"id"`
		BaseURL string `json:"baseUrl"`
	} `json:"model"`
	Scenarios []struct {
		Name   string                `json:"name"`
		APIKey string                `json:"apiKey"`
		Env    map[string]string     `json:"env"`
		Script [][][]json.RawMessage `json:"script"`
		Turns  []capturedTurn        `json:"turns"`
	} `json:"scenarios"`
}

type capturedTurn struct {
	Body string `json:"body"`
	Rest []struct {
		Method   string            `json:"method"`
		URL      string            `json:"url"`
		Headers  map[string]string `json:"headers"`
		Body     string            `json:"body"`
		Response struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		} `json:"response"`
	} `json:"rest"`
	SocketsOpened []struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	} `json:"socketsOpened"`
	Sent []struct {
		Socket int      `json:"socket"`
		Frames []string `json:"frames"`
	} `json:"sent"`
	Closed []int `json:"closed"`
	Events []struct {
		Type         string  `json:"type"`
		ContentIndex *int    `json:"contentIndex"`
		Delta        *string `json:"delta"`
	} `json:"events"`
	Final json.RawMessage `json:"final"`
}

// TestRuntimeMatchesNeoPiCapture replays NeoPi's own streamSimple over the
// gitlab-duo-agent model (testdata/capture_neopi_gitlab_duo_agent_runtime.ts)
// turn by turn: the same GitLab answers and socket frames must produce the
// same REST requests, sockets, sent frames, closes, events and message.
func TestRuntimeMatchesNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/gitlab_duo_agent_runtime.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture runtimeCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"GITLAB_DUO_WORKFLOW_DEFINITION", "GITLAB_DUO_NAMESPACE_ID", "GITLAB_DUO_PROJECT_ID", "GITLAB_DUO_PROJECT_PATH"} {
		t.Setenv(name, "")
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			for name, value := range scenario.Env {
				t.Setenv(name, value)
			}
			sockets := &scriptedSockets{t: t, script: scenario.Script}
			gitlab := &scriptedGitLab{t: t}
			runtime := NewRuntime()
			for index, turn := range scenario.Turns {
				label := fmt.Sprintf("turn %d", index)
				parsed, err := cursorwire.ParseOpenAIChat([]byte(turn.Body), func(string) string { return "" })
				if err != nil {
					t.Fatal(err)
				}
				gitlab.expect(label, turn)
				openedBefore := sockets.count()
				sentBefore := sockets.sentCounts()
				var events []Event
				output := runtime.Stream(t.Context(), &Request{
					APIKey: scenario.APIKey, BaseURL: capture.Model.BaseURL, ModelID: capture.Model.ID,
					SessionID: "session-" + scenario.Name, Context: &parsed.Context, Client: &http.Client{Transport: gitlab},
					Options: Options{ToolChoiceNone: gjson.Get(turn.Body, "tool_choice").String() == "none"},
					Sink:    func(event Event) { events = append(events, event) },
					Dial:    sockets.dial,
				})
				sockets.settle()
				gitlab.done()
				sockets.check(label, turn, openedBefore, sentBefore)
				checkEvents(t, label, turn, events)
				checkFinal(t, label, turn.Final, output)
			}
		})
	}
}

func checkEvents(t *testing.T, label string, turn capturedTurn, events []Event) {
	t.Helper()
	got := make([]string, len(events))
	for i, event := range events {
		got[i] = event.Type
		switch event.Type {
		case "start", "done", "error":
		default:
			got[i] += ":" + strconv.Itoa(event.ContentIndex)
		}
		if strings.HasSuffix(event.Type, "_delta") {
			got[i] += ":" + strconv.Quote(event.Delta)
		}
	}
	want := make([]string, len(turn.Events))
	for i, event := range turn.Events {
		want[i] = event.Type
		if event.ContentIndex != nil {
			want[i] += ":" + strconv.Itoa(*event.ContentIndex)
		}
		if event.Delta != nil {
			want[i] += ":" + strconv.Quote(*event.Delta)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s events:\n got %v\nwant %v", label, got, want)
	}
}

// checkFinal compares the message as the capture summarizes it.
func checkFinal(t *testing.T, label string, final json.RawMessage, output *Output) {
	t.Helper()
	content := make([]any, len(output.Content))
	for i, block := range output.Content {
		switch block.Type {
		case "text":
			content[i] = map[string]any{"type": "text", "text": block.Text}
		case "thinking":
			content[i] = map[string]any{"type": "thinking", "thinking": block.Text}
		default:
			var arguments any
			if err := json.Unmarshal([]byte(cursorwire.StringifyJSON(block.Arguments)), &arguments); err != nil {
				t.Fatal(err)
			}
			content[i] = map[string]any{"type": block.Type, "id": block.ID, "name": block.Name, "arguments": arguments}
		}
	}
	summary := map[string]any{
		"content":    content,
		"stopReason": output.StopReason,
		"usage":      map[string]any{"input": float64(output.Usage.Input), "totalTokens": float64(output.Usage.Total)},
	}
	if output.StopDetails != "" {
		summary["stopDetails"] = map[string]any{"type": output.StopDetails}
	}
	if output.ErrorMessage != "" {
		summary["errorMessage"] = output.ErrorMessage
	}
	var want any
	if err := json.Unmarshal(final, &want); err != nil {
		t.Fatal(err)
	}
	var got any
	encoded, _ := json.Marshal(summary)
	_ = json.Unmarshal(encoded, &got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s final message:\n got %s\nwant %s", label, encoded, final)
	}
}

// scriptedGitLab answers the turn's REST calls with NeoPi's recorded
// answers, in order, after checking each request against NeoPi's.
type scriptedGitLab struct {
	t     *testing.T
	mu    sync.Mutex
	label string
	turn  capturedTurn
	next  int
}

func (g *scriptedGitLab) expect(label string, turn capturedTurn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.label, g.turn, g.next = label, turn, 0
}

func (g *scriptedGitLab) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next != len(g.turn.Rest) {
		g.t.Errorf("%s: %d REST requests, NeoPi sent %d", g.label, g.next, len(g.turn.Rest))
	}
}

func (g *scriptedGitLab) RoundTrip(req *http.Request) (*http.Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	index := g.next
	g.next++
	if index >= len(g.turn.Rest) {
		g.t.Errorf("%s: unexpected %s %s", g.label, req.Method, req.URL)
		return &http.Response{StatusCode: http.StatusTeapot, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}
	want := g.turn.Rest[index]
	if req.Method != want.Method || req.URL.String() != want.URL || string(body) != want.Body {
		g.t.Errorf("%s request %d:\n got %s %s %s\nwant %s %s %s", g.label, index, req.Method, req.URL, body, want.Method, want.URL, want.Body)
	}
	headers := map[string]string{}
	for name, values := range req.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	if len(body) > 0 {
		headers["content-length"] = strconv.Itoa(len(body))
	}
	if !reflect.DeepEqual(headers, want.Headers) {
		g.t.Errorf("%s request %d headers:\n got %v\nwant %v", g.label, index, headers, want.Headers)
	}
	return &http.Response{
		StatusCode: want.Response.Status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(want.Response.Body)),
		Request:    req,
	}, nil
}

// scriptedSockets stands in for the workflow socket as the capture's
// ScriptedSocket does: it opens at once, and answers each send with the
// script's next step of frames.
type scriptedSockets struct {
	t       *testing.T
	mu      sync.Mutex
	script  [][][]json.RawMessage
	sockets []*scriptedConn
	pending sync.WaitGroup
}

type scriptedConn struct {
	owner          *scriptedSockets
	steps          [][]json.RawMessage
	url            string
	headers        map[string]string
	sent           []string
	closedByClient bool
	events         chan SocketEvent
}

func (s *scriptedSockets) dial(url string, headers map[string]string, deliver func(SocketEvent)) Conn {
	s.mu.Lock()
	conn := &scriptedConn{owner: s, url: url, headers: headers, events: make(chan SocketEvent, 1024)}
	if index := len(s.sockets); index < len(s.script) {
		conn.steps = s.script[index]
	}
	s.sockets = append(s.sockets, conn)
	s.mu.Unlock()
	go func() {
		for event := range conn.events {
			deliver(event)
			s.pending.Done()
		}
	}()
	conn.enqueue(SocketEvent{Type: "open"})
	return conn
}

func (c *scriptedConn) enqueue(event SocketEvent) {
	c.owner.pending.Add(1)
	c.events <- event
}

func (c *scriptedConn) Send(data string) error {
	c.owner.mu.Lock()
	c.sent = append(c.sent, data)
	var frames []json.RawMessage
	if step := len(c.sent) - 1; step < len(c.steps) {
		frames = c.steps[step]
	}
	c.owner.mu.Unlock()
	for _, frame := range frames {
		var text string
		if err := json.Unmarshal(frame, &text); err == nil {
			c.enqueue(SocketEvent{Type: "message", Data: text})
			continue
		}
		var closing struct {
			Close  int    `json:"close"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(frame, &closing); err != nil {
			c.owner.t.Fatalf("frame %s: %v", frame, err)
		}
		c.enqueue(SocketEvent{Type: "close", Code: closing.Close, Reason: closing.Reason})
	}
	return nil
}

func (c *scriptedConn) Close() {
	c.owner.mu.Lock()
	if c.closedByClient {
		c.owner.mu.Unlock()
		return
	}
	c.closedByClient = true
	c.owner.mu.Unlock()
	c.enqueue(SocketEvent{Type: "close", Code: 1005})
}

// settle waits until every frame and close the sockets owe has been
// delivered, as the capture sleeps after each turn.
func (s *scriptedSockets) settle() { s.pending.Wait() }

func (s *scriptedSockets) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sockets)
}

func (s *scriptedSockets) sentCounts() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := make([]int, len(s.sockets))
	for i, conn := range s.sockets {
		counts[i] = len(conn.sent)
	}
	return counts
}

func (s *scriptedSockets) check(label string, turn capturedTurn, openedBefore int, sentBefore []int) {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	opened := s.sockets[openedBefore:]
	if len(opened) != len(turn.SocketsOpened) {
		s.t.Fatalf("%s: opened %d sockets, NeoPi %d", label, len(opened), len(turn.SocketsOpened))
	}
	for i, conn := range opened {
		want := turn.SocketsOpened[i]
		if conn.url != want.URL {
			s.t.Fatalf("%s socket %d URL:\n got %s\nwant %s", label, openedBefore+i, conn.url, want.URL)
		}
		if !reflect.DeepEqual(conn.headers, want.Headers) {
			s.t.Fatalf("%s socket %d headers:\n got %v\nwant %v", label, openedBefore+i, conn.headers, want.Headers)
		}
	}
	type sent struct {
		socket int
		frames []string
	}
	var got []sent
	for i, conn := range s.sockets {
		before := 0
		if i < len(sentBefore) {
			before = sentBefore[i]
		}
		if frames := conn.sent[before:]; len(frames) > 0 {
			got = append(got, sent{socket: i, frames: frames})
		}
	}
	var want []sent
	for _, entry := range turn.Sent {
		want = append(want, sent{socket: entry.Socket, frames: entry.Frames})
	}
	if !reflect.DeepEqual(got, want) {
		s.t.Fatalf("%s sent frames:\n got %v\nwant %v", label, got, want)
	}
	closed := []int{}
	for i, conn := range s.sockets {
		if conn.closedByClient {
			closed = append(closed, i)
		}
	}
	wantClosed := append([]int{}, turn.Closed...)
	if !reflect.DeepEqual(closed, wantClosed) {
		s.t.Fatalf("%s closed sockets: got %v, NeoPi %v", label, closed, wantClosed)
	}
}
