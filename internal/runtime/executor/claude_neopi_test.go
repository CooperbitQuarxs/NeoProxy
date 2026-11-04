package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type neoPiCapturedRequest struct {
	Name             string            `json:"name"`
	IsOAuth          bool              `json:"isOAuth"`
	APIKey           string            `json:"apiKey"`
	BaseURL          string            `json:"baseUrl"`
	Input            json.RawMessage   `json:"input"`
	DownstreamStream bool              `json:"downstreamStream"`
	RequestedURL     string            `json:"requestedURL"`
	Method           string            `json:"method"`
	Path             string            `json:"path"`
	Headers          map[string]string `json:"headers"`
	RawBody          string            `json:"rawBody"`
	Body             json.RawMessage   `json:"body"`
}

type neoPiCaptureFixture struct {
	InstallID string                 `json:"installId"`
	SessionID string                 `json:"sessionId"`
	AccountID string                 `json:"accountId"`
	Token     string                 `json:"token"`
	Response  string                 `json:"response"`
	Fixtures  []neoPiCapturedRequest `json:"fixtures"`
}

type neoPiCaptureTransport struct {
	target  *url.URL
	base    http.RoundTripper
	seenURL string
}

func (r *neoPiCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.seenURL = request.URL.String()
	clone := request.Clone(request.Context())
	clone.Host = request.URL.Host
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return r.base.RoundTrip(clone)
}

func neoPiLoadFixture(t *testing.T) neoPiCaptureFixture {
	t.Helper()
	data, err := os.ReadFile("testdata/anthropic_npi_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture neoPiCaptureFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestAnthropicNPICapturedParity(t *testing.T) {
	fixture := neoPiLoadFixture(t)
	var mu sync.Mutex
	var latest neoPiCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__capture/latest" {
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"method": latest.Method, "path": latest.Path, "headers": latest.Headers, "rawBody": latest.RawBody, "body": latest.Body})
			return
		}
		body, _ := io.ReadAll(r.Body)
		headers := make(map[string]string, len(r.Header)+1)
		for name, values := range r.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		headers["host"] = r.Host
		mu.Lock()
		latest = neoPiCapturedRequest{Method: r.Method, Path: r.URL.RequestURI(), Headers: headers, RawBody: string(body), Body: body}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, fixture.Response)
	}))
	defer server.Close()
	// Optional live-source comparison runs NeoPi itself and the Go executor
	// against this SAME server. Normal CI uses the committed reference capture.
	if modules := os.Getenv("NEOPI_SOURCE_MODULES"); modules != "" {
		home := os.Getenv("NEOPI_CAPTURE_HOME")
		if home == "" {
			t.Fatal("NEOPI_CAPTURE_HOME must be an isolated capture home")
		}
		script, err := filepath.Abs("testdata/capture_anthropic_npi.ts")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bun", "--preserve-symlinks", script)
		cmd.Env = append(os.Environ(), "HOME="+home, "NEOPI_CAPTURE_URL="+server.URL, "PI_CODING_AGENT_DIR=/tmp/astra/npi-profile", "PI_PROFILE=", "NPI_PROFILE=")
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("NeoPi source capture: %v", err)
		}
		if err = json.Unmarshal(output, &fixture); err != nil {
			t.Fatal(err)
		}
		t.Log("NeoPi source and Go executor use the same local capture server; no real Anthropic requests")
	}
	target, _ := url.Parse(server.URL)
	for _, capture := range fixture.Fixtures {
		t.Run(capture.Name, func(t *testing.T) {
			auth := &cliproxyauth.Auth{ID: "parity", Provider: anthropicnpi.Provider, Prefix: anthropicnpi.Provider, Metadata: map[string]any{"access_token": fixture.Token, "neopi_install_id": fixture.InstallID, "account_uuid": fixture.AccountID, "base_url": capture.BaseURL}}
			if !capture.IsOAuth {
				auth.Metadata = nil
				auth.Attributes = map[string]string{"api_key": capture.APIKey, "base_url": capture.BaseURL}
			}
			transport := &neoPiCaptureTransport{target: target, base: http.DefaultTransport}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
			executor := NewClaudeExecutor(&config.Config{})
			request := cliproxyexecutor.Request{Model: gjson.GetBytes(capture.Input, "model").String(), Payload: capture.Input}
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), Headers: http.Header{"X-Claude-Code-Session-Id": []string{fixture.SessionID}}}
			if capture.DownstreamStream {
				stream, err := executor.ExecuteStream(ctx, auth, request, options)
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
					output.Write(chunk.Payload)
				}
				if !strings.Contains(output.String(), "Captured.") {
					t.Fatal("streamed response missing content")
				}
			} else {
				response, err := executor.Execute(ctx, auth, request, options)
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(response.Payload, "content.0.text").String() != "Captured." {
					t.Fatalf("bad native response: %s", response.Payload)
				}
			}
			mu.Lock()
			actual := latest
			mu.Unlock()
			if transport.seenURL != capture.RequestedURL {
				t.Errorf("URL got %s want %s", transport.seenURL, capture.RequestedURL)
			}
			if actual.Method != capture.Method || actual.Path != capture.Path {
				t.Errorf("method/path got %s %s want %s %s", actual.Method, actual.Path, capture.Method, capture.Path)
			}
			if !reflect.DeepEqual(actual.Headers, capture.Headers) {
				t.Errorf("headers\ngot  %#v\nwant %#v", actual.Headers, capture.Headers)
			}
			if actual.RawBody != capture.RawBody {
				t.Errorf("body (including CCH)\ngot  %s\nwant %s", actual.RawBody, capture.RawBody)
			}
		})
	}
}

func TestAnthropicNPIRetryPolicy(t *testing.T) {
	for _, code := range []int{429, 529} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			executor := NewClaudeExecutor(nil)
			var waited time.Duration
			executor.wait = func(_ context.Context, delay time.Duration) error { waited = delay; return nil }
			err := &neoPiUpstreamError{code: code, body: "rate_limit_error", headers: http.Header{"Retry-After": []string{"2"}}}
			if got := executor.retry(context.Background(), err, 0); got != nil {
				t.Fatal(got)
			}
			if waited < 2*time.Second {
				t.Fatalf("retried sooner than Retry-After: %v", waited)
			}
			err.headers.Set("Retry-After", "61")
			waited = 0
			if got := executor.retry(context.Background(), err, 0); got != err || waited != 0 {
				t.Fatal("over-cap hint did not surface original error")
			}
			err.headers.Set("Retry-After", "0")
			if got := executor.retry(context.Background(), err, 10); got != err {
				t.Fatal("provider exceeded ten retries")
			}
		})
	}
	if neoPiRetryable(&neoPiUpstreamError{code: 409, body: "conflict"}) {
		t.Fatal("provider loop must not retry terminal 409")
	}
	if neoPiRetryable(&neoPiUpstreamError{code: 429, body: "usage_limit_reached"}) {
		t.Fatal("account usage limits belong to credential selection")
	}
}

func TestAnthropicNPICacheAndTools(t *testing.T) {
	messages := make([]map[string]any, 0, 70)
	for index := 0; index < 35; index++ {
		messages = append(messages, map[string]any{"role": "user", "content": "turn"}, map[string]any{"role": "assistant", "content": []map[string]any{{"type": "thinking", "thinking": "reasoning", "signature": "signed"}, {"type": "text", "text": "answer"}}})
	}
	body := neoPiJSON(map[string]any{"messages": messages, "max_tokens": 64, "thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "high"}, "tools": []map[string]any{{"name": "lookup", "input_schema": map[string]any{"type": "object"}}}})
	projected, _, err := projectNeoPiRequest(body, "claude-opus-5-5", "https://api.anthropic.com", anthropicnpi.CodeVersion, neoPiWireIdentity{InstallID: "install", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if count := bytesCount(projected, `"cache_control"`); count != 4 {
		t.Fatalf("cache breakpoints=%d", count)
	}
	for _, message := range gjson.GetBytes(projected, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			if block.Get("type").String() == "thinking" && block.Get("cache_control").Exists() {
				t.Fatal("thinking block was cached")
			}
		}
	}
	for _, name := range []string{"web_search", "Code_Execution", "text_editor", "computer"} {
		if neoPiToolName(name) != name {
			t.Fatalf("built-in %s was prefixed", name)
		}
	}
	if neoPiToolName("_lookup") != "__lookup" {
		t.Fatal("custom leading underscore was not prefixed exactly once")
	}
	response := neoPiStripToolPrefix([]byte(`{"type":"content_block_start","content_block":{"type":"tool_use","name":"__lookup"}}`))
	if gjson.GetBytes(response, "content_block.name").String() != "_lookup" {
		t.Fatal("response stripped more than one prefix")
	}
}

func bytesCount(body []byte, text string) int { return strings.Count(string(body), text) }
