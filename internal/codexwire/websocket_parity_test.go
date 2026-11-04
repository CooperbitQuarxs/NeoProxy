package codexwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type websocketCapture struct {
	InstallID string          `json:"installId"`
	AccountID string          `json:"accountId"`
	Token     string          `json:"token"`
	SessionID string          `json:"sessionId"`
	BaseURL   string          `json:"baseUrl"`
	Discovery json.RawMessage `json:"discovery"`
	Turns     []struct {
		Name       string          `json:"name"`
		Downstream json.RawMessage `json:"downstream"`
		Handshakes []struct {
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
		} `json:"handshakes"`
		Frames       []string `json:"frames"`
		SteerOutcome string   `json:"steerOutcome"`
	} `json:"turns"`
}

// captureEvents is the capture server's reply to each response.create.
func captureEvents(id string) []string {
	item := fmt.Sprintf(`{"id":"msg_%s","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Captured.","annotations":[]}]}`, id)
	return []string{
		fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress","output":[]}}`, id),
		fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_%s","type":"message","role":"assistant","status":"in_progress","content":[]}}`, id),
		fmt.Sprintf(`{"type":"response.output_text.delta","item_id":"msg_%s","output_index":0,"content_index":0,"delta":"Captured."}`, id),
		fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s}`, item),
		fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[%s],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`, id, item),
	}
}

// One session over NeoPi's default WebSocket transport, sent through the turn
// runner to a socket server: the first turn sends the full transcript over one
// handshake, the next chains with previous_response_id and a delta on the same
// socket, an effort change rides a configuration_update, and live steering
// sends one response.steer to the streaming response.
func TestWebsocketTurnsMatchNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("../runtime/executor/testdata/codex_npi_ws_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture websocketCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(capture.Discovery) }))
	defer discovery.Close()
	catalog, err := codexauth.FetchModels(context.Background(), discovery.Client(), codexauth.ModelDiscoveryOptions{AccessToken: capture.Token, AccountID: capture.AccountID, BaseURL: discovery.URL, ClientVersion: ClientVersion})
	if err != nil || len(catalog.Models) != 1 {
		t.Fatalf("discovery: %v", err)
	}
	model := *catalog.Models[0].ModelInfo().Codex

	steeredResponse := 0
	for index, turn := range capture.Turns {
		if turn.SteerOutcome != "" {
			steeredResponse = index + 1
		}
	}
	if steeredResponse == 0 {
		t.Fatal("source capture has no steered turn")
	}
	var mu sync.Mutex
	var handshakes []http.Header
	var frames []string
	responses := 0
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		handshakes = append(handshakes, r.Header.Clone())
		mu.Unlock()
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		write := func(events []string) bool {
			for _, event := range events {
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(event)); errWrite != nil {
					return false
				}
			}
			return true
		}
		var held []string
		for {
			_, message, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			mu.Lock()
			frames = append(frames, string(message))
			mu.Unlock()
			// As the capture server: reject the steer, then finish the held response.
			if previous := gjson.GetBytes(message, "previous_response_id").String(); gjson.GetBytes(message, "type").String() == "response.steer" {
				ack := fmt.Sprintf(`{"type":"response.steer.failed","steer":{"previous_response_id":%q},"error":{"code":"capture_rejected","message":"Captured."}}`, previous)
				if !write(append([]string{ack}, held...)) {
					return
				}
				held = nil
				continue
			}
			mu.Lock()
			responses++
			id := fmt.Sprintf("resp_%d", responses)
			steered := responses == steeredResponse
			mu.Unlock()
			events := captureEvents(id)
			if steered {
				events, held = events[:3], events[3:]
			}
			if !write(events) {
				return
			}
		}
	}))
	defer server.Close()
	baseURL := server.URL

	sessions := NewSessions()
	dial := func(ctx context.Context, url string, header http.Header) (*websocket.Conn, *http.Response, error) {
		return (&websocket.Dialer{}).DialContext(ctx, url, header)
	}
	framing := map[string]bool{"host": true, "connection": true, "upgrade": true, "sec-websocket-key": true, "sec-websocket-version": true, "sec-websocket-extensions": true}
	sent := 0
	for _, turn := range capture.Turns {
		ctx, opts := FromResponses(turn.Downstream, &model)
		opts.SessionID = capture.SessionID
		request, errShape := sessions.Shape(&model, ctx, opts, Call{AccessToken: capture.Token, BaseURL: baseURL, InstallID: capture.InstallID, Transport: TransportWebsocket})
		if errShape != nil {
			t.Fatal(errShape)
		}
		var emitted []string
		runner := &Turn{Sessions: sessions, Request: request, Model: &model, Dial: dial, Do: http.DefaultClient.Do}
		var steering *onceSteering
		if turn.SteerOutcome != "" {
			var input []json.RawMessage
			for _, item := range gjson.Get(turn.Frames[1], "input").Array() {
				input = append(input, json.RawMessage(item.Raw))
			}
			steering = &onceSteering{input: input}
			runner.Steering = steering
		}
		outcome, errRun := runner.Run(context.Background(), func(event []byte) error {
			emitted = append(emitted, string(event))
			return nil
		})
		if errRun != nil {
			t.Fatalf("%s: %v", turn.Name, errRun)
		}
		if outcome.Transport != TransportWebsocket || len(emitted) != 5 {
			t.Fatalf("%s: transport %s emitted %d events", turn.Name, outcome.Transport, len(emitted))
		}
		if steering != nil && steering.outcome() != turn.SteerOutcome {
			t.Errorf("%s: steering %q, source %q", turn.Name, steering.outcome(), turn.SteerOutcome)
		}
		mu.Lock()
		sentFrames, sockets := append([]string(nil), frames[sent:]...), len(handshakes)
		mu.Unlock()
		sent += len(sentFrames)
		if len(sentFrames) != len(turn.Frames) {
			t.Fatalf("%s: sent %d frames, source sent %d", turn.Name, len(sentFrames), len(turn.Frames))
		}
		for i := range sentFrames {
			if got, want := normalizeRandom(sentFrames[i], capture.InstallID), normalizeRandom(turn.Frames[i], capture.InstallID); got != want {
				t.Errorf("%s frame %d\ngot    %s\nsource %s", turn.Name, i, got, want)
			}
		}
		if sockets != 1 {
			t.Errorf("%s: %d handshakes, NeoPi reuses one socket", turn.Name, sockets)
		}
	}
	got := map[string]string{}
	for name, values := range handshakes[0] {
		if !framing[strings.ToLower(name)] {
			got[strings.ToLower(name)] = normalizeRandom(strings.Join(values, ", "), capture.InstallID)
		}
	}
	want := map[string]string{}
	for name, value := range capture.Turns[0].Handshakes[0].Headers {
		if !framing[name] {
			want[name] = normalizeRandom(value, capture.InstallID)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("handshake\ngot    %v\nsource %v", got, want)
	}
	if gjson.Get(capture.Turns[1].Frames[0], "previous_response_id").String() != "resp_1" {
		t.Fatal("source capture lost its chained follow-up")
	}
}

// onceSteering submits one steer, like a client that steers once per turn.
type onceSteering struct {
	mu       sync.Mutex
	input    []json.RawMessage
	claimed  bool
	resolved string
}

func (s *onceSteering) Claim(ctx context.Context) (*SteerClaim, bool) {
	s.mu.Lock()
	claimed := s.claimed
	s.claimed = true
	s.mu.Unlock()
	if claimed {
		<-ctx.Done()
		return nil, false
	}
	return &SteerClaim{Input: s.input, Resolve: func(accepted bool, _ []byte) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.resolved = "rejected"
		if accepted {
			s.resolved = "accepted"
		}
	}}, true
}

func (s *onceSteering) outcome() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolved
}

// Payload rules apply exactly once to the frame actually sent, after
// chaining: a rule on a chained continuation sees the delta frame, and the
// next turn's chain baseline is the pre-rule body.
func TestWebsocketPayloadRulesApplyToEachFinalFrame(t *testing.T) {
	raw, err := os.ReadFile("../runtime/executor/testdata/codex_npi_ws_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture websocketCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(capture.Discovery) }))
	defer discovery.Close()
	catalog, err := codexauth.FetchModels(context.Background(), discovery.Client(), codexauth.ModelDiscoveryOptions{AccessToken: capture.Token, AccountID: capture.AccountID, BaseURL: discovery.URL, ClientVersion: ClientVersion})
	if err != nil {
		t.Fatal(err)
	}
	model := *catalog.Models[0].ModelInfo().Codex
	var mu sync.Mutex
	var frames []string
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			_, message, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			mu.Lock()
			frames = append(frames, string(message))
			id := fmt.Sprintf("resp_%d", len(frames))
			mu.Unlock()
			for _, event := range captureEvents(id) {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(event))
			}
		}
	}))
	defer server.Close()
	dial := func(ctx context.Context, url string, header http.Header) (*websocket.Conn, *http.Response, error) {
		return (&websocket.Dialer{}).DialContext(ctx, url, header)
	}
	sessions := NewSessions()
	calls := 0
	finalize := func(body []byte) []byte {
		calls++
		out, _ := sjson.SetBytes(body, "metadata_rule", gjson.GetBytes(body, "previous_response_id").String())
		return out
	}
	for _, turn := range capture.Turns[:2] {
		ctx, opts := FromResponses(turn.Downstream, &model)
		opts.SessionID = capture.SessionID
		request, errShape := sessions.Shape(&model, ctx, opts, Call{AccessToken: capture.Token, BaseURL: server.URL, InstallID: capture.InstallID, Transport: TransportWebsocket})
		if errShape != nil {
			t.Fatal(errShape)
		}
		runner := &Turn{Sessions: sessions, Request: request, Model: &model, Dial: dial, Do: http.DefaultClient.Do, Finalize: finalize}
		if _, errRun := runner.Run(context.Background(), func([]byte) error { return nil }); errRun != nil {
			t.Fatal(errRun)
		}
	}
	if calls != 2 {
		t.Fatalf("payload rules ran %d times for 2 frames", calls)
	}
	if got := gjson.Get(frames[0], "metadata_rule").String(); got != "" {
		t.Errorf("first frame rule saw previous_response_id %q", got)
	}
	if got := gjson.Get(frames[1], "metadata_rule").String(); got != "resp_1" {
		t.Errorf("chained frame rule saw previous_response_id %q, want the final chained frame", got)
	}
	if !gjson.Get(frames[1], "previous_response_id").Exists() {
		t.Error("a payload rule on the first frame broke the chain baseline")
	}
}
