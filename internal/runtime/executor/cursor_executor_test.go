package executor

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func readCursorGzipJSON(t *testing.T, path string, target any) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(reader).Decode(target); err != nil {
		t.Fatal(err)
	}
}

var cursorUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestFetchCursorModelsMatchesNeoPiDiscovery sends NeoPi's three discovery
// RPCs (bodies and identity from capture_cursor_npi_discovery.ts) and lists
// what NeoPi lists when any of them fails.
func TestFetchCursorModelsMatchesNeoPiDiscovery(t *testing.T) {
	var bodies map[string]string
	readCursorGzipJSON(t, "../../registry/testdata/cursor_discovery_bodies.json.gz", &bodies)
	var capture struct {
		Requests []struct {
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"requests"`
		Scenarios []struct {
			Name       string   `json:"name"`
			Fail       []string `json:"fail"`
			Discovered []string `json:"discovered"`
		} `json:"scenarios"`
	}
	readCursorGzipJSON(t, "../../registry/testdata/cursor_npi_discovery.json.gz", &capture)
	wantRequests := map[string]int{}
	for i, request := range capture.Requests {
		wantRequests[request.Path] = i
	}

	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var mu sync.Mutex
			seen := map[string]*http.Request{}
			seenBodies := map[string][]byte{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				seen[r.URL.Path], seenBodies[r.URL.Path] = r, body
				mu.Unlock()
				name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				if slices.Contains(scenario.Fail, name) {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				data, _ := base64.StdEncoding.DecodeString(bodies[name])
				_, _ = w.Write(data)
			}))
			defer server.Close()
			auth := &cliproxyauth.Auth{ID: "cursor-test", Provider: "cursor", Metadata: map[string]any{
				"access_token": capture.Requests[0].Headers["authorization"][len("Bearer "):], "base_url": server.URL}}

			models, err := FetchCursorModels(context.Background(), nil, auth)
			if scenario.Discovered == nil {
				if err == nil {
					t.Fatalf("FetchCursorModels succeeded; NeoPi publishes nothing when both model lists fail")
				}
			} else if err != nil {
				t.Fatalf("FetchCursorModels: %v", err)
			}

			want, _ := registry.CursorDiscoveredModels(registry.CursorDiscoveryResponses{
				Usable: discoveryBody(t, bodies, scenario.Fail, "GetUsableModels"), Available: discoveryBody(t, bodies, scenario.Fail, "AvailableModels"),
				Default: discoveryBody(t, bodies, scenario.Fail, "GetDefaultModelForCli")}, server.URL, time.Now())
			var gotIDs, wantIDs []string
			for _, model := range models {
				gotIDs = append(gotIDs, model.ID)
			}
			for _, model := range want {
				wantIDs = append(wantIDs, model.ID)
			}
			if !slices.Equal(gotIDs, wantIDs) {
				t.Errorf("models:\n got %v\nwant %v", gotIDs, wantIDs)
			}

			for path, index := range wantRequests {
				wantRequest := capture.Requests[index]
				got := seen[path]
				if got == nil {
					t.Errorf("%s was not requested", path)
					continue
				}
				if wantBody, _ := base64.StdEncoding.DecodeString(wantRequest.Body); string(seenBodies[path]) != string(wantBody) {
					t.Errorf("%s body = %x, NeoPi %x", path, seenBodies[path], wantBody)
				}
				for name, value := range wantRequest.Headers {
					switch name {
					case "x-request-id":
						if !cursorUUIDPattern.MatchString(got.Header.Get(name)) {
							t.Errorf("%s x-request-id = %q, want a fresh uuid", path, got.Header.Get(name))
						}
					case "content-length", "host":
					default:
						if got.Header.Get(name) != value {
							t.Errorf("%s header %s = %q, NeoPi %q", path, name, got.Header.Get(name), value)
						}
					}
				}
			}
		})
	}
}

func discoveryBody(t *testing.T, bodies map[string]string, fail []string, name string) []byte {
	if slices.Contains(fail, name) {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(bodies[name])
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestCursorChunkEncoderHandsOnlyClientCallsToTheClient pins the streamed
// chat shape: calls Cursor ran itself never reach the client, client calls
// are numbered densely, and arguments that never streamed arrive at the end.
func TestCursorChunkEncoderHandsOnlyClientCallsToTheClient(t *testing.T) {
	var chunks []string
	encoder := newCursorChunkEncoder("cursor-model", func(chunk []byte) error {
		chunks = append(chunks, string(chunk))
		return nil
	})
	native := &cursorwire.OutBlock{Type: "toolCall", ID: "read-1", Name: "read", Resolved: true}
	streamed := &cursorwire.OutBlock{Type: "toolCall", ID: "tc_1", Name: "lookup"}
	handoff := &cursorwire.OutBlock{Type: "toolCall", ID: "tc_2", Name: "lookup"}
	args := &cursorwire.JSONObject{}
	args.Set("q", "z")
	handoff.Arguments = args

	encoder.ThinkingDelta("plan")
	encoder.TextDelta("Checking.")
	encoder.ToolCallStart(native)
	encoder.ToolCallEnd(native)
	encoder.ToolCallStart(streamed)
	encoder.ToolCallDelta(streamed, `{"q":"y"}`)
	encoder.ToolCallEnd(streamed)
	encoder.ToolCallStart(handoff)
	encoder.ToolCallEnd(handoff)
	reasoning := int64(3)
	if err := encoder.finish(&cursorwire.Output{StopReason: "stop", Usage: cursorwire.Usage{Input: 10, Output: 5, CacheRead: 2, Reasoning: &reasoning}}); err != nil {
		t.Fatal(err)
	}

	var deltas []string
	for _, chunk := range chunks[:len(chunks)-2] {
		deltas = append(deltas, gjson.Get(chunk, "choices.0.delta").Raw)
	}
	want := []string{
		`{"role":"assistant"}`,
		`{"reasoning_content":"plan"}`,
		`{"content":"Checking."}`,
		`{"tool_calls":[{"function":{"arguments":"","name":"lookup"},"id":"tc_1","index":0,"type":"function"}]}`,
		`{"tool_calls":[{"function":{"arguments":"{\"q\":\"y\"}"},"index":0}]}`,
		`{"tool_calls":[{"function":{"arguments":"","name":"lookup"},"id":"tc_2","index":1,"type":"function"}]}`,
		`{"tool_calls":[{"function":{"arguments":"{\"q\":\"z\"}"},"index":1}]}`,
		`{}`,
	}
	if !slices.Equal(deltas, want) {
		t.Fatalf("deltas:\n got %v\nwant %v", deltas, want)
	}
	if finish := gjson.Get(chunks[len(chunks)-3], "choices.0.finish_reason").String(); finish != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", finish)
	}
	if usage := gjson.Get(chunks[len(chunks)-2], "usage").Raw; usage != `{"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":3},"prompt_tokens":12,"prompt_tokens_details":{"cached_tokens":2},"total_tokens":17}` {
		t.Errorf("usage = %s", usage)
	}
	if chunks[len(chunks)-1] != "[DONE]" {
		t.Errorf("last chunk = %q, want [DONE]", chunks[len(chunks)-1])
	}
}

// TestCursorTurnWithOnlyServerCallsFinishesStop: a turn whose only tool
// calls Cursor ran itself ends on stop, not a handoff.
func TestCursorTurnWithOnlyServerCallsFinishesStop(t *testing.T) {
	var chunks []string
	encoder := newCursorChunkEncoder("m", func(chunk []byte) error {
		chunks = append(chunks, string(chunk))
		return nil
	})
	todo := &cursorwire.OutBlock{Type: "toolCall", ID: "todo-1", Name: "todo", Resolved: true}
	encoder.ToolCallStart(todo)
	encoder.ToolCallEnd(todo)
	encoder.TextDelta("done")
	if err := encoder.finish(&cursorwire.Output{StopReason: "stop"}); err != nil {
		t.Fatal(err)
	}
	if finish := gjson.Get(chunks[len(chunks)-3], "choices.0.finish_reason").String(); finish != "stop" {
		t.Errorf("finish_reason = %q, want stop", finish)
	}
	collector := &cursorCollector{}
	collector.ToolCallStart(todo)
	collector.TextDelta("done")
	completion := encodeCursorCompletion(&cursorwire.Output{StopReason: "stop"}, collector, "m")
	if gjson.GetBytes(completion, "choices.0.message.tool_calls").Exists() || gjson.GetBytes(completion, "choices.0.finish_reason").String() != "stop" {
		t.Errorf("completion = %s", completion)
	}
}
