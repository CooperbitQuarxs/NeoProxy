package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// neoPiStreamCaptureFile is testdata/neopi_completions_stream.json: NeoPi's OWN
// streamSimple run (capture_neopi_completions_stream.ts) over scripted SSE
// bodies, with the AssistantMessage it built from each.
type neoPiStreamCaptureFile struct {
	Scenarios []struct {
		Name    string            `json:"name"`
		Body    string            `json:"body"`
		Compat  map[string]bool   `json:"compat"`
		Headers map[string]string `json:"headers"`
		NeoPi   struct {
			StopReason   string  `json:"stopReason"`
			ErrorMessage *string `json:"errorMessage"`
			ErrorStatus  *int    `json:"errorStatus"`
			Content      []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Thinking  string          `json:"thinking"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"content"`
			Usage *struct {
				Input           float64 `json:"input"`
				Output          float64 `json:"output"`
				CacheRead       float64 `json:"cacheRead"`
				CacheWrite      float64 `json:"cacheWrite"`
				TotalTokens     float64 `json:"totalTokens"`
				ReasoningTokens float64 `json:"reasoningTokens"`
			} `json:"usage"`
		} `json:"neopi"`
	} `json:"scenarios"`
}

// neoPiFoldedStream is a normalized stream read back the way a client reads it.
type neoPiFoldedStream struct {
	lines    []string
	text     strings.Builder
	thinking strings.Builder
	calls    map[int]*neoPiFoldedCall
	order    []int
	finish   string
	finishes int
	usage    gjson.Result
	done     bool
	// firstRole reports that the first delta chunk carried the assistant role.
	firstRole bool
}

type neoPiFoldedCall struct {
	id, name  string
	arguments strings.Builder
	// startedWithIdentity reports that the first entry for the call carried id and name.
	startedWithIdentity bool
}

func neoPiFoldStream(t *testing.T, lines []string) *neoPiFoldedStream {
	t.Helper()
	folded := &neoPiFoldedStream{lines: lines, calls: map[int]*neoPiFoldedCall{}}
	sawDelta := false
	for _, line := range lines {
		if line == "data: [DONE]" {
			folded.done = true
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("line without data prefix: %q", line)
		}
		if folded.done {
			t.Fatalf("chunk after [DONE]: %q", line)
		}
		chunk := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if !chunk.IsObject() || chunk.Get("object").String() != "chat.completion.chunk" || chunk.Get("id").String() == "" {
			t.Fatalf("malformed chunk: %q", line)
		}
		if usage := chunk.Get("usage"); usage.Exists() {
			folded.usage = usage
			if chunk.Get("choices.#").Int() != 0 {
				t.Fatalf("usage chunk carries choices: %q", line)
			}
			continue
		}
		if reason := chunk.Get("choices.0.finish_reason"); reason.Type == gjson.String {
			folded.finish = reason.String()
			folded.finishes++
			continue
		}
		delta := chunk.Get("choices.0.delta")
		if !sawDelta {
			sawDelta = true
			folded.firstRole = delta.Get("role").String() == "assistant"
		}
		folded.text.WriteString(delta.Get("content").String())
		folded.thinking.WriteString(delta.Get("reasoning_content").String())
		delta.Get("tool_calls").ForEach(func(_, entry gjson.Result) bool {
			index := int(entry.Get("index").Int())
			call := folded.calls[index]
			if call == nil {
				call = &neoPiFoldedCall{id: entry.Get("id").String(), name: entry.Get("function.name").String()}
				call.startedWithIdentity = call.id != "" && entry.Get("type").String() == "function"
				folded.calls[index] = call
				folded.order = append(folded.order, index)
			}
			call.arguments.WriteString(entry.Get("function.arguments").String())
			return true
		})
	}
	return folded
}

func neoPiRunNormalizer(t *testing.T, settings neoPiCompletionsStreamSettings, body string) (*neoPiFoldedStream, error) {
	t.Helper()
	var lines []string
	err := neoPiNormalizeCompletionsStream(context.Background(), nil, settings, strings.NewReader(body), func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	return neoPiFoldStream(t, lines), err
}

func neoPiTestSettings(compat *registry.NeoPiCompletionsCompat) neoPiCompletionsStreamSettings {
	return neoPiCompletionsStreamSettingsFor("test", compat, nil)
}

func neoPiCanonicalJSON(t *testing.T, raw string) string {
	t.Helper()
	if strings.TrimSpace(raw) == "" || !gjson.Valid(raw) {
		t.Fatalf("not JSON: %q", raw)
	}
	return string(jsjson.Normalize([]byte(strings.TrimSpace(raw))))
}

// TestNeoPiCompletionsStreamParity replays NeoPi's scripted responses through
// the normalizer and checks what a client reads against what NeoPi built.
func TestNeoPiCompletionsStreamParity(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_completions_stream.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture neoPiStreamCaptureFile
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Scenarios) == 0 {
		t.Fatal("capture has no scenarios")
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			compat := &registry.NeoPiCompletionsCompat{
				StripDeepseekSpecialTokens:      scenario.Compat["stripDeepseekSpecialTokens"],
				ReasoningDeltasMayBeCumulative:  scenario.Compat["reasoningDeltasMayBeCumulative"],
				EmptyLengthFinishIsContextError: scenario.Compat["emptyLengthFinishIsContextError"],
				MistralReasoningContentParts:    scenario.Compat["mistralReasoningContentParts"],
			}
			header := http.Header{}
			for key, value := range scenario.Headers {
				header.Set(key, value)
			}
			settings := neoPiCompletionsStreamSettingsFor("test", compat, header)
			folded, err := neoPiRunNormalizer(t, settings, scenario.Body)
			expected := scenario.NeoPi

			var neoPiText, neoPiThinking strings.Builder
			for _, block := range expected.Content {
				switch block.Type {
				case "text":
					neoPiText.WriteString(block.Text)
				case "thinking":
					neoPiThinking.WriteString(block.Thinking)
				}
			}
			// A repetition stop discards the partial output in NeoPi; the stream
			// has already delivered it by then.
			discarded := strings.HasPrefix(scenario.Name, "repetition-")
			if !discarded {
				if folded.text.String() != neoPiText.String() {
					t.Errorf("text = %q, NeoPi %q", folded.text.String(), neoPiText.String())
				}
				if folded.thinking.String() != neoPiThinking.String() {
					t.Errorf("thinking = %q, NeoPi %q", folded.thinking.String(), neoPiThinking.String())
				}
			}

			if expected.StopReason == "error" {
				if err == nil {
					t.Fatalf("stream succeeded, NeoPi failed with %q", *expected.ErrorMessage)
				}
				wantStatus := http.StatusBadGateway
				if expected.ErrorStatus != nil {
					wantStatus = *expected.ErrorStatus
				}
				var coded interface{ StatusCode() int }
				if !errorsAs(err, &coded) || coded.StatusCode() != wantStatus {
					t.Errorf("error %v, status want %d", err, wantStatus)
				}
				// The JSON parser's own wording is a JavaScript engine detail.
				if scenario.Name != "invalid-json-frame" && !strings.HasSuffix(err.Error(), *expected.ErrorMessage) {
					t.Errorf("error %q, NeoPi %q", err.Error(), *expected.ErrorMessage)
				}
				if folded.finishes != 0 || folded.done {
					t.Errorf("failed stream still finished: finishes=%d done=%v", folded.finishes, folded.done)
				}
				return
			}
			if err != nil {
				t.Fatalf("stream failed: %v (NeoPi stopReason %s)", err, expected.StopReason)
			}
			if !folded.done || folded.finishes != 1 {
				t.Fatalf("finishes=%d done=%v", folded.finishes, folded.done)
			}
			wantFinish := map[string]string{"stop": "stop", "length": "length", "toolUse": "tool_calls"}[expected.StopReason]
			if folded.finish != wantFinish {
				t.Errorf("finish_reason = %q, want %q", folded.finish, wantFinish)
			}

			var wantCalls []int
			for index, block := range expected.Content {
				if block.Type == "toolCall" {
					wantCalls = append(wantCalls, index)
				}
			}
			if len(folded.order) != len(wantCalls) {
				t.Fatalf("%d tool calls, NeoPi %d", len(folded.order), len(wantCalls))
			}
			for position, contentIndex := range wantCalls {
				want := expected.Content[contentIndex]
				got := folded.calls[folded.order[position]]
				if folded.order[position] != position {
					t.Errorf("call %d has index %d", position, folded.order[position])
				}
				if got.name != want.Name {
					t.Errorf("call %d name = %q, NeoPi %q", position, got.name, want.Name)
				}
				switch {
				case scenario.Name == "tool-fresh-id-per-fragment":
					// NeoPi renames the block to each fragment's id; a stream
					// cannot retract the id it announced first.
				case want.ID == "":
					if !strings.HasPrefix(got.id, "call_") || len(got.id) != len("call_")+24 {
						t.Errorf("call %d id = %q, want a minted id for NeoPi's empty one", position, got.id)
					}
				case got.id != want.ID:
					t.Errorf("call %d id = %q, NeoPi %q", position, got.id, want.ID)
				}
				if !got.startedWithIdentity || got.name == "" {
					t.Errorf("call %d opened without id and name", position)
				}
				if got, want := neoPiCanonicalJSON(t, got.arguments.String()), neoPiCanonicalJSON(t, string(want.Arguments)); got != want {
					t.Errorf("call %d arguments = %s, NeoPi %s", position, got, want)
				}
			}

			var got struct{ input, output, cacheRead, cacheWrite, total, reasoning float64 }
			if folded.usage.Exists() {
				got.cacheRead = folded.usage.Get("prompt_tokens_details.cached_tokens").Float()
				got.cacheWrite = folded.usage.Get("prompt_tokens_details.cache_write_tokens").Float()
				got.input = folded.usage.Get("prompt_tokens").Float() - got.cacheRead - got.cacheWrite
				got.output = folded.usage.Get("completion_tokens").Float()
				got.total = folded.usage.Get("total_tokens").Float()
				got.reasoning = folded.usage.Get("completion_tokens_details.reasoning_tokens").Float()
			}
			want := struct{ input, output, cacheRead, cacheWrite, total, reasoning float64 }{}
			if expected.Usage != nil {
				want.input, want.output, want.cacheRead = expected.Usage.Input, expected.Usage.Output, expected.Usage.CacheRead
				want.cacheWrite, want.total, want.reasoning = expected.Usage.CacheWrite, expected.Usage.TotalTokens, expected.Usage.ReasoningTokens
			}
			if got != want {
				t.Errorf("usage = %+v, NeoPi %+v", got, want)
			}
		})
	}
	t.Logf("%d scripted responses replayed", len(capture.Scenarios))
}

func errorsAs(err error, target *interface{ StatusCode() int }) bool {
	for err != nil {
		if coded, ok := err.(interface{ StatusCode() int }); ok {
			*target = coded
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

func TestNeoPiCompletionsStreamShape(t *testing.T) {
	body := `data: {"id":"c1","created":5,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning":"think"}}]}` + "\n\n" +
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n" +
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":1}}}` + "\n\n" +
		"data: [DONE]\n\n"
	folded, err := neoPiRunNormalizer(t, neoPiTestSettings(nil), body)
	if err != nil {
		t.Fatal(err)
	}
	if !folded.firstRole {
		t.Error("first delta chunk has no assistant role")
	}
	if len(folded.lines) != 5 || folded.lines[len(folded.lines)-1] != "data: [DONE]" {
		t.Fatalf("lines = %q", folded.lines)
	}
	if gjson.Get(strings.TrimPrefix(folded.lines[0], "data: "), "choices.0.delta.reasoning_content").String() != "think" {
		t.Errorf("reasoning not relayed as reasoning_content: %s", folded.lines[0])
	}
	if first := gjson.Parse(strings.TrimPrefix(folded.lines[0], "data: ")); first.Get("id").String() != "c1" || first.Get("model").String() != "m" || first.Get("created").Int() != 5 {
		t.Errorf("identity not carried: %s", folded.lines[0])
	}
	// prompt_tokens covers cache reads: 3 uncached + 1 cached.
	if got := folded.usage.Get("prompt_tokens").Int(); got != 4 || folded.usage.Get("total_tokens").Int() != 6 {
		t.Errorf("usage = %s", folded.usage.Raw)
	}
}

// A call announced with its name before its id, and arguments before both,
// reaches the client as one opening entry carrying everything known.
func TestNeoPiCompletionsStreamDefersToolCallOpening(t *testing.T) {
	body := `data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}` + "\n\n" +
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"f"}}]}}]}` + "\n\n" +
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"arguments":"1}"}}]}}]}` + "\n\n" +
		`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	folded, err := neoPiRunNormalizer(t, neoPiTestSettings(nil), body)
	if err != nil {
		t.Fatal(err)
	}
	call := folded.calls[0]
	if call == nil || call.id != "call_9" || call.name != "f" || !call.startedWithIdentity || call.arguments.String() != `{"a":1}` {
		t.Fatalf("call = %+v", call)
	}
	opening := 0
	for _, line := range folded.lines {
		if gjson.Get(line[len("data: "):], "choices.0.delta.tool_calls.0.id").Exists() {
			opening++
		}
	}
	if len(folded.lines) > 2 && opening != 1 {
		t.Errorf("%d opening entries", opening)
	}
}

// Failures keep the usage NeoPi had parsed, for the caller's usage buffer.
func TestNeoPiCompletionsStreamFailureCarriesUsage(t *testing.T) {
	body := `data: {"id":"c","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":2}}}` + "\n\n" +
		"data: [DONE]\n\n"
	_, err := neoPiRunNormalizer(t, neoPiTestSettings(nil), body)
	if err == nil {
		t.Fatal("content_filter finish succeeded")
	}
	usage := neoPiCompletionsFailureUsage(err)
	if usage == nil {
		t.Fatal("failure lost its usage")
	}
	parsed := gjson.Parse(strings.TrimPrefix(string(usage), "data: "))
	if parsed.Get("usage.prompt_tokens").Int() != 9 || parsed.Get("usage.prompt_tokens_details.cached_tokens").Int() != 2 || parsed.Get("choices.#").Int() != 0 {
		t.Errorf("usage chunk = %s", usage)
	}
}

func TestNeoPiCompletionsStreamFoldsIntoAggregate(t *testing.T) {
	body := `data: {"id":"c","created":1,"model":"m","choices":[{"index":0,"delta":{"reasoning_text":"why"}}]}` + "\n\n" +
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"f","arguments":""}}]}}]}` + "\n\n" +
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}` + "\n\n" +
		`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"prompt_tokens_details":{"cached_tokens":1}}}` + "\n\n"
	var lines [][]byte
	err := neoPiNormalizeCompletionsStream(context.Background(), nil, neoPiTestSettings(nil), strings.NewReader(body), func(line []byte) error {
		lines = append(lines, append([]byte(nil), line...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	message := gjson.ParseBytes(neoPiAggregateCompletionStream(lines))
	if message.Get("choices.0.message.reasoning_content").String() != "why" ||
		message.Get("choices.0.message.tool_calls.0.id").String() != "call_1" ||
		message.Get("choices.0.message.tool_calls.0.function.name").String() != "f" ||
		message.Get("choices.0.finish_reason").String() != "tool_calls" ||
		message.Get("usage.total_tokens").Int() != 2 {
		t.Fatalf("aggregate = %s", message.Raw)
	}
}

func TestNeoPiCompletionsStreamSettings(t *testing.T) {
	zero := 0
	custom := 45_000
	if got := neoPiCompletionsStreamSettingsFor("p", nil, nil); got.idleTimeout != 300*time.Second || got.firstEventTimeout != 300*time.Second {
		t.Errorf("defaults = %+v", got)
	}
	if got := neoPiCompletionsStreamSettingsFor("p", &registry.NeoPiCompletionsCompat{StreamFirstEventTimeoutMs: &zero}, nil); got.firstEventTimeout != 0 || got.idleTimeout != 300*time.Second {
		t.Errorf("disabled first event = %+v", got)
	}
	// The idle budget floors the first-event budget.
	if got := neoPiCompletionsStreamSettingsFor("p", &registry.NeoPiCompletionsCompat{StreamIdleTimeoutMs: 600_000, StreamFirstEventTimeoutMs: &custom}, nil); got.firstEventTimeout != 600*time.Second || got.idleTimeout != 600*time.Second {
		t.Errorf("floored first event = %+v", got)
	}
	header := http.Header{}
	header.Set("Fireworks-Cached-Prompt-Tokens", "12")
	if got := neoPiCompletionsStreamSettingsFor("p", nil, header); got.cachedPromptTokensHeader != 12 {
		t.Errorf("cached header = %v", got.cachedPromptTokensHeader)
	}
}

func neoPiPipeFrame(content string) []byte {
	return []byte(`data: {"id":"c","choices":[{"index":0,"delta":{"content":` + string(jsjson.Marshal(content)) + `}}]}` + "\n\n")
}

// A host that keeps the connection open after its terminal chunk is released
// when NeoPi's grace window ends, cleanly.
func TestNeoPiCompletionsStreamGraceEndsOpenBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	settings := neoPiTestSettings(nil)
	settings.postFinishGrace = 60 * time.Millisecond
	go func() {
		_, _ = writer.Write(append(neoPiPipeFrame("hi"), []byte(`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")...))
	}()
	var lines []string
	start := time.Now()
	err := neoPiNormalizeCompletionsStream(context.Background(), nil, settings, reader, func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("returned after %v", elapsed)
	}
	if folded := neoPiFoldStream(t, lines); folded.finish != "stop" || !folded.done || folded.text.String() != "hi" {
		t.Errorf("lines = %q", lines)
	}
}

// A trailing usage-only chunk completes the response without waiting for
// [DONE] or the connection to close.
func TestNeoPiCompletionsStreamUsageChunkEndsOpenBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	settings := neoPiTestSettings(nil)
	settings.postFinishGrace = time.Hour
	go func() {
		_, _ = writer.Write(append(neoPiPipeFrame("hi"), []byte(
			`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n"+
				`data: {"id":"c","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4,"prompt_tokens_details":{"cached_tokens":1}}}`+"\n\n")...))
	}()
	done := make(chan []string, 1)
	go func() {
		var lines []string
		_ = neoPiNormalizeCompletionsStream(context.Background(), nil, settings, reader, func(line []byte) error {
			lines = append(lines, string(line))
			return nil
		})
		done <- lines
	}()
	select {
	case lines := <-done:
		if folded := neoPiFoldStream(t, lines); folded.usage.Get("total_tokens").Int() != 4 || !folded.done {
			t.Errorf("lines = %q", lines)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream waited for the host to close")
	}
}

func TestNeoPiCompletionsStreamFirstEventTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	settings := neoPiTestSettings(nil)
	settings.firstEventTimeout = 40 * time.Millisecond
	// Role-only preambles are not progress: the first-event budget keeps running.
	go func() {
		for {
			if _, err := writer.Write([]byte(`data: {"id":"c","choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n")); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	err := neoPiNormalizeCompletionsStream(context.Background(), nil, settings, reader, func([]byte) error { return nil })
	var coded interface{ StatusCode() int }
	if err == nil || !errorsAs(err, &coded) || coded.StatusCode() != http.StatusGatewayTimeout || !strings.HasSuffix(err.Error(), neoPiCompletionsFirstEventMessage) {
		t.Fatalf("error = %v", err)
	}
}

func TestNeoPiCompletionsStreamIdleTimeout(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	settings := neoPiTestSettings(nil)
	settings.idleTimeout = 40 * time.Millisecond
	go func() { _, _ = writer.Write(neoPiPipeFrame("hi")) }()
	err := neoPiNormalizeCompletionsStream(context.Background(), nil, settings, reader, func([]byte) error { return nil })
	if err == nil || !strings.HasSuffix(err.Error(), neoPiCompletionsIdleMessage) {
		t.Fatalf("error = %v", err)
	}
}

func TestNeoPiCompletionsStreamHonorsCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	err := neoPiNormalizeCompletionsStream(ctx, nil, neoPiTestSettings(nil), reader, func([]byte) error { return nil })
	if err != context.Canceled {
		t.Fatalf("error = %v", err)
	}
}

func TestNeoPiCompletionsStreamBareCarriageReturnLines(t *testing.T) {
	body := "data: " + `{"id":"c","choices":[{"index":0,"delta":{"content":"a"}}]}` + "\r\r" +
		"data: " + `{"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\r\r" +
		"data: [DONE]\r\r"
	folded, err := neoPiRunNormalizer(t, neoPiTestSettings(nil), body)
	if err != nil || folded.text.String() != "a" || folded.finish != "stop" {
		t.Fatalf("err = %v, folded = %+v", err, folded)
	}
}
