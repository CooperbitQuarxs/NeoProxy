package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/bedrockwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// amazonBedrockFrame is one scripted eventstream message: an event, an
// exception or an error frame.
type amazonBedrockFrame struct {
	Event     string          `json:"event"`
	Payload   json.RawMessage `json:"payload"`
	Exception string          `json:"exception"`
	Error     string          `json:"error"`
	Message   string          `json:"message"`
}

type amazonBedrockReply struct {
	Status int                  `json:"status"`
	Frames []amazonBedrockFrame `json:"frames"`
	Body   string               `json:"body"`
}

// amazonBedrockCapture is ../../bedrockwire/testdata/neopi_bedrock_gateway.json.gz:
// NeoPi's OWN auth gateway serving OpenAI chat bodies through streamBedrock
// against a scripted Converse stream.
type amazonBedrockCapture struct {
	APIKey  string `json:"apiKey"`
	Results []struct {
		Name     string               `json:"name"`
		Env      map[string]string    `json:"env"`
		Body     string               `json:"body"`
		Replies  []amazonBedrockReply `json:"replies"`
		Upstream []struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"upstream"`
		Reply string `json:"reply"`
	} `json:"results"`
}

// encodeAmazonBedrockFrame frames string headers and a payload as NeoPi's
// test encoder does.
func encodeAmazonBedrockFrame(headers [][2]string, payload []byte) []byte {
	var head []byte
	for _, header := range headers {
		head = append(head, byte(len(header[0])))
		head = append(head, header[0]...)
		head = append(head, 7)
		head = binary.BigEndian.AppendUint16(head, uint16(len(header[1])))
		head = append(head, header[1]...)
	}
	total := 16 + len(head) + len(payload)
	out := binary.BigEndian.AppendUint32(nil, uint32(total))
	out = binary.BigEndian.AppendUint32(out, uint32(len(head)))
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out))
	out = append(append(out, head...), payload...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out))
}

func (r amazonBedrockReply) bytes() []byte {
	if r.Frames == nil {
		return []byte(r.Body)
	}
	var out []byte
	for _, frame := range r.Frames {
		switch {
		case frame.Event != "":
			payload := []byte(frame.Payload)
			if s := gjson.ParseBytes(frame.Payload); s.Type == gjson.String {
				payload = []byte(s.Str)
			}
			out = append(out, encodeAmazonBedrockFrame([][2]string{{":message-type", "event"}, {":event-type", frame.Event}, {":content-type", "application/json"}}, payload)...)
		case frame.Exception != "":
			payload, _ := json.Marshal(map[string]string{"message": frame.Message})
			out = append(out, encodeAmazonBedrockFrame([][2]string{{":message-type", "exception"}, {":exception-type", frame.Exception}, {":content-type", "application/json"}}, payload)...)
		default:
			out = append(out, encodeAmazonBedrockFrame([][2]string{{":message-type", "error"}, {":error-code", frame.Error}, {":error-message", frame.Message}}, nil)...)
		}
	}
	return out
}

type amazonBedrockRecorded struct {
	uri     string
	headers http.Header
	body    string
}

// amazonBedrockServer answers Converse requests with scripted replies and
// records every request.
func amazonBedrockServer(t *testing.T, replies []amazonBedrockReply) (*httptest.Server, func() []amazonBedrockRecorded) {
	var mu sync.Mutex
	var recorded []amazonBedrockRecorded
	queue := append(replies[:0:0], replies...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		recorded = append(recorded, amazonBedrockRecorded{uri: r.RequestURI, headers: r.Header.Clone(), body: string(body)})
		if len(queue) == 0 {
			mu.Unlock()
			http.Error(w, "no scripted reply", 599)
			return
		}
		reply := queue[0]
		queue = queue[1:]
		mu.Unlock()
		if reply.Frames != nil {
			w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(reply.Status)
		_, _ = w.Write(reply.bytes())
	}))
	t.Cleanup(server.Close)
	return server, func() []amazonBedrockRecorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]amazonBedrockRecorded(nil), recorded...)
	}
}

// amazonBedrockTurn is what a client reads from a chat completion stream:
// its text, reasoning, tool calls, finish reason, usage, or the error.
type amazonBedrockTurn struct {
	Content, Reasoning, Finish string
	ToolCalls                  []string
	Usage                      string
	Error                      string
}

// summarizeAmazonBedrockStream reads chat completion SSE lines.
func summarizeAmazonBedrockStream(lines []string) amazonBedrockTurn {
	var turn amazonBedrockTurn
	calls := map[int64]*[3]string{}
	var order []int64
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		chunk := gjson.Parse(line)
		if message := chunk.Get("error.message"); message.Exists() {
			turn.Error = message.String()
			continue
		}
		for _, choice := range chunk.Get("choices").Array() {
			delta := choice.Get("delta")
			turn.Content += delta.Get("content").String()
			turn.Reasoning += delta.Get("reasoning_content").String()
			for _, call := range delta.Get("tool_calls").Array() {
				index := call.Get("index").Int()
				entry, ok := calls[index]
				if !ok {
					entry = &[3]string{}
					calls[index] = entry
					order = append(order, index)
				}
				if id := call.Get("id").String(); id != "" {
					entry[0] = id
				}
				if name := call.Get("function.name").String(); name != "" {
					entry[1] = name
				}
				entry[2] += call.Get("function.arguments").String()
			}
			if finish := choice.Get("finish_reason"); finish.Type == gjson.String {
				turn.Finish = finish.Str
			}
		}
		if usage := chunk.Get("usage"); usage.IsObject() {
			turn.Usage = strings.Join([]string{usage.Get("prompt_tokens").String(), usage.Get("completion_tokens").String(),
				usage.Get("total_tokens").String(), usage.Get("prompt_tokens_details.cached_tokens").String()}, "/")
		}
	}
	for _, index := range order {
		entry := calls[index]
		turn.ToolCalls = append(turn.ToolCalls, entry[0]+" "+entry[1]+" "+entry[2])
	}
	return turn
}

// runAmazonBedrock streams one turn and returns what the client read.
func runAmazonBedrock(t *testing.T, exec *AmazonBedrockExecutor, auth *cliproxyauth.Auth, body string) amazonBedrockTurn {
	t.Helper()
	req := cliproxyexecutor.Request{Model: gjson.Get(body, "model").String(), Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true}
	stream, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		return amazonBedrockTurn{Error: err.Error()}
	}
	var lines []string
	var failure error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			failure = chunk.Err
			continue
		}
		lines = append(lines, strings.Split(string(chunk.Payload), "\n")...)
	}
	turn := summarizeAmazonBedrockStream(lines)
	if failure != nil {
		turn = amazonBedrockTurn{Error: failure.Error()}
	}
	return turn
}

func newAmazonBedrockTestExecutor(cfg *config.Config, env map[string]string) *AmazonBedrockExecutor {
	exec := NewAmazonBedrockExecutor("", cfg)
	exec.wait = func(context.Context, time.Duration) error { return nil }
	exec.ambientRegion = func() string {
		if region := env["AWS_REGION"]; region != "" {
			return region
		}
		return env["AWS_DEFAULT_REGION"]
	}
	return exec
}

// Every gateway chat body reaches Bedrock exactly as NeoPi's gateway sends
// it through streamBedrock (the URL with its resolved region, the headers and
// the Converse body byte for byte) for each attempt fetchWithRetry makes, and
// the client reads the same turn NeoPi's gateway answered with: text,
// reasoning, tool calls, finish reason and usage, or the same error message.
func TestAmazonBedrockMatchesNeoPiGateway(t *testing.T) {
	raw, err := os.ReadFile("../../bedrockwire/testdata/neopi_bedrock_gateway.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var capture amazonBedrockCapture
	if err = json.NewDecoder(zr).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Results) == 0 {
		t.Fatal("capture has no scenarios")
	}
	rows, err := registry.AmazonBedrockBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range capture.Results {
		t.Run(tc.Name, func(t *testing.T) {
			server, recorded := amazonBedrockServer(t, tc.Replies)
			exec := newAmazonBedrockTestExecutor(&config.Config{}, tc.Env)
			auth := &cliproxyauth.Auth{ID: "amazon-bedrock-test", Provider: "amazon-bedrock",
				Attributes: map[string]string{"api_key": capture.APIKey, "base_url": server.URL}}
			got := runAmazonBedrock(t, exec, auth, tc.Body)

			requests := recorded()
			if len(requests) != len(tc.Upstream) {
				t.Fatalf("requests = %d, NeoPi sent %d", len(requests), len(tc.Upstream))
			}
			modelID := gjson.Get(tc.Body, "model").String()
			for i, request := range requests {
				want := tc.Upstream[i]
				wantURL, errURL := url.Parse(want.URL)
				if errURL != nil {
					t.Fatal(errURL)
				}
				if request.uri != wantURL.RequestURI() {
					t.Errorf("request %d path = %s, NeoPi %s", i, request.uri, wantURL.RequestURI())
				}
				if request.body != want.Body {
					t.Errorf("request %d body:\n got %s\nwant %s", i, request.body, want.Body)
				}
				for name, value := range want.Headers {
					if got := request.headers.Get(name); got != value {
						t.Errorf("request %d %s = %q, NeoPi %q", i, name, got, value)
					}
				}
			}
			// The bundled row's AWS host re-pointed at the resolved region.
			for _, row := range rows {
				if row.ID != modelID {
					continue
				}
				scheme, host, path, search, ok := registry.NeoPiURLParts(row.BaseURL)
				if !ok {
					t.Fatalf("row base %q does not parse", row.BaseURL)
				}
				full := bedrockwire.RequestURL(bedrockwire.Base{Scheme: scheme, Host: host, Path: path, Search: search},
					bedrockwire.Region(row.ID, exec.ambientRegion()), row.ID)
				if full != tc.Upstream[0].URL {
					t.Errorf("url = %s, NeoPi %s", full, tc.Upstream[0].URL)
				}
			}

			want := summarizeAmazonBedrockStream(strings.Split(tc.Reply, "\n"))
			if want.Error != "" {
				// NeoPi's gateway streams a failure after its 200; the turn fails
				// here. NeoPi appends the path of a request dump it wrote to its
				// own log directory to a 400's message, which has no counterpart.
				message, _, _ := strings.Cut(want.Error, "\nraw-http-request=")
				want = amazonBedrockTurn{Error: message}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("client turn:\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// Configured payload rules are the last word on the Converse body, on the
// first attempt and on the retry that drops prefix-bound reasoning: an
// override replaces the thinking fields the request resolved, and nothing
// rebuilt afterwards restores them.
func TestAmazonBedrockPayloadRulesApplyLast(t *testing.T) {
	prefixFailure := `{"message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block: the block is bound to a different conversation prefix"}`
	ok := amazonBedrockReply{Status: http.StatusOK, Frames: []amazonBedrockFrame{
		{Event: "messageStart", Payload: json.RawMessage(`{"role":"assistant"}`)},
		{Event: "contentBlockDelta", Payload: json.RawMessage(`{"contentBlockIndex":0,"delta":{"text":"ok"}}`)},
		{Event: "messageStop", Payload: json.RawMessage(`{"stopReason":"end_turn"}`)},
	}}
	server, recorded := amazonBedrockServer(t, []amazonBedrockReply{{Status: http.StatusBadRequest, Body: prefixFailure}, ok})
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "us.anthropic.claude-opus-5-5", Protocol: "bedrock"}},
		Params: map[string]any{"additionalModelRequestFields.output_config.effort": "max", "inferenceConfig.maxTokens": 777},
	}}}}
	exec := newAmazonBedrockTestExecutor(cfg, nil)
	auth := &cliproxyauth.Auth{ID: "amazon-bedrock-test", Provider: "amazon-bedrock", Attributes: map[string]string{"api_key": "key", "base_url": server.URL}}
	body := `{"model":"us.anthropic.claude-opus-5-5","messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"high","stream":true}`
	if turn := runAmazonBedrock(t, exec, auth, body); turn.Error != "" || turn.Content != "ok" {
		t.Fatalf("turn = %+v", turn)
	}
	got := recorded()
	if len(got) != 2 {
		t.Fatalf("requests = %d, want the prefix-binding retry", len(got))
	}
	for i, request := range got {
		if effort := gjson.Get(request.body, "additionalModelRequestFields.output_config.effort").String(); effort != "max" {
			t.Errorf("request %d effort = %q, want the configured max", i, effort)
		}
		if limit := gjson.Get(request.body, "inferenceConfig.maxTokens").Int(); limit != 777 {
			t.Errorf("request %d maxTokens = %d, want 777", i, limit)
		}
		if binding := gjson.Get(request.body, "additionalModelRequestFields.thinking.block_binding.prefix_mismatch_behavior").String(); binding != "drop_block" {
			t.Errorf("request %d lost thinking binding: %s", i, request.body)
		}
	}
}
