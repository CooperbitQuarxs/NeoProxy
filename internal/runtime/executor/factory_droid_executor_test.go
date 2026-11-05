package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// factoryDroidCapture is testdata/neopi_factory_droid/<wire>.json.gz: NeoPi's
// OWN streamSimple over every registry model of one wire, built for a
// signed-in account, against a loopback server, beside the downstream
// request a client sends NeoProxy for each scenario.
type factoryDroidCapture struct {
	Wire      string `json:"wire"`
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Identity  struct {
		OrgID           string `json:"orgId"`
		Region          string `json:"region"`
		InferenceRegion string `json:"inferenceRegion"`
	} `json:"identity"`
	Fixtures []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		Method       string            `json:"method"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

// Every captured NeoPi Factory request is sent byte for byte: method, path,
// headers (the per-request x-assistant-message-id is a fresh v4 UUID) and
// body, for the client format the model's wire takes.
func TestFactoryDroidReplaysNeoPiRequests(t *testing.T) {
	formats := map[string]sdktranslator.Format{
		"anthropic-messages": sdktranslator.FormatClaude,
		"openai-completions": sdktranslator.FormatOpenAI,
		"google-generate":    sdktranslator.FormatGemini,
		"openai-responses":   sdktranslator.FormatOpenAIResponse,
	}
	var sent struct {
		method, path string
		headers      http.Header
		body         []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.method, sent.path, sent.headers = r.Method, r.URL.RequestURI(), r.Header.Clone()
		sent.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	for _, wire := range []string{"anthropic-messages", "openai-completions", "google-generate", "openai-responses"} {
		capture := loadFactoryDroidCapture(t, "testdata/neopi_factory_droid/"+wire+".json.gz")
		t.Run(wire, func(t *testing.T) {
			exec := NewFactoryDroidExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: "factory-droid-replay", Provider: "factory-droid", Metadata: map[string]any{
				"type": "factory-droid", "access_token": capture.APIKey, "org_id": capture.Identity.OrgID,
				"region": capture.Identity.Region, "inference_region": capture.Identity.InferenceRegion,
			}}
			scope := registry.FactoryDroidScope{Region: capture.Identity.Region, InferenceRegion: capture.Identity.InferenceRegion, OrgID: capture.Identity.OrgID}
			for _, fixture := range capture.Fixtures {
				t.Run(fixture.Name, func(t *testing.T) {
					model := registry.FactoryDroidModelFor(fixture.Model)
					if model == nil {
						t.Fatalf("no registry model %s", fixture.Model)
					}
					requested, err := url.Parse(fixture.RequestedURL)
					if err != nil {
						t.Fatal(err)
					}
					// The account's row as discovery registers it, served from
					// the capture server at the wire's path.
					row := registry.BuildFactoryDroidModel(model, scope, capture.Identity.InferenceRegion, model.Policy.Rotation)
					prefix := requested.Path
					for _, suffix := range []string{"/v1/messages", "/chat/completions", "/generate", "/responses"} {
						prefix = strings.TrimSuffix(prefix, suffix)
					}
					row.BaseURL = server.URL + prefix
					var downstream bytes.Buffer
					if err := json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					sent.body = nil
					_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
						Model:    fixture.Model,
						Payload:  downstream.Bytes(),
						Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.FactoryDroidModelInfo(row)},
					}, cliproxyexecutor.Options{SourceFormat: formats[wire], OriginalRequest: downstream.Bytes(),
						Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: capture.SessionID}})
					if sent.body == nil {
						t.Fatal("executor sent no request")
					}
					if sent.method != fixture.Method {
						t.Errorf("method = %s, want %s", sent.method, fixture.Method)
					}
					if sent.path != requested.RequestURI() {
						t.Errorf("path = %s, want %s", sent.path, requested.RequestURI())
					}
					if string(sent.body) != fixture.RawBody {
						t.Errorf("body:\n got %s\nwant %s", sent.body, fixture.RawBody)
					}
					want := map[string]string{}
					for name, value := range fixture.Headers {
						if name != "host" && name != "content-length" {
							want[name] = value
						}
					}
					got := map[string]string{}
					for name, values := range sent.headers {
						if name != "Content-Length" {
							got[strings.ToLower(name)] = strings.Join(values, ", ")
						}
					}
					for _, headers := range []map[string]string{got, want} {
						if _, err := uuid.Parse(headers["x-assistant-message-id"]); err == nil {
							headers["x-assistant-message-id"] = "<uuid>"
						}
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("headers:\n got %v\nwant %v", got, want)
					}
				})
			}
		})
	}
}

func loadFactoryDroidCapture(t *testing.T, path string) factoryDroidCapture {
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
	var capture factoryDroidCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatalf("%s holds no fixtures", path)
	}
	return capture
}

// factoryDroidServe runs one request for model against a server answering
// with status and body, the account's row served from that server.
func factoryDroidServe(t *testing.T, model string, format sdktranslator.Format, payload string, stream bool, status int, contentType, body string) (string, error) {
	t.Helper()
	return factoryDroidServeHeaders(t, model, format, payload, stream, status, http.Header{"Content-Type": {contentType}}, body)
}

// factoryDroidServeHeaders is factoryDroidServe with the reply's headers.
func factoryDroidServeHeaders(t *testing.T, model string, format sdktranslator.Format, payload string, stream bool, status int, header http.Header, body string) (string, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range header {
			w.Header()[name] = values
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	entry := registry.FactoryDroidModelFor(model)
	if entry == nil {
		t.Fatalf("no registry model %s", model)
	}
	row := registry.BuildFactoryDroidModel(entry, registry.FactoryDroidScope{Region: "global", InferenceRegion: "global"}, "global", entry.Policy.Rotation)
	row.BaseURL = server.URL + "/api"
	auth := &cliproxyauth.Auth{ID: "factory-droid-account", Provider: "factory-droid", Metadata: map[string]any{"type": "factory-droid", "access_token": "token"}}
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(payload), Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.FactoryDroidModelInfo(row)}}
	opts := cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: []byte(payload), Stream: stream}
	exec := NewFactoryDroidExecutor(&config.Config{})
	if !stream {
		response, err := exec.Execute(context.Background(), auth, req, opts)
		return string(response.Payload), err
	}
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			return out.String(), chunk.Err
		}
		out.Write(chunk.Payload)
	}
	return out.String(), nil
}

// A chat-completions tool name over the route's bound goes out in the CLI's
// capped form, and the call that comes back carries the declared name.
func TestFactoryDroidCompletionsReturnsDeclaredToolNames(t *testing.T) {
	// inkling's route bounds tool names at 64.
	long := strings.Repeat("lookup.term.", 7)
	wire := helps.FactoryDroidCompletionsToolName(long, 64)
	if wire == long {
		t.Fatal("tool name must exceed the route bound")
	}
	upstream := "data: " + `{"id":"c","object":"chat.completion.chunk","model":"inkling","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"` + wire + `","arguments":"{}"}}]},"finish_reason":null}]}` + "\n\n" +
		"data: " + `{"id":"c","object":"chat.completion.chunk","model":"inkling","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}` + "\n\ndata: [DONE]\n\n"
	payload := `{"model":"inkling","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"` + long + `","parameters":{"type":"object"}}}],"stream":true}`
	for _, stream := range []bool{true, false} {
		out, err := factoryDroidServe(t, "inkling", sdktranslator.FormatOpenAI, payload, stream, http.StatusOK, "text/event-stream", upstream)
		if err != nil {
			t.Fatalf("stream=%v: %v", stream, err)
		}
		if !strings.Contains(out, `"name":"`+long+`"`) || strings.Contains(out, wire) {
			t.Errorf("stream=%v: returned call does not carry the declared name:\n%s", stream, out)
		}
	}
}

// Gemini routes end the way the native client does: a stream without a
// finish or block reason fails even after a tool call, otherwise a tool call
// wins over any finish reason, a prompt block fails, and a returned call
// carries the declared tool name.
func TestFactoryDroidGeminiTerminal(t *testing.T) {
	payload := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"my.tool","parameters":{"type":"object"}}]}]}`
	frame := func(chunk string) string { return "data: " + chunk + "\n\n" }
	for _, tc := range []struct {
		name, body, wantErr, wantOut string
	}{
		{"tool call wins", frame(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"my_tool","args":{}}}]},"finishReason":"SAFETY"}]}`), "", `"name":"my.tool"`},
		{"max tokens", frame(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"MAX_TOKENS"}]}`), "", `Hi`},
		{"prompt block", frame(`{"promptFeedback":{"blockReason":"SAFETY"}}`), "Generation was blocked by content filters (SAFETY)", ""},
		{"blocked finish", frame(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"RECITATION"}]}`), "Generation was blocked by content filters (RECITATION)", ""},
		{"unknown finish", frame(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"OTHER"}]}`), "Unknown finish reason: OTHER", ""},
		{"truncated", frame(`{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]}}]}`), "closed before a finish_reason", ""},
		{"truncated tool call", frame(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"my_tool","args":{}}}]}}]}`), "closed before a finish_reason", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := factoryDroidServe(t, "garnet-07-15", sdktranslator.FormatGemini, payload, false, http.StatusOK, "text/event-stream", tc.body)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.wantOut) {
				t.Errorf("response lacks %s:\n%s", tc.wantOut, out)
			}
		})
	}
}

// A Gemini route's rejection keeps the reply's headers, so its Retry-After
// reaches scheduling (GoogleApiError carries response.headers).
func TestFactoryDroidGeminiRejectionKeepsRetryAfter(t *testing.T) {
	payload := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	header := http.Header{"Content-Type": {"application/json"}, "Retry-After": {"75"}}
	for _, stream := range []bool{false, true} {
		_, err := factoryDroidServeHeaders(t, "garnet-07-15", sdktranslator.FormatGemini, payload, stream, http.StatusTooManyRequests, header, `{"error":{"code":429,"message":"Slow down"}}`)
		var hinted interface {
			StatusCode() int
			RetryAfter() *time.Duration
		}
		if !errors.As(err, &hinted) || hinted.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("stream=%v: err = %T %v, want a 429 carrying its retry hint", stream, err, err)
		}
		if after := hinted.RetryAfter(); after == nil || *after != 75*time.Second {
			t.Errorf("stream=%v: RetryAfter = %v, want 75s", stream, after)
		}
	}
}

// Every Factory wire carries the account's configured headers over droid's
// identity, as NeoPi spreads the caller's headers over the identity set: a
// custom gateway's own key reaches the upstream on each of them.
func TestFactoryDroidSendsConfiguredHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, model, payload string
		format               sdktranslator.Format
		opts                 func([]byte) cliproxyexecutor.Options
	}{
		{name: "messages", model: "claude-opus-5-5", format: sdktranslator.FormatClaude, payload: `{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`},
		{name: "completions", model: "inkling", format: sdktranslator.FormatOpenAI, payload: `{"model":"inkling","messages":[{"role":"user","content":"hi"}]}`},
		{name: "responses", model: "gpt-6-astra", format: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-6-astra","input":"hi"}`},
		{name: "gemini", model: "garnet-07-15", format: sdktranslator.FormatGemini, payload: `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
		{name: "image", model: "gpt-image-2.5-sunburst", payload: `{"model":"gpt-image-2.5-sunburst","prompt":"A red square."}`,
			opts: func(payload []byte) cliproxyexecutor.Options {
				return factoryDroidImageOptions("/v1/images/generations", "session", payload)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Clone()
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer server.Close()
			entry := registry.FactoryDroidModelFor(tc.model)
			if entry == nil {
				t.Fatalf("no registry model %s", tc.model)
			}
			row := registry.BuildFactoryDroidModel(entry, registry.FactoryDroidScope{Region: "global", InferenceRegion: "global"}, "global", entry.Policy.Rotation)
			row.BaseURL = server.URL + "/api"
			auth := &cliproxyauth.Auth{ID: "factory-droid-gateway", Provider: "factory-droid", Metadata: map[string]any{"type": "factory-droid", "access_token": "token"},
				Attributes: map[string]string{"header:X-Custom-Gateway-Key": "required-key"}}
			req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.payload), Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.FactoryDroidModelInfo(row)}}
			opts := cliproxyexecutor.Options{SourceFormat: tc.format, OriginalRequest: []byte(tc.payload)}
			if tc.opts != nil {
				opts = tc.opts([]byte(tc.payload))
			}
			_, _ = NewFactoryDroidExecutor(&config.Config{}).Execute(context.Background(), auth, req, opts)
			if got == nil {
				t.Fatal("executor sent no request")
			}
			if key := got.Get("X-Custom-Gateway-Key"); key != "required-key" {
				t.Errorf("X-Custom-Gateway-Key = %q, want the configured required-key", key)
			}
		})
	}
}

// NeoPi checks a Factory request's effort against the model's own ladder
// before any wire (requireSupportedEffort): the effort the caller named, not
// the one the proxy's thinking normalization would fit, is refused with
// NeoPi's message, and nothing is sent. Each case is one NeoPi's own
// streamSimple refuses with exactly this message.
func TestFactoryDroidRejectsUnsupportedEffort(t *testing.T) {
	for _, tc := range []struct {
		name, model, payload, want string
		format                     sdktranslator.Format
	}{
		{"responses", "gpt-5.4", `{"model":"gpt-5.4","input":"hi","reasoning":{"effort":"max"}}`,
			"Thinking effort max is not supported by factory-droid/gpt-5.4. Supported efforts: low, medium, high, xhigh", sdktranslator.FormatOpenAIResponse},
		{"messages", "claude-opus-4-5-20251101", `{"model":"claude-opus-4-5-20251101","max_tokens":64,"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},"messages":[{"role":"user","content":"hi"}]}`,
			"Thinking effort max is not supported by factory-droid/claude-opus-4-5-20251101. Supported efforts: low, medium, high", sdktranslator.FormatClaude},
		{"completions", "glm-5.3", `{"model":"glm-5.3","reasoning_effort":"medium","messages":[{"role":"user","content":"hi"}]}`,
			"Thinking effort medium is not supported by factory-droid/glm-5.3. Supported efforts: low, high, max", sdktranslator.FormatOpenAI},
		{"gemini", "garnet-07-15", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"minimal"}}}`,
			"Thinking effort minimal is not supported by factory-droid/garnet-07-15. Supported efforts: medium, high", sdktranslator.FormatGemini},
		{"gemini-snake-containers", "garnet-07-15", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generation_config":{"thinking_config":{"thinking_level":"minimal"}}}`,
			"Thinking effort minimal is not supported by factory-droid/garnet-07-15. Supported efforts: medium, high", sdktranslator.FormatGemini},
		{"gemini-snake-thinking-container", "garnet-07-15", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinking_config":{"thinkingLevel":"minimal"}}}`,
			"Thinking effort minimal is not supported by factory-droid/garnet-07-15. Supported efforts: medium, high", sdktranslator.FormatGemini},
		{"gemini-level-lowercased", "garnet-07-15", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generation_config":{"thinking_config":{"thinking_level":"MINIMAL"}}}`,
			"Thinking effort minimal is not supported by factory-droid/garnet-07-15. Supported efforts: medium, high", sdktranslator.FormatGemini},
		{"non-reasoning", "gpt-image-2.5-sunburst", `{"model":"gpt-image-2.5-sunburst","input":"hi","reasoning":{"effort":"high"}}`,
			"Model factory-droid/gpt-image-2.5-sunburst does not support thinking", sdktranslator.FormatOpenAIResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent, err := factoryDroidEffortAttempt(t, tc.model, tc.format, tc.payload)
			var coded interface{ StatusCode() int }
			if err == nil || err.Error() != tc.want || !errors.As(err, &coded) || coded.StatusCode() != http.StatusBadRequest {
				t.Errorf("err = %v, want a 400 %q", err, tc.want)
			}
			if sent {
				t.Error("an unsupported effort reached the upstream")
			}
		})
	}
}

// A Gemini request names an effort only as the native request reader reads
// one: the first existing container and level of either spelling, and only
// a string level. A level of another type (shadowing a string one under the
// other spelling included) or a budget names none, and the request is sent.
func TestFactoryDroidSendsGeminiWithoutNamedEffort(t *testing.T) {
	contents := `"contents":[{"role":"user","parts":[{"text":"hi"}]}]`
	for _, tc := range []struct{ name, thinking string }{
		{"numeric level", `"generation_config":{"thinking_config":{"thinkingLevel":1}}`},
		{"boolean level with budget", `"generationConfig":{"thinking_config":{"thinkingLevel":false,"thinking_budget":4096}}`},
		{"object level", `"generation_config":{"thinking_config":{"thinking_level":{}}}`},
		{"numeric level shadows string level", `"generation_config":{"thinkingConfig":{"thinkingLevel":1,"thinking_level":"minimal"}}`},
		{"budget under snake containers", `"generation_config":{"thinking_config":{"thinking_budget":4096}}`},
		{"budget under camel containers", `"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent, err := factoryDroidEffortAttempt(t, "garnet-07-15", sdktranslator.FormatGemini, `{`+contents+`,`+tc.thinking+`}`)
			if !sent {
				t.Errorf("the request was refused before the upstream: %v", err)
			}
		})
	}
}

// factoryDroidEffortAttempt executes payload on a Factory row against a
// loopback upstream answering 400, reporting whether anything was sent and
// the error.
func factoryDroidEffortAttempt(t *testing.T, model string, format sdktranslator.Format, payload string) (bool, error) {
	t.Helper()
	sent := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = true
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	entry := registry.FactoryDroidModelFor(model)
	if entry == nil {
		t.Fatalf("no registry model %s", model)
	}
	row := registry.BuildFactoryDroidModel(entry, registry.FactoryDroidScope{Region: "global", InferenceRegion: "global"}, "global", entry.Policy.Rotation)
	row.BaseURL = server.URL + "/api"
	auth := &cliproxyauth.Auth{ID: "factory-droid-effort", Provider: "factory-droid", Metadata: map[string]any{"type": "factory-droid", "access_token": "token"}}
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(payload), Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.FactoryDroidModelInfo(row)}}
	_, err := NewFactoryDroidExecutor(&config.Config{}).Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: []byte(payload)})
	return sent, err
}

// A region rejection explains itself without excluding the model, keeping
// the upstream status.
func TestFactoryDroidRewritesRegionRejection(t *testing.T) {
	payload := `{"model":"inkling","messages":[{"role":"user","content":"hi"}],"stream":true}`
	_, err := factoryDroidServe(t, "inkling", sdktranslator.FormatOpenAI, payload, false, http.StatusForbidden, "application/json", `{"error":{"message":"Model is not available in this region"}}`)
	if err == nil || err.Error() != "inkling is not served from your network's region. Choose another model." {
		t.Fatalf("err = %v", err)
	}
	var coded interface{ StatusCode() int }
	if !errors.As(err, &coded) || coded.StatusCode() != http.StatusForbidden {
		t.Errorf("status = %v, want 403", err)
	}
}

// Discovery asks what NeoPi's fetchFactoryDroidModels asks (both endpoints
// on the account's residency host, with droid's client identity), answers
// with NeoPi's roster, and stays unanswered where NeoPi returned null
// (testdata of internal/registry: capture_factory_droid_discovery.ts).
func TestFetchFactoryDroidModelsMatchesNeoPi(t *testing.T) {
	file, err := os.Open("../../registry/testdata/factory_droid_discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Token     string `json:"token"`
		Scenarios []struct {
			Name  string `json:"name"`
			Scope struct {
				Region          string `json:"region"`
				InferenceRegion string `json:"inferenceRegion"`
				OrgID           string `json:"orgId"`
			} `json:"scope"`
			Flags, Settings struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
			}
			Requests []struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"requests"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"scenarios"`
	}
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	original := factoryDroidAPIBaseURL
	defer func() { factoryDroidAPIBaseURL = original }()
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var host string
			var mu sync.Mutex
			seen := map[string]http.Header{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen[r.URL.Path] = r.Header.Clone()
				mu.Unlock()
				answer := scenario.Flags
				if r.URL.Path == "/api/organization/managed-settings" {
					answer = scenario.Settings
				}
				w.WriteHeader(answer.Status)
				_, _ = io.WriteString(w, answer.Body)
			}))
			defer server.Close()
			factoryDroidAPIBaseURL = func(region string) string {
				host = original(region)
				return server.URL
			}
			auth := &cliproxyauth.Auth{ID: "factory-droid-account", Provider: "factory-droid", Metadata: map[string]any{"type": "factory-droid",
				"access_token": capture.Token, "region": scenario.Scope.Region, "inference_region": scenario.Scope.InferenceRegion, "org_id": scenario.Scope.OrgID}}
			models, err := FetchFactoryDroidModels(context.Background(), &config.Config{}, auth)
			for _, request := range scenario.Requests {
				requested, errURL := url.Parse(request.URL)
				if errURL != nil {
					t.Fatal(errURL)
				}
				if host+requested.Path != request.URL {
					t.Errorf("request %s, want %s", host+requested.Path, request.URL)
				}
				mu.Lock()
				sent, ok := seen[requested.Path]
				mu.Unlock()
				if !ok {
					t.Fatalf("no request to %s", requested.Path)
				}
				for name, value := range request.Headers {
					if got := sent.Get(name); got != value {
						t.Errorf("%s %s = %q, want %q", requested.Path, name, got, value)
					}
				}
				if sent.Get("X-Factory-Org-Id") != "" && request.Headers["x-factory-org-id"] == "" {
					t.Errorf("%s sends X-Factory-Org-Id NeoPi omits", requested.Path)
				}
			}
			if scenario.Models == nil {
				if err == nil {
					t.Fatalf("discovery answered %d models; NeoPi returned null", len(models))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got, want []string
			for _, model := range models {
				got = append(got, model.ID)
			}
			for _, model := range scenario.Models {
				want = append(want, model.ID)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("models:\n got %v\nwant %v", got, want)
			}
		})
	}
}
