package cursorwire_test

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicatalog"
	"github.com/tidwall/gjson"
)

// The fixture is NeoPi's own Cursor provider driven the way its auth gateway
// drives it (testdata/capture_cursor_npi_runtime.ts): an OpenAI chat body,
// a scripted Cursor server, and every frame the client wrote.
type runtimeFixture struct {
	Cwd       string            `json:"cwd"`
	Scenarios []runtimeScenario `json:"scenarios"`
}

type runtimeScenario struct {
	Name            string          `json:"name"`
	Body            json.RawMessage `json:"body"`
	ConversationKey string          `json:"conversationKey"`
	UUIDStart       int             `json:"uuidStart"`
	WireModelID     string          `json:"wireModelId"`
	Model           struct {
		ID                                 string                              `json:"id"`
		Name                               string                              `json:"name"`
		RequestModelID                     string                              `json:"requestModelId"`
		CursorMaxMode                      *bool                               `json:"cursorMaxMode"`
		CursorMaxModeRoutes                map[string]bool                     `json:"cursorMaxModeRoutes"`
		CursorModelRoutes                  map[string]neopicatalog.CursorRoute `json:"cursorModelRoutes"`
		CursorModelParameters              []neopicatalog.ModelParameter       `json:"cursorModelParameters"`
		EffortRouting                      map[string]string                   `json:"effortRouting"`
		RequiresCursorToolSchemaProjection bool                                `json:"requiresCursorToolSchemaProjection"`
	} `json:"model"`
	Exchanges [][]struct {
		Send  []string        `json:"send"`
		Await *int            `json:"await"`
		End   json.RawMessage `json:"end"`
	} `json:"exchanges"`
	Requests []struct {
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Frames  []struct {
			Flags   byte   `json:"flags"`
			Payload string `json:"payload"`
		} `json:"frames"`
	} `json:"requests"`
	Events []map[string]any `json:"events"`
	Final  map[string]any   `json:"final"`
}

type recordedRequest struct {
	path    string
	headers http.Header
	mu      sync.Mutex
	frames  []string
	notify  chan struct{}
}

func (r *recordedRequest) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.frames)
}

type eventSink struct{ events []map[string]any }

func (s *eventSink) TextDelta(delta string) {
	s.events = append(s.events, map[string]any{"type": "text_delta", "delta": delta})
}

func (s *eventSink) ThinkingDelta(delta string) {
	s.events = append(s.events, map[string]any{"type": "thinking_delta", "delta": delta})
}

func (s *eventSink) ToolCallStart(block *cursorwire.OutBlock) {
	s.events = append(s.events, map[string]any{"type": "toolcall_start", "id": block.ID, "name": block.Name, "resolved": block.Resolved})
}

func (s *eventSink) ToolCallDelta(_ *cursorwire.OutBlock, delta string) {
	s.events = append(s.events, map[string]any{"type": "toolcall_delta", "delta": delta})
}

func (s *eventSink) ToolCallEnd(block *cursorwire.OutBlock) {
	event := map[string]any{"type": "toolcall_end", "id": block.ID, "name": block.Name, "resolved": block.Resolved}
	if !block.Resolved {
		event["arguments"] = cursorwire.StringifyJSON(block.Arguments)
	}
	s.events = append(s.events, event)
}

func loadRuntimeFixture(t *testing.T) runtimeFixture {
	t.Helper()
	file, err := os.Open("testdata/cursor_npi_runtime.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var fixture runtimeFixture
	if err := json.NewDecoder(reader).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func readClientFrames(body io.Reader, record *recordedRequest) {
	reader := bufio.NewReader(body)
	heartbeat := base64.StdEncoding.EncodeToString(cursorwire.HeartbeatFrame()[5:])
	for {
		var header [5]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return
		}
		payload := make([]byte, binary.BigEndian.Uint32(header[1:5]))
		if _, err := io.ReadFull(reader, payload); err != nil {
			return
		}
		encoded := base64.StdEncoding.EncodeToString(payload)
		if encoded == heartbeat {
			continue
		}
		record.mu.Lock()
		record.frames = append(record.frames, encoded)
		record.mu.Unlock()
		select {
		case record.notify <- struct{}{}:
		default:
		}
	}
}

// TestCursorRuntimeMatchesNeoPi replays NeoPi's scripted Cursor exchanges and
// requires the identical client frames, request identity, events and outcome.
func TestCursorRuntimeMatchesNeoPi(t *testing.T) {
	fixture := loadRuntimeFixture(t)
	t.Chdir(fixture.Cwd)

	var mu sync.Mutex
	var pending []func(http.ResponseWriter, *http.Request)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if len(pending) == 0 {
			mu.Unlock()
			http.Error(w, "unexpected request", http.StatusTeapot)
			return
		}
		handler := pending[0]
		pending = pending[1:]
		mu.Unlock()
		handler(w, r)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	client := server.Client()

	conversations := cursorwire.NewConversations()
	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var records []*recordedRequest
			for _, exchange := range scenario.Exchanges {
				exchange := exchange
				record := &recordedRequest{notify: make(chan struct{}, 1)}
				records = append(records, record)
				mu.Lock()
				pending = append(pending, func(w http.ResponseWriter, r *http.Request) {
					record.path = r.URL.Path
					record.headers = r.Header.Clone()
					go readClientFrames(r.Body, record)
					w.Header().Set("Content-Type", "application/connect+proto")
					w.WriteHeader(http.StatusOK)
					flusher := w.(http.Flusher)
					flusher.Flush()
					for _, step := range exchange {
						switch {
						case step.Send != nil:
							for _, frame := range step.Send {
								data, _ := base64.StdEncoding.DecodeString(frame)
								_, _ = w.Write(data)
							}
							flusher.Flush()
						case step.Await != nil:
							deadline := time.After(10 * time.Second)
							for record.count() < *step.Await+1 {
								select {
								case <-record.notify:
								case <-deadline:
									t.Errorf("timed out awaiting %d client frames, have %d", *step.Await+1, record.count())
									return
								}
							}
							time.Sleep(20 * time.Millisecond)
						default:
							// Ending the handler closes the request body; the run
							// request itself must have been read first.
							deadline := time.After(10 * time.Second)
							for record.count() < 1 {
								select {
								case <-record.notify:
								case <-deadline:
									t.Errorf("timed out awaiting the run request")
									return
								}
							}
							if string(step.End) != "null" {
								var encoded string
								_ = json.Unmarshal(step.End, &encoded)
								payload, _ := base64.StdEncoding.DecodeString(encoded)
								_, _ = w.Write(cursorwire.Frame(payload, cursorwire.FlagEndStream))
								flusher.Flush()
							}
							return
						}
					}
				})
				mu.Unlock()
			}

			parsed, err := cursorwire.ParseOpenAIChat(scenario.Body, nil)
			if err != nil {
				t.Fatal(err)
			}
			key := parsed.PromptCacheKey
			if key == "" {
				key = cursorwire.DeriveSessionID(parsed.ModelID, &parsed.Context)
			}
			if key != scenario.ConversationKey {
				t.Fatalf("conversation key = %q, NeoPi %q", key, scenario.ConversationKey)
			}
			m := scenario.Model
			model := &cursorwire.Model{ID: m.ID, Name: m.Name, Provider: "cursor", BaseURL: server.URL, RequestModelID: m.RequestModelID,
				MaxMode: m.CursorMaxMode, MaxModeRoutes: m.CursorMaxModeRoutes, ModelRoutes: m.CursorModelRoutes,
				ModelParameters: m.CursorModelParameters, EffortRouting: m.EffortRouting, RequiresToolSchemaProjection: m.RequiresCursorToolSchemaProjection}
			counter := scenario.UUIDStart
			sink := &eventSink{events: []map[string]any{}}
			turn, runErr := conversations.Run(context.Background(), &cursorwire.RunInput{
				Model: model, Context: &parsed.Context, WireModelID: scenario.WireModelID, ConversationKey: key,
				APIKey: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYXB0dXJlIn0.c2lnbmF0dXJl", Do: client.Do, Sink: sink,
				NewUUID: func() string {
					counter++
					return fmt.Sprintf("00000000-0000-4000-8000-%012d", counter)
				},
				ToolSchema: func(tool cursorwire.Tool) any {
					schema := codexwire.ToolWireSchema(gjson.Parse(cursorwire.StringifyJSON(tool.Parameters)))
					if model.RequiresToolSchemaProjection {
						schema = codexwire.SanitizeSchemaForCursor(schema)
					}
					value, _ := cursorwire.ParseJSON(string(schema))
					return value
				},
				Wait: func(context.Context, time.Duration) error { return nil },
			})

			if len(records) != len(scenario.Requests) {
				t.Fatalf("requests = %d, NeoPi %d", len(records), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				got := records[i]
				if got.path != want.Path {
					t.Errorf("request %d path = %q, NeoPi %q", i, got.path, want.Path)
				}
				for _, name := range []string{"content-type", "authorization", "connect-protocol-version", "x-cursor-client-type",
					"x-cursor-client-version", "x-ghost-mode", "x-request-id", "x-original-request-id", "te"} {
					if g, w := got.headers.Get(name), want.Headers[name]; g != w {
						t.Errorf("request %d header %s = %q, NeoPi %q", i, name, g, w)
					}
				}
				got.mu.Lock()
				frames := append([]string(nil), got.frames...)
				got.mu.Unlock()
				if len(frames) != len(want.Frames) {
					t.Errorf("request %d client frames = %d, NeoPi %d", i, len(frames), len(want.Frames))
				}
				for j := 0; j < len(frames) && j < len(want.Frames); j++ {
					if frames[j] != want.Frames[j].Payload {
						t.Errorf("request %d frame %d differs\n go:    %s\n neopi: %s", i, j, frames[j], want.Frames[j].Payload)
					}
				}
			}

			gotEvents, _ := json.Marshal(sink.events)
			wantEvents := normalizeEvents(scenario.Events)
			if string(gotEvents) != wantEvents {
				t.Errorf("events differ\n go:    %s\n neopi: %s", gotEvents, wantEvents)
			}

			if scenario.Final["stopReason"] == "error" {
				var failure *cursorwire.RunError
				if !errors.As(runErr, &failure) {
					t.Fatalf("Run error = %v, NeoPi failed with %v", runErr, scenario.Final["errorMessage"])
				}
				if failure.Error() != scenario.Final["errorMessage"] {
					t.Errorf("error = %q, NeoPi %q", failure.Error(), scenario.Final["errorMessage"])
				}
				if status, ok := scenario.Final["errorStatus"].(float64); ok && failure.HTTPStatus() != int(status) {
					t.Errorf("error status = %d, NeoPi %v", failure.HTTPStatus(), status)
				}
				return
			}
			if runErr != nil {
				t.Fatalf("Run error = %v, NeoPi completed", runErr)
			}
			usage := turn.Output.Usage
			wantUsage, _ := scenario.Final["usage"].(map[string]any)
			number := func(key string) int64 {
				value, _ := wantUsage[key].(float64)
				return int64(value)
			}
			reasoning := int64(0)
			if usage.Reasoning != nil {
				reasoning = *usage.Reasoning
			}
			got := []int64{usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Total, reasoning, usage.ContextTokens}
			want := []int64{number("input"), number("output"), number("cacheRead"), number("cacheWrite"), number("totalTokens"), number("reasoningTokens"), number("contextTokens")}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("usage [input output cacheRead cacheWrite total reasoning context] = %v, NeoPi %v", got, want)
			}
			if turn.Output.StopReason != scenario.Final["stopReason"] {
				t.Errorf("stop reason = %q, NeoPi %v", turn.Output.StopReason, scenario.Final["stopReason"])
			}
		})
	}
}

// normalizeEvents drops what the Go sink does not report: arguments of
// resolved calls (Cursor ran them; they never reach a client).
func normalizeEvents(events []map[string]any) string {
	for _, event := range events {
		if event["type"] == "toolcall_end" && event["resolved"] == true {
			delete(event, "arguments")
		}
	}
	data, _ := json.Marshal(events)
	return strings.TrimSpace(string(data))
}
