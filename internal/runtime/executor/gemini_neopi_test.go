package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// geminiCapture is testdata/gemini_npi_capture.json.gz: NeoPi's OWN
// streamSimple run over its bundled google rows against a loopback server, one
// request per scenario, beside the downstream request a client sends NeoProxy
// for it (an OpenAI Chat body, or a native Gemini body for format "gemini").
type geminiCapture struct {
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name       string            `json:"name"`
		Model      string            `json:"model"`
		Format     string            `json:"format"`
		Method     string            `json:"method"`
		Path       string            `json:"path"`
		Headers    map[string]string `json:"headers"`
		RawBody    string            `json:"rawBody"`
		Downstream json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

// Every captured NeoPi request is reproduced by the request NeoProxy actually
// sends: method, path and query, headers and body, for each thinking mode,
// tool, image, tool-result and signed-thought history shape, read by an
// httptest server behind Execute.
func TestGeminiNeoPiRequestParity(t *testing.T) {
	file, err := os.Open("testdata/gemini_npi_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture geminiCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("capture has no fixtures")
	}
	var sent struct {
		method, uri string
		header      http.Header
		contentLen  int64
		body        []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.method, sent.uri, sent.header, sent.contentLen = r.Method, r.URL.RequestURI(), r.Header.Clone(), r.ContentLength
		sent.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Captured."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`+"\n\n")
	}))
	defer server.Close()
	exec := NewGeminiExecutor(&config.Config{})
	// The capture's base URL is the default Gemini one, version included; a
	// configured base is used verbatim, so the test base carries the version too.
	auth := &cliproxyauth.Auth{ID: "parity", Provider: "gemini", Attributes: map[string]string{"api_key": capture.APIKey, "base_url": server.URL + "/v1beta"}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			// Translators hand the shaper compact JSON.
			var downstream bytes.Buffer
			if err = json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			format := sdktranslator.FormatOpenAI
			if fixture.Format == "gemini" {
				format = sdktranslator.FormatGemini
			}
			req := cliproxyexecutor.Request{Model: fixture.Model, Payload: downstream.Bytes()}
			if _, err = exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: format}); err != nil {
				t.Fatal(err)
			}
			if sent.method != fixture.Method {
				t.Errorf("method = %s, want %s", sent.method, fixture.Method)
			}
			if sent.uri != fixture.Path {
				t.Errorf("uri = %s, want %s", sent.uri, fixture.Path)
			}
			if string(sent.body) != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", sent.body, fixture.RawBody)
			}
			// content-length is compared as the body length Go's server reports;
			// host differs by construction (loopback vs the Google host) and
			// connection is transport-owned (Bun stamps keep-alive on HTTP/1.1, Go's
			// transport manages it and forbids it on the HTTP/2 Google is served over).
			if sent.contentLen != int64(len(fixture.RawBody)) {
				t.Errorf("content-length = %d, want %d", sent.contentLen, len(fixture.RawBody))
			}
			want := map[string]string{}
			for name, value := range fixture.Headers {
				if name != "host" && name != "content-length" && name != "connection" {
					want[name] = value
				}
			}
			got := map[string]string{}
			for name, values := range sent.header {
				got[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			delete(got, "content-length")
			for name, value := range want {
				if got[name] != value {
					t.Errorf("header %s = %q, want %q", name, got[name], value)
				}
			}
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("extra header %s = %q", name, got[name])
				}
			}
		})
	}
}

func geminiTestExecutor(t *testing.T, handler http.HandlerFunc) (*NeoPiGoogleExecutor, *cliproxyauth.Auth) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "gemini", Attributes: map[string]string{"api_key": "AIzaTest", "base_url": server.URL}}
	return NewGeminiExecutor(&config.Config{}), auth
}

func geminiChatRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := []byte(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	return cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true}
}

func drainGeminiStream(t *testing.T, result *cliproxyexecutor.StreamResult) (string, error) {
	t.Helper()
	var text strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
		text.Write(chunk.Payload)
	}
	return text.String(), streamErr
}

// A stream that ends without a finish reason is truncated, as in NeoPi.
func TestGeminiNeoPiStreamWithoutFinishFails(t *testing.T) {
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"par\"}]}}]}\n\n")
	})
	req, opts := geminiChatRequest()
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, streamErr := drainGeminiStream(t, result); streamErr == nil || !strings.Contains(streamErr.Error(), "without a finish reason") {
		t.Fatalf("stream error = %v", streamErr)
	}
}

// A STOP with only whitespace text is retried, then delivered once content arrives.
func TestGeminiNeoPiEmptyResponseIsRetried(t *testing.T) {
	var attempts int
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		text := " "
		if attempts == 2 {
			text = "real"
		}
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"`+text+`"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`+"\n\n")
	})
	req, opts := geminiChatRequest()
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	text, streamErr := drainGeminiStream(t, result)
	if streamErr != nil || attempts != 2 || !strings.Contains(text, "real") {
		t.Fatalf("attempts=%d err=%v text=%s", attempts, streamErr, text)
	}
	// The empty attempt's terminal chunk is held back: one finish, after the answer.
	if n := strings.Count(text, `"finish_reason":"stop"`); n != 1 {
		t.Fatalf("terminal chunks = %d, want 1:\n%s", n, text)
	}
	if strings.Index(text, `"finish_reason":"stop"`) < strings.Index(text, "real") {
		t.Fatalf("finish precedes the answer:\n%s", text)
	}
}

// Three empty answers in a row fail like NeoPi's exhausted retries.
func TestGeminiNeoPiEmptyResponseExhaustsRetries(t *testing.T) {
	var attempts int
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}]}`+"\n\n")
	})
	req, opts := geminiChatRequest()
	if _, err := exec.Execute(context.Background(), auth, req, opts); err == nil || attempts != 3 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

// The request carries NeoPi's headers and a 4xx body surfaces with its status.
func TestGeminiNeoPiSendsKeyHeaderAndSurfacesErrors(t *testing.T) {
	var gotKey, gotAuth, gotAgent string
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotAuth, gotAgent = r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Quota","status":"RESOURCE_EXHAUSTED"}}`)
	})
	req, opts := geminiChatRequest()
	_, err := exec.Execute(context.Background(), auth, req, opts)
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("err = %T %v", err, err)
	}
	if gotKey != "AIzaTest" || gotAuth != "" || !strings.HasPrefix(gotAgent, "npi/") {
		t.Fatalf("headers key=%q auth=%q agent=%q", gotKey, gotAuth, gotAgent)
	}
}

// Discovery follows NeoPi's paged /v1beta/models listing and overlays the bundle.
func TestFetchGoogleModelsPagesAndOverlays(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-9.9-pro","displayName":"Gemini 9.9 Pro","supportedGenerationMethods":["generateContent"],"inputTokenLimit":123456,"outputTokenLimit":4000}],"nextPageToken":"p2"}`)
			return
		}
		_, _ = io.WriteString(w, `{"models":[{"name":"models/imagen-4","supportedGenerationMethods":["predict"]}]}`)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "AIzaTest", "base_url": server.URL}}
	models, err := FetchGoogleModels(context.Background(), &config.Config{}, auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "pageSize=100") || !strings.Contains(queries[0], "key=AIzaTest") || !strings.Contains(queries[1], "pageToken=p2") {
		t.Fatalf("queries = %v", queries)
	}
	found := map[string]*registry.ModelInfo{}
	for _, model := range models {
		found[model.ID] = model
	}
	if found["gemini-3.1-pro-preview"] == nil {
		t.Fatal("bundled rows must survive a listing")
	}
	if row := found["gemini-9.9-pro"]; row == nil || row.NeoPi == nil || row.NeoPi.ContextWindow != 123456 || !row.NeoPi.Reasoning {
		t.Fatalf("discovered row = %+v", row)
	}
	if found["imagen-4"] != nil {
		t.Fatal("non-generateContent models must be skipped")
	}
}

// Discovery failures never carry the key-bearing request URL.
func TestFetchGoogleModelsErrorDoesNotLeakKey(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close() // a refused port
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "AIzaFAKEKEYFORTEST", "base_url": "http://" + address + "/v1beta"}}
	_, err = FetchGoogleModels(context.Background(), &config.Config{}, auth)
	if err == nil {
		t.Fatal("want an error from a refused port")
	}
	if strings.Contains(err.Error(), "AIzaFAKEKEYFORTEST") || strings.Contains(err.Error(), "key=") {
		t.Fatalf("error leaks the key: %v", err)
	}
}

// A malformed listing is a failed refresh (the cache keeps its previous
// catalog); entries that fail NeoPi's schema are skipped, not fatal.
func TestFetchGoogleModelsValidatesListing(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `{"models":[`,
		"not an object": `[]`,
		"models a map":  `{"models":{}}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, body)
		}))
		auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "AIzaTest", "base_url": server.URL}}
		if _, err := FetchGoogleModels(context.Background(), &config.Config{}, auth); err == nil {
			t.Errorf("%s: want an error", name)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"models":[{"name":"models/mixed-pro","supportedGenerationMethods":["generateContent",3]},{"name":7,"supportedGenerationMethods":["generateContent"]},"junk",{"name":"models/ok-pro","supportedGenerationMethods":["generateContent"]}]}`)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "AIzaTest", "base_url": server.URL}}
	models, err := FetchGoogleModels(context.Background(), &config.Config{}, auth)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, model := range models {
		ids[model.ID] = true
	}
	if !ids["ok-pro"] || ids["mixed-pro"] || ids["7"] {
		t.Fatalf("ids = %v", ids)
	}
}

// PrepareRequest on the public executor clears a stale key and fails closed.
func TestGeminiNeoPiPrepareRequestFailsClosedWithoutKey(t *testing.T) {
	exec := NewGeminiExecutor(&config.Config{})
	request, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/x", nil)
	request.Header.Set("x-goog-api-key", "stale")
	request.Header.Set("Authorization", "Bearer stale")
	err := exec.PrepareRequest(request, &cliproxyauth.Auth{Attributes: map[string]string{}})
	if err == nil {
		t.Fatal("PrepareRequest without a key must fail")
	}
	if request.Header.Get("x-goog-api-key") != "" || request.Header.Get("Authorization") != "" {
		t.Fatalf("stale credential survived: %v", request.Header)
	}
	if err = exec.PrepareRequest(request, &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fresh"}}); err != nil || request.Header.Get("x-goog-api-key") != "fresh" {
		t.Fatalf("err=%v header=%v", err, request.Header)
	}
}

// Production usage uses NeoPi's mapper: a missing prompt count is inferred and
// a cache count above the prompt is clamped.
func TestGeminiNeoPiUsageNormalization(t *testing.T) {
	detail := helps.ParseGeminiUsage(geminiNormalizeUsage([]byte(`{"usageMetadata":{"candidatesTokenCount":10,"thoughtsTokenCount":5,"totalTokenCount":115}}`)))
	if detail.InputTokens != 100 {
		t.Fatalf("inferred input = %d, want 100", detail.InputTokens)
	}
	detail = helps.ParseGeminiUsage(geminiNormalizeUsage([]byte(`{"usageMetadata":{"promptTokenCount":50,"cachedContentTokenCount":80,"candidatesTokenCount":1,"totalTokenCount":51}}`)))
	if detail.InputTokens != 50 || detail.CacheReadTokens != 50 {
		t.Fatalf("clamped usage = %+v", detail)
	}
}

func geminiSentBody(t *testing.T, source sdktranslator.Format, payload string) gjson.Result {
	t.Helper()
	var sent []byte
	exec, auth := geminiTestExecutor(t, func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`+"\n\n")
	})
	req := cliproxyexecutor.Request{Model: "gemini-3-flash-preview", Payload: []byte(payload)}
	if _, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: source}); err != nil {
		t.Fatal(err)
	}
	return gjson.ParseBytes(sent)
}

// An explicit summary opt-out and a cached-content name reach the wire from an
// OpenAI Chat request, as they do from a native one.
func TestGeminiNeoPiChatSummaryOptOutAndCachedContent(t *testing.T) {
	got := geminiSentBody(t, sdktranslator.FormatOpenAI, `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"low","extra_body":{"google":{"thinking_config":{"include_thoughts":false}}},"cachedContent":"cachedContents/abc"}`)
	if got.Get("generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts must be false: %s", got.Raw)
	}
	if got.Get("cachedContent").String() != "cachedContents/abc" {
		t.Fatalf("cachedContent lost: %s", got.Raw)
	}
}

// A native request keeps same-model signed thinking, signature-only parts included.
func TestGeminiNeoPiNativeSignedThoughtSurvives(t *testing.T) {
	sig := testNativeGemini3ThoughtSignature()
	got := geminiSentBody(t, sdktranslator.FormatGemini, `{"contents":[{"role":"user","parts":[{"text":"hi"}]},{"role":"model","parts":[{"thought":true,"text":"","thoughtSignature":"`+sig+`"},{"text":"Hello","thoughtSignature":"`+sig+`"}]},{"role":"user","parts":[{"text":"more"}]}]}`)
	if !got.Get("contents.1.parts.0.thought").Bool() || got.Get("contents.1.parts.0.thoughtSignature").String() != sig || got.Get("contents.1.parts.1.thoughtSignature").String() != sig {
		t.Fatalf("signed thought lost: %s", got.Get("contents").Raw)
	}
}
