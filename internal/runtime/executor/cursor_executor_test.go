package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestCursorFrameRoundTrip(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(cursorFrame([]byte("first")))
	stream.Write(cursorFrame(nil))
	end := cursorFrame([]byte(`{}`))
	end[0] = cursorFlagEndStream
	stream.Write(end)

	r := newCursorFrameReader(&stream)
	for i, want := range []struct {
		flags   byte
		payload string
	}{{0, "first"}, {0, ""}, {cursorFlagEndStream, "{}"}} {
		flags, payload, err := r.next()
		if err != nil || flags != want.flags || string(payload) != want.payload {
			t.Fatalf("frame %d = %x %q %v, want %x %q", i, flags, payload, err, want.flags, want.payload)
		}
	}
	if _, _, err := r.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("clean end = %v, want EOF", err)
	}

	cut := cursorFrame([]byte("truncated"))
	if _, _, err := newCursorFrameReader(bytes.NewReader(cut[:len(cut)-2])).next(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("cut frame = %v, want a stream error", err)
	}
}

func TestResolveCursorWireModel(t *testing.T) {
	for _, tc := range []struct {
		slug, model string
		params      [][2]string
	}{
		{"gpt-5.3-codex-high", "gpt-5.3-codex", [][2]string{{"reasoning", "high"}}},
		{"gpt-5.3-codex-xhigh-fast", "gpt-5.3-codex-fast", [][2]string{{"reasoning", "xhigh"}}},
		{"gpt-5.5-extra-high", "gpt-5.5", [][2]string{{"reasoning", "xhigh"}}},
		{"gpt-5.6-sol-none-fast", "gpt-5.6-sol-fast", nil},
		{"gpt-5.2", "gpt-5.2", nil},
		{"gpt-5-mini", "gpt-5-mini", nil},
		{"composer-2.5", "composer-2.5", [][2]string{{"fast", "false"}}},
		{"composer-2.5-fast", "composer-2.5-fast", nil},
		{"claude-opus-5-5-high", "claude-opus-5-5-high", nil},
		{"cursor-grok-4.6-low", "cursor-grok-4.6-low", nil},
	} {
		got := resolveCursorWireModel(tc.slug, false)
		if got.modelID != tc.model || got.detailsModelID != tc.slug || len(got.params) != len(tc.params) {
			t.Fatalf("%s = %+v, want model %s params %v", tc.slug, got, tc.model, tc.params)
		}
		for i := range tc.params {
			if got.params[i] != tc.params[i] {
				t.Fatalf("%s params = %v, want %v", tc.slug, got.params, tc.params)
			}
		}
	}
}

const cursorToolChat = `{"model":"cursor/composer-2.5","messages":[
	{"role":"system","content":"Be terse."},
	{"role":"user","content":"Weather in Paris?"},
	{"role":"assistant","content":"Checking.","tool_calls":[{"id":"call|1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},
	{"role":"tool","tool_call_id":"call|1","content":"Sunny"},
	{"role":"user","content":[{"type":"text","text":"And now?"}]}
],"tools":[{"type":"function","function":{"name":"get_weather","description":"Weather.","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`

func mustSub(t *testing.T, m cursorMsg, n protowire.Number) cursorMsg {
	t.Helper()
	sub, ok := m.sub(n)
	if !ok {
		t.Fatalf("field %d missing", n)
	}
	return sub
}

func TestCursorRunRequestCarriesHistoryAndActiveMessage(t *testing.T) {
	conv, err := parseCursorChatRequest([]byte(cursorToolChat))
	if err != nil {
		t.Fatal(err)
	}
	blobs := cursorBlobs{}
	frame := cursorRunRequest(conv, resolveCursorWireModel("composer-2.5", false), "Composer 2.5", "conv-1", blobs)
	client, _ := cursorParse(frame)
	run := mustSub(t, client, 1)

	// The trailing user message rides in the action, not the history.
	user := mustSub(t, mustSub(t, mustSub(t, run, 2), 1), 1)
	if user.str(1) != "And now?" {
		t.Fatalf("active user text = %q", user.str(1))
	}
	requested := mustSub(t, run, 9)
	param := mustSub(t, requested, 3)
	if requested.str(1) != "composer-2.5" || param.str(1) != "fast" || param.str(2) != "false" || run.str(5) != "conv-1" {
		t.Fatalf("requested model = %q %q=%q conversation %q", requested.str(1), param.str(1), param.str(2), run.str(5))
	}

	state := mustSub(t, run, 1)
	var roots []string
	var turns int
	for _, f := range state {
		blob := string(blobs[hex.EncodeToString(f.bytes)])
		switch f.num {
		case 1:
			roots = append(roots, blob)
		case 8:
			turns++
		}
	}
	want := []string{
		`{"content":"Be terse.","role":"system"}`,
		`{"content":[{"text":"Weather in Paris?","type":"text"}],"role":"user"}`,
		`{"content":[{"text":"Checking.","type":"text"},{"args":{"city":"Paris"},"toolCallId":"call_1","toolName":"get_weather","type":"tool-call"}],"role":"assistant"}`,
		`{"content":[{"result":"Sunny","toolCallId":"call_1","toolName":"get_weather","type":"tool-result"}],"id":"call_1","role":"tool"}`,
	}
	if strings.Join(roots, "\n") != strings.Join(want, "\n") {
		t.Fatalf("root prompt messages:\n%s\nwant:\n%s", strings.Join(roots, "\n"), strings.Join(want, "\n"))
	}
	if turns != 1 {
		t.Fatalf("turns = %d, want 1 (the active message is not history)", turns)
	}

	// A trailing tool result resumes over the whole history instead.
	conv.messages = conv.messages[:3]
	resume, _ := cursorParse(cursorRunRequest(conv, resolveCursorWireModel("composer-2.5", false), "", "conv-2", cursorBlobs{}))
	if _, ok := mustSub(t, mustSub(t, resume, 1), 2).field(2); !ok {
		t.Fatal("trailing tool result did not produce a resume action")
	}
}

// fakeCursorServer speaks the Run stream: it serves the history blob the
// client stored, asks for the request context, then runs script. recv(false)
// waits for the client to abandon the stream.
func fakeCursorServer(t *testing.T, script func(w http.ResponseWriter, send func([]byte), recv func(bool) cursorMsg)) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != cursorauth.RunPath || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Content-Type") != "application/connect+proto" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		frames := newCursorFrameReader(r.Body)
		recv := func(expect bool) cursorMsg {
			for {
				_, payload, err := frames.next()
				if err != nil {
					if expect {
						t.Errorf("server read: %v", err)
					}
					return nil
				}
				msg, _ := cursorParse(payload)
				if _, ok := msg.field(7); ok { // heartbeat
					continue
				}
				return msg
			}
		}
		send := func(server []byte) {
			_, _ = w.Write(cursorFrame(server))
			w.(http.Flusher).Flush()
		}
		runMsg := recv(true)
		var firstBlob cursorField
		for _, f := range mustSub(t, mustSub(t, runMsg, 1), 1) {
			if f.num == 1 {
				firstBlob = f
				break
			}
		}
		send(cursorPB(nil).msg(4, cursorPB(nil).uvarint(1, 7).msg(2, cursorPB(nil).raw(1, firstBlob.bytes))))
		kv := mustSub(t, recv(true), 3)
		if kv.uint(1) != 7 || !strings.Contains(string(mustSub(t, kv, 2).str(1)), `"role":"system"`) {
			t.Errorf("kv reply did not return the system prompt blob")
		}
		send(cursorPB(nil).msg(2, cursorPB(nil).uvarint(1, 1).str(15, "exec-1").msg(10, nil)))
		ctx := mustSub(t, mustSub(t, mustSub(t, mustSub(t, recv(true), 2), 10), 1), 1)
		if tool := mustSub(t, ctx, 7); tool.str(1) != "get_weather" || tool.str(4) != cursorMcpProvider {
			t.Errorf("request context tools = %q/%q", tool.str(1), tool.str(4))
		}
		script(w, send, recv)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func cursorInteraction(n protowire.Number, body cursorPB) []byte {
	return cursorPB(nil).msg(1, cursorPB(nil).msg(n, body))
}

func cursorTestContext(srv *httptest.Server) context.Context {
	return context.WithValue(context.Background(), "cliproxy.roundtripper", srv.Client().Transport)
}

func cursorTestAuth(srv *httptest.Server) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "cursor-test", Provider: "cursor", Metadata: map[string]any{"access_token": "test-token", "base_url": srv.URL}}
}

func TestCursorExecuteStreamTextAndUsage(t *testing.T) {
	srv := fakeCursorServer(t, func(w http.ResponseWriter, send func([]byte), _ func(bool) cursorMsg) {
		send(cursorInteraction(4, cursorPB(nil).str(1, "thinking")))
		send(cursorInteraction(1, cursorPB(nil).str(1, "Hello")))
		send(cursorInteraction(1, cursorPB(nil).str(1, " there")))
		send(cursorInteraction(14, cursorPB(nil).uvarint(1, 120).uvarint(2, 9).uvarint(3, 100)))
		end := cursorFrame([]byte(`{}`))
		end[0] = cursorFlagEndStream
		_, _ = w.Write(end)
	})
	e := NewCursorExecutor(nil)
	res, err := e.ExecuteStream(cursorTestContext(srv), cursorTestAuth(srv), cliproxyexecutor.Request{Model: "cursor/composer-2.5", Payload: []byte(cursorToolChat)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var text, reasoning strings.Builder
	var finish string
	var usage gjson.Result
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		line := strings.TrimSpace(strings.TrimPrefix(string(chunk.Payload), "data:"))
		if line == "[DONE]" || line == "" {
			continue
		}
		j := gjson.Parse(line)
		text.WriteString(j.Get("choices.0.delta.content").String())
		reasoning.WriteString(j.Get("choices.0.delta.reasoning_content").String())
		if f := j.Get("choices.0.finish_reason").String(); f != "" {
			finish = f
		}
		if u := j.Get("usage"); u.Exists() {
			usage = u
		}
	}
	if text.String() != "Hello there" || reasoning.String() != "thinking" || finish != "stop" {
		t.Fatalf("stream = %q / %q / %q", text.String(), reasoning.String(), finish)
	}
	if usage.Get("prompt_tokens").Int() != 120 || usage.Get("completion_tokens").Int() != 9 || usage.Get("prompt_tokens_details.cached_tokens").Int() != 100 {
		t.Fatalf("usage = %s", usage.Raw)
	}
}

func TestCursorExecuteHandsToolCallsToClient(t *testing.T) {
	srv := fakeCursorServer(t, func(_ http.ResponseWriter, send func([]byte), recv func(bool) cursorMsg) {
		send(cursorInteraction(1, cursorPB(nil).str(1, "Let me check.")))
		args := cursorPB(nil).str(1, "get_weather").msg(2, cursorPB(nil).str(1, "city").raw(2, cursorEncodeValue("Paris"))).str(3, "toolu_1").str(4, cursorMcpProvider).str(5, "get_weather")
		send(cursorPB(nil).msg(2, cursorPB(nil).uvarint(1, 2).str(15, "exec-2").msg(11, args)))
		reply := mustSub(t, mustSub(t, recv(true), 2), 11)
		text := mustSub(t, mustSub(t, mustSub(t, reply, 1), 1), 1).str(1)
		if !strings.Contains(text, "handed off") {
			t.Errorf("mcp reply = %q, want the handoff notice", text)
		}
		// The checkpoint after the handoff ends the response; the server never sends turnEnded.
		send(cursorPB(nil).msg(3, cursorPB(nil).msg(5, cursorPB(nil).uvarint(1, 500))))
		_ = recv(false) // blocks until the client abandons the stream
	})
	e := NewCursorExecutor(nil)
	// Claude Messages in, Claude Messages out: the engine's translators carry the call.
	claudeReq := `{"model":"cursor/composer-2.5","max_tokens":64,"tools":[{"name":"get_weather","description":"Weather.","input_schema":{"type":"object","properties":{"city":{"type":"string"}}}}],"messages":[{"role":"user","content":"Weather in Paris?"}]}`
	resp, err := e.Execute(cursorTestContext(srv), cursorTestAuth(srv), cliproxyexecutor.Request{Model: "cursor/composer-2.5", Payload: []byte(claudeReq)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatal(err)
	}
	out := gjson.ParseBytes(resp.Payload)
	if out.Get("stop_reason").String() != "tool_use" {
		t.Fatalf("stop_reason = %s in %s", out.Get("stop_reason"), resp.Payload)
	}
	var sawTool bool
	for _, block := range out.Get("content").Array() {
		if block.Get("type").String() == "tool_use" {
			sawTool = block.Get("id").String() == "toolu_1" && block.Get("name").String() == "get_weather" && block.Get("input.city").String() == "Paris"
		}
	}
	if !sawTool || out.Get("usage.input_tokens").Int() == 0 {
		t.Fatalf("claude response = %s", resp.Payload)
	}
}

func TestCursorEndStreamErrorMapsCursorErrorDetails(t *testing.T) {
	detail := cursorPB(nil).uvarint(1, 44).msg(2, cursorPB(nil).str(1, "Usage limit").str(2, "Buy credits"))
	payload, _ := json.Marshal(map[string]any{"error": map[string]any{
		"code": "resource_exhausted", "message": "Error",
		"details": []any{map[string]any{"type": "aiserver.v1.ErrorDetails", "value": base64.RawStdEncoding.EncodeToString(detail)}},
	}})
	var se statusErr
	if err := cursorEndStreamError(payload); !errors.As(err, &se) || se.code != http.StatusTooManyRequests || !strings.Contains(se.msg, "Usage limit: Buy credits") {
		t.Fatalf("structured error = %v", err)
	}
	if err := cursorEndStreamError([]byte(`{"error":{"code":"not_found","message":"model"}}`)); !cursorIsNotFound(err) {
		t.Fatalf("not_found = %v", err)
	}
	if err := cursorEndStreamError([]byte(`{"metadata":{}}`)); err != nil {
		t.Fatalf("clean end = %v", err)
	}
}
