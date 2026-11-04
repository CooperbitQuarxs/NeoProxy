package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type codexNeoPiCapture struct {
	InstallID string          `json:"installId"`
	AccountID string          `json:"accountId"`
	Token     string          `json:"token"`
	SessionID string          `json:"sessionId"`
	Response  string          `json:"response"`
	Discovery json.RawMessage `json:"discovery"`
	Fixtures  []struct {
		Name         string `json:"name"`
		BaseURL      string `json:"baseUrl"`
		RequestedURL string `json:"requestedURL"`
		Scenario     struct {
			Downstream json.RawMessage `json:"downstream"`
		} `json:"scenario"`
		Requests []struct {
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			RawBody string            `json:"rawBody"`
		} `json:"requests"`
	} `json:"fixtures"`
}

var (
	codexNeoPiUUID      = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	codexNeoPiTurnStart = regexp.MustCompile(`(turn_started_at_unix_ms\\?":)\d+`)
)

// Random-by-design thread/window/turn UUIDs and the Date.now() turn start are
// the only normalizations; the installation ID stays literal.
func codexNeoPiNormalize(text, installID string) string {
	ids := map[string]string{installID: installID}
	text = codexNeoPiUUID.ReplaceAllStringFunc(text, func(id string) string {
		if _, ok := ids[id]; !ok {
			ids[id] = "<uuid-" + string(rune('a'+len(ids)-1)) + ">"
		}
		return ids[id]
	})
	return codexNeoPiTurnStart.ReplaceAllString(text, "${1}<now>")
}

// When the socket is unavailable, the OAuth Codex executor falls back to
// NeoPi's exact SSE request, including Bun's fetch framing headers and the
// zstd body for the official backend.
func TestCodexExecutorSendsNeoPiSSERequests(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_npi_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture codexNeoPiCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(capture.Discovery) }))
	defer discovery.Close()
	catalog, err := codexauth.FetchModels(context.Background(), discovery.Client(), codexauth.ModelDiscoveryOptions{AccessToken: capture.Token, AccountID: capture.AccountID, BaseURL: discovery.URL, ClientVersion: codexwire.ClientVersion})
	if err != nil || len(catalog.Models) != 1 {
		t.Fatalf("discovery: %v", err)
	}
	info := catalog.Models[0].ModelInfo()

	var mu sync.Mutex
	type seen struct {
		method, path string
		headers      map[string]string
		body         string
	}
	var latest seen
	decoder, _ := zstd.NewReader(nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Content-Encoding") == "zstd" {
			body, _ = decoder.DecodeAll(body, nil)
		}
		headers := map[string]string{"host": r.Host}
		for name, values := range r.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		mu.Lock()
		latest = seen{r.Method, r.URL.RequestURI(), headers, string(body)}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, capture.Response)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	// The captured source ran without provider session state, so it never
	// tried a socket; here the socket is refused and NeoPi falls back to SSE.
	dial := codexNeoPiDial
	codexNeoPiDial = func(context.Context, *config.Config, *cliproxyauth.Auth) codexwire.DialFunc {
		return func(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error) {
			return nil, nil, errors.New("connection refused")
		}
	}
	t.Cleanup(func() { codexNeoPiDial = dial })

	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			// Each captured scenario ran in a fresh NeoPi provider session.
			sessions := codexNeoPiSessions
			codexNeoPiSessions = codexwire.NewSessions()
			t.Cleanup(func() { codexNeoPiSessions = sessions })
			authDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(authDir, ".claude-neopi-install-id"), []byte(capture.InstallID), 0o600); err != nil {
				t.Fatal(err)
			}
			executor := NewCodexExecutor(&config.Config{AuthDir: authDir})
			auth := &cliproxyauth.Auth{ID: "codex-parity", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": capture.Token}}
			if !strings.HasPrefix(fixture.BaseURL, "https://") {
				auth.Attributes = map[string]string{"base_url": fixture.BaseURL}
			}
			downstream, err := sjson.SetBytes(fixture.Scenario.Downstream, "prompt_cache_key", capture.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			request := cliproxyexecutor.Request{
				Model:    "gpt-6.1-terra",
				Payload:  downstream,
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info},
			}
			rewrite := &neoPiCaptureTransport{target: target, base: http.DefaultTransport}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", rewrite)
			options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")}
			if _, err := executor.Execute(ctx, auth, request, options); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			actual := latest
			mu.Unlock()
			want := fixture.Requests[0]
			if rewrite.seenURL != fixture.RequestedURL || actual.method != "POST" {
				t.Errorf("URL %s %s; source POST %s", actual.method, rewrite.seenURL, fixture.RequestedURL)
			}
			if got, source := codexNeoPiNormalize(actual.body, capture.InstallID), codexNeoPiNormalize(want.RawBody, capture.InstallID); got != source {
				t.Errorf("body\ngot    %s\nsource %s", got, source)
			}
			gotHeaders, sourceHeaders := map[string]string{}, map[string]string{}
			for name, value := range actual.headers {
				if name != "content-length" && name != "host" {
					gotHeaders[name] = codexNeoPiNormalize(value, capture.InstallID)
				}
			}
			for name, value := range want.Headers {
				if name != "content-length" && name != "host" {
					sourceHeaders[name] = codexNeoPiNormalize(value, capture.InstallID)
				}
			}
			if !reflect.DeepEqual(gotHeaders, sourceHeaders) {
				t.Errorf("headers\ngot    %v\nsource %v", gotHeaders, sourceHeaders)
			}
		})
	}
}

// An HTTP client's Codex OAuth turns run websocket-first. Payload rules apply
// exactly once to each frame actually sent, after every built-in step: the
// first full create and the chained continuation both carry the override, and
// the handshake turn-state the transport injects at frame time is filtered.
func TestCodexExecutorPayloadRulesFinalizeEachWebsocketFrame(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_npi_ws_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		InstallID string          `json:"installId"`
		AccountID string          `json:"accountId"`
		Token     string          `json:"token"`
		SessionID string          `json:"sessionId"`
		Discovery json.RawMessage `json:"discovery"`
		Turns     []struct {
			Downstream json.RawMessage `json:"downstream"`
		} `json:"turns"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(capture.Discovery) }))
	defer discovery.Close()
	catalog, err := codexauth.FetchModels(context.Background(), discovery.Client(), codexauth.ModelDiscoveryOptions{AccessToken: capture.Token, AccountID: capture.AccountID, BaseURL: discovery.URL, ClientVersion: codexwire.ClientVersion})
	if err != nil || len(catalog.Models) != 1 {
		t.Fatalf("discovery: %v", err)
	}
	info := catalog.Models[0].ModelInfo()
	model := *info.Codex

	var mu sync.Mutex
	var frames []string
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, http.Header{"X-Codex-Turn-State": {"sticky-route"}})
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
			item := fmt.Sprintf(`{"id":"msg_%s","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Captured.","annotations":[]}]}`, id)
			for _, event := range []string{
				`{"type":"response.metadata","headers":{"x-codex-turn-state":"sticky-route"}}`,
				fmt.Sprintf(`{"type":"response.created","response":{"id":%q,"status":"in_progress","output":[]}}`, id),
				fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_%s","type":"message","role":"assistant","status":"in_progress","content":[]}}`, id),
				fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s}`, item),
				fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[%s],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`, id, item),
			} {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(event))
			}
		}
	}))
	defer server.Close()
	dial := codexNeoPiDial
	codexNeoPiDial = func(context.Context, *config.Config, *cliproxyauth.Auth) codexwire.DialFunc {
		return func(ctx context.Context, _ string, header http.Header) (*websocket.Conn, *http.Response, error) {
			return (&websocket.Dialer{}).DialContext(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/codex/responses", header)
		}
	}
	t.Cleanup(func() { codexNeoPiDial = dial })

	authDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(authDir, ".claude-neopi-install-id"), []byte(capture.InstallID), 0o600); err != nil {
		t.Fatal(err)
	}
	rules := config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: model.ID}}, Params: map[string]any{"text.verbosity": "high"}}},
		Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: model.ID}}, Params: []string{"client_metadata.x-codex-turn-state"}}},
	}
	executor := NewCodexExecutor(&config.Config{AuthDir: authDir, Payload: rules})
	auth := &cliproxyauth.Auth{ID: "codex-ws-barrier", Provider: "codex", Metadata: map[string]any{"type": "codex", "access_token": capture.Token}}
	for _, turn := range capture.Turns[:2] {
		downstream, errSet := sjson.SetBytes(turn.Downstream, "prompt_cache_key", capture.SessionID+"-barrier")
		if errSet != nil {
			t.Fatal(errSet)
		}
		request := cliproxyexecutor.Request{
			Model:    model.ID,
			Payload:  downstream,
			Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": info},
		}
		if _, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); errExecute != nil {
			t.Fatal(errExecute)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(frames) != 2 {
		t.Fatalf("sent %d frames, want 2", len(frames))
	}
	for index, frame := range frames {
		if got := gjson.Get(frame, "text.verbosity").String(); got != "high" {
			t.Errorf("frame %d text.verbosity = %q, want the override", index, got)
		}
		if gjson.Get(frame, "client_metadata.x-codex-turn-state").Exists() {
			t.Errorf("frame %d kept the filtered turn-state injected at frame time: %s", index, frame)
		}
	}
	if gjson.Get(frames[1], "previous_response_id").String() != "resp_1" {
		t.Errorf("the continuation did not chain on the pre-rule baseline: %s", frames[1])
	}
}
