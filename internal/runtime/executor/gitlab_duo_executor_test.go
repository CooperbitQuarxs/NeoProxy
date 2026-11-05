package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/gitlabduo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// gitlabDuoCapture is testdata/neopi_gitlab_duo.json.gz: NeoPi's OWN
// streamSimple over every bundled GitLab Duo row, against loopback answers
// for gitlab.com and cloud.gitlab.com (capture_neopi_gitlab_duo.ts). Each
// fixture records the direct-access mint and the model request NeoPi sent,
// beside the downstream request a client sends NeoProxy in the routed
// wire's format.
type gitlabDuoCapture struct {
	InstallID           string            `json:"installId"`
	APIKey              string            `json:"apiKey"`
	SessionID           string            `json:"sessionId"`
	DirectAccessToken   string            `json:"directAccessToken"`
	DirectAccessHeaders map[string]string `json:"directAccessHeaders"`
	Fixtures            []struct {
		Name       string            `json:"name"`
		Model      string            `json:"model"`
		Route      string            `json:"route"`
		Downstream json.RawMessage   `json:"downstream"`
		Mint       gitlabDuoWireCall `json:"mint"`
		Request    gitlabDuoWireCall `json:"request"`
		Options    map[string]any    `json:"options"`
	} `json:"fixtures"`
	// Edges are the direct-access cases, once per route: rejected and
	// malformed mints, caller headers over the minted ones, and the cache.
	Edges []struct {
		Name         string              `json:"name"`
		Model        string              `json:"model"`
		Route        string              `json:"route"`
		Downstream   json.RawMessage     `json:"downstream"`
		Options      map[string]any      `json:"options"`
		Requests     []gitlabDuoWireCall `json:"requests"`
		ErrorMessage *string             `json:"errorMessage"`
		First        []string            `json:"first"`
		Reused       []string            `json:"reused"`
	} `json:"edges"`
}

type gitlabDuoWireCall struct {
	Method  string            `json:"method"`
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
	RawBody string            `json:"rawBody"`
}

func loadGitLabDuoCapture(t *testing.T) gitlabDuoCapture {
	t.Helper()
	file, err := os.Open("testdata/neopi_gitlab_duo.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture gitlabDuoCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("neopi_gitlab_duo.json.gz holds no fixtures")
	}
	return capture
}

// gitlabDuoLoopback answers GitLab's hosts in process: every request to
// gitlab.com or cloud.gitlab.com reaches a loopback server that records what
// crossed the wire. The mint gets mint's answer, a model request model's, or
// a 400 so no executor retry follows.
type gitlabDuoLoopback struct {
	server *httptest.Server
	calls  []gitlabDuoWireCall
	mint   func(w http.ResponseWriter)
	model  func(w http.ResponseWriter)
}

func newGitLabDuoLoopback(t *testing.T) *gitlabDuoLoopback {
	t.Helper()
	loop := &gitlabDuoLoopback{}
	loop.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{}
		for name, values := range r.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		host := r.Header.Get("X-Loopback-Host")
		delete(headers, "x-loopback-host")
		loop.calls = append(loop.calls, gitlabDuoWireCall{Method: r.Method, Host: host, Path: r.URL.RequestURI(), Headers: headers, RawBody: string(body)})
		if r.URL.Path == "/api/v4/ai/third_party_agents/direct_access" {
			loop.mint(w)
			return
		}
		if loop.model != nil {
			loop.model(w)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(loop.server.Close)
	return loop
}

// context routes GitLab's hosts to the loopback server; any other host fails.
func (l *gitlabDuoLoopback) context() context.Context {
	target, _ := url.Parse(l.server.URL)
	return helps.WithTransportWrapper(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "gitlab.com" && req.URL.Host != "cloud.gitlab.com" {
				return nil, &url.Error{Op: req.Method, URL: req.URL.String(), Err: io.ErrUnexpectedEOF}
			}
			out := req.Clone(req.Context())
			out.Header.Set("X-Loopback-Host", req.URL.Host)
			out.URL.Scheme, out.URL.Host, out.Host = target.Scheme, target.Host, ""
			return http.DefaultTransport.RoundTrip(out)
		})
	})
}

func assertGitLabDuoWireCall(t *testing.T, label string, got, want gitlabDuoWireCall) {
	t.Helper()
	if got.Method != want.Method || got.Host != want.Host || got.Path != want.Path {
		t.Errorf("%s = %s %s%s, NeoPi sent %s %s%s", label, got.Method, got.Host, got.Path, want.Method, want.Host, want.Path)
	}
	if got.RawBody != want.RawBody {
		t.Errorf("%s body:\n got %s\nwant %s", label, got.RawBody, want.RawBody)
	}
	expected := map[string]string{}
	for name, value := range want.Headers {
		if name != "host" && name != "content-length" {
			expected[name] = value
		}
	}
	sent := map[string]string{}
	for name, value := range got.Headers {
		if name != "content-length" {
			sent[name] = value
		}
	}
	if !reflect.DeepEqual(sent, expected) {
		t.Errorf("%s headers:\n got %v\nwant %v", label, sent, expected)
	}
}

// The GitLab Duo executor sends, for every bundled row and scenario, the
// direct-access mint and the model request NeoPi's own streamGitLabDuo
// sends: the route's host and path, the upstream model id, body bytes and
// headers, the minted headers included.
func TestGitLabDuoReplaysNeoPiRequests(t *testing.T) {
	capture := loadGitLabDuoCapture(t)
	formats := map[string]sdktranslator.Format{
		"anthropic-messages": sdktranslator.FormatClaude,
		"openai-completions": sdktranslator.FormatOpenAI,
		"openai-responses":   sdktranslator.FormatOpenAIResponse,
	}
	authDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(authDir, ".claude-neopi-install-id"), []byte(capture.InstallID), 0o600); err != nil {
		t.Fatal(err)
	}
	loop := newGitLabDuoLoopback(t)
	minted, _ := json.Marshal(map[string]any{"token": capture.DirectAccessToken, "headers": capture.DirectAccessHeaders})
	loop.mint = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(minted)
	}
	bundled, err := registry.GitLabDuoBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	exec := NewGitLabDuoExecutor(gitlabduo.Provider, &config.Config{AuthDir: authDir})
	auth := &cliproxyauth.Auth{ID: "gitlab-duo-replay", Provider: gitlabduo.Provider, Metadata: map[string]any{"type": gitlabduo.Provider, "access_token": capture.APIKey}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			var row *registry.NeoPiModel
			for _, candidate := range bundled {
				if candidate.ID == fixture.Model {
					row = candidate
				}
			}
			if row == nil {
				t.Fatalf("no bundled row %s", fixture.Model)
			}
			if row.API != fixture.Route {
				t.Fatalf("row %s routes to %s, NeoPi routed to %s", row.ID, row.API, fixture.Route)
			}
			var downstream bytes.Buffer
			if err := json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			gitlabduo.ClearDirectAccessCache()
			loop.calls = nil
			_, _ = exec.Execute(loop.context(), auth, cliproxyexecutor.Request{
				Model:    fixture.Model,
				Payload:  downstream.Bytes(),
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitLabDuoModelInfo(row)},
			}, cliproxyexecutor.Options{SourceFormat: formats[fixture.Route], OriginalRequest: downstream.Bytes(),
				Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: capture.SessionID}})
			if len(loop.calls) != 2 {
				t.Fatalf("executor sent %d requests, NeoPi sent the mint and the model request", len(loop.calls))
			}
			assertGitLabDuoWireCall(t, "mint", loop.calls[0], fixture.Mint)
			assertGitLabDuoWireCall(t, "request", loop.calls[1], fixture.Request)
		})
	}
}

// The direct-access cases NeoPi's streamGitLabDuo handles, on every route: a
// refused (403) or failed mint, and a mint without its token or headers, end
// the request with NeoPi's message before any model request; caller headers
// (the account's configured ones) win over the minted set; and a second
// request reuses the minted token.
func TestGitLabDuoDirectAccessMatchesNeoPi(t *testing.T) {
	capture := loadGitLabDuoCapture(t)
	formats := map[string]sdktranslator.Format{
		"anthropic-messages": sdktranslator.FormatClaude,
		"openai-completions": sdktranslator.FormatOpenAI,
		"openai-responses":   sdktranslator.FormatOpenAIResponse,
	}
	authDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(authDir, ".claude-neopi-install-id"), []byte(capture.InstallID), 0o600); err != nil {
		t.Fatal(err)
	}
	minted, _ := json.Marshal(map[string]any{"token": capture.DirectAccessToken, "headers": capture.DirectAccessHeaders})
	answers := map[string]func(w http.ResponseWriter){
		"denied-403": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "duo disabled")
		},
		"failed-500": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "boom")
		},
		"missing-token": func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(map[string]any{"headers": capture.DirectAccessHeaders})
		},
		"missing-headers": func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(map[string]any{"token": capture.DirectAccessToken})
		},
	}
	bundled, err := registry.GitLabDuoBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range capture.Edges {
		t.Run(edge.Name, func(t *testing.T) {
			var row *registry.NeoPiModel
			for _, candidate := range bundled {
				if candidate.ID == edge.Model {
					row = candidate
				}
			}
			loop := newGitLabDuoLoopback(t)
			kind := edge.Name[strings.LastIndex(edge.Name, "/")+1:]
			loop.mint = answers[kind]
			if loop.mint == nil {
				loop.mint = func(w http.ResponseWriter) { _, _ = w.Write(minted) }
			}
			auth := &cliproxyauth.Auth{ID: "gitlab-duo-edge", Provider: gitlabduo.Provider, Attributes: map[string]string{},
				Metadata: map[string]any{"type": gitlabduo.Provider, "access_token": capture.APIKey}}
			if headers, ok := edge.Options["headers"].(map[string]any); ok {
				for name, value := range headers {
					auth.Attributes["header:"+name] = value.(string)
				}
			}
			var downstream bytes.Buffer
			if err := json.Compact(&downstream, edge.Downstream); err != nil {
				t.Fatal(err)
			}
			exec := NewGitLabDuoExecutor(gitlabduo.Provider, &config.Config{AuthDir: authDir})
			run := func() error {
				_, err := exec.Execute(loop.context(), auth, cliproxyexecutor.Request{
					Model:    edge.Model,
					Payload:  downstream.Bytes(),
					Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitLabDuoModelInfo(row)},
				}, cliproxyexecutor.Options{SourceFormat: formats[edge.Route], OriginalRequest: downstream.Bytes(),
					Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: capture.SessionID}})
				return err
			}
			paths := func() []string {
				var out []string
				for _, call := range loop.calls {
					out = append(out, call.Path)
				}
				return out
			}
			gitlabduo.ClearDirectAccessCache()
			if kind == "cache" {
				_ = run()
				if got := paths(); !reflect.DeepEqual(got, edge.First) {
					t.Fatalf("first request sent %v, NeoPi sent %v", got, edge.First)
				}
				loop.calls = nil
				_ = run()
				if got := paths(); !reflect.DeepEqual(got, edge.Reused) {
					t.Fatalf("second request sent %v, NeoPi sent %v", got, edge.Reused)
				}
				return
			}
			if kind == "version-too-old" {
				// The adopted version is process-wide: start from the pinned one
				// and put back whatever other tests left.
				t.Setenv("PI_AI_CLAUDE_CODE_VERSION", "")
				previous := anthropicnpi.SwapAdoptedVersionForTesting("")
				defer anthropicnpi.SwapAdoptedVersionForTesting(previous)
				answered := 0
				loop.model = func(w http.ResponseWriter) {
					answered++
					w.WriteHeader(http.StatusBadRequest)
					if answered == 1 {
						_, _ = io.WriteString(w, `{"type":"error","error":{"type":"claude_code_version_too_old","message":"version 2.1.999 or newer is required"}}`)
						return
					}
					_, _ = io.WriteString(w, "sentinel after adoption")
				}
			}
			err := run()
			if edge.ErrorMessage != nil {
				if err == nil {
					t.Fatalf("succeeded, NeoPi failed with %q", *edge.ErrorMessage)
				}
				// NeoPi's text for a model error names a local request dump.
				if kind != "version-too-old" && err.Error() != *edge.ErrorMessage {
					t.Fatalf("error = %v, NeoPi failed with %q", err, *edge.ErrorMessage)
				}
			}
			if len(loop.calls) != len(edge.Requests) {
				t.Fatalf("sent %d requests, NeoPi sent %d", len(loop.calls), len(edge.Requests))
			}
			for i := range edge.Requests {
				assertGitLabDuoWireCall(t, fmt.Sprintf("request %d", i), loop.calls[i], edge.Requests[i])
			}
		})
	}
}

// A personal access token added as an account key (NeoPi's GITLAB_TOKEN) is
// the GitLab token the direct-access mint carries, and the request then goes
// to GitLab's proxy like a sign-in's.
func TestGitLabDuoServesPersonalAccessToken(t *testing.T) {
	loop := newGitLabDuoLoopback(t)
	loop.mint = func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"token":"direct-from-pat","headers":{"x-gitlab-realm":"saas"}}`)
	}
	bundled, err := registry.GitLabDuoBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	var row *registry.NeoPiModel
	for _, candidate := range bundled {
		if candidate.ID == "duo-chat-gpt-5-1" {
			row = candidate
		}
	}
	gitlabduo.ClearDirectAccessCache()
	auth := &cliproxyauth.Auth{ID: "gitlab-pat", Provider: "openai-compatibility",
		Attributes: map[string]string{"api_key": "glpat-personal", "base_url": gitlabduo.BaseURL, "compat_name": "gitlab-duo"}}
	payload := []byte(`{"model":"duo-chat-gpt-5-1","messages":[{"role":"user","content":"hi"}]}`)
	_, _ = NewGitLabDuoExecutor("gitlab-duo", &config.Config{}).Execute(loop.context(), auth, cliproxyexecutor.Request{Model: row.ID, Payload: payload,
		Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitLabDuoModelInfo(row)}},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
	if len(loop.calls) != 2 {
		t.Fatalf("sent %d requests, want the mint and the model request", len(loop.calls))
	}
	if got := loop.calls[0].Headers["authorization"]; got != "Bearer glpat-personal" {
		t.Fatalf("mint Authorization = %q, want the personal access token", got)
	}
	if got := loop.calls[1].Headers["authorization"]; got != "Bearer direct-from-pat" || loop.calls[1].Path != "/ai/v1/proxy/openai/v1/chat/completions" {
		t.Fatalf("model request %s with Authorization %q, want GitLab's proxy with the minted token", loop.calls[1].Path, got)
	}
}

// Adopting a required Claude Code version starts the request's retry budget
// over (anthropic.ts sets providerRetryAttempt = 0): ten transient failures,
// the version rejection, then one more transient failure still retry, on the
// token minted once.
func TestGitLabDuoVersionAdoptionRestartsRetryBudget(t *testing.T) {
	bundled, err := registry.GitLabDuoBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	var row *registry.NeoPiModel
	for _, candidate := range bundled {
		if candidate.API == "anthropic-messages" {
			row = candidate
			break
		}
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			t.Setenv("PI_AI_CLAUDE_CODE_VERSION", "")
			previous := anthropicnpi.SwapAdoptedVersionForTesting("")
			defer anthropicnpi.SwapAdoptedVersionForTesting(previous)
			gitlabduo.ClearDirectAccessCache()
			defer gitlabduo.ClearDirectAccessCache()
			loop := newGitLabDuoLoopback(t)
			loop.mint = func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"token":"direct","headers":{}}`) }
			answered := 0
			loop.model = func(w http.ResponseWriter) {
				answered++
				switch {
				case answered == 11:
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"type":"error","error":{"type":"claude_code_version_too_old","message":"version 2.1.999 or newer is required"}}`)
				case answered <= 12:
					w.WriteHeader(http.StatusInternalServerError)
				default:
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, "sentinel")
				}
			}
			auth := &cliproxyauth.Auth{ID: "gitlab-duo-retry", Provider: gitlabduo.Provider, Metadata: map[string]any{"type": gitlabduo.Provider, "access_token": "gitlab-oauth"}}
			payload := []byte(`{"model":"` + row.ID + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
			req := cliproxyexecutor.Request{Model: row.ID, Payload: payload, Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitLabDuoModelInfo(row)}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}
			exec := NewGitLabDuoExecutor(gitlabduo.Provider, &config.Config{AuthDir: t.TempDir()})
			a, err := exec.attempt(loop.context(), auth, req)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := exec.wire(a, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			messages := wire.(*ClaudeExecutor)
			messages.wait = func(context.Context, time.Duration) error { return nil }
			messages.random = func() float64 { return 0 }
			if stream {
				_, err = messages.ExecuteStream(loop.context(), auth, req, opts)
			} else {
				_, err = messages.Execute(loop.context(), auth, req, opts)
			}
			if answered != 13 || err == nil || !strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("model requests = %d, error = %v; NeoPi retries afresh after adopting the version and reaches request 13", answered, err)
			}
			if mints := len(loop.calls) - answered; mints != 1 {
				t.Fatalf("minted %d times, want 1", mints)
			}
		})
	}
}
