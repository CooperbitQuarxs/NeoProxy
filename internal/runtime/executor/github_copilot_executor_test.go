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
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// githubCopilotCapture is testdata/neopi_github_copilot/<api>.json.gz:
// NeoPi's OWN streamSimple over every bundled Copilot model on one API,
// against a loopback server, beside the downstream request a client sends
// NeoProxy for each scenario.
type githubCopilotCapture struct {
	Wire      string `json:"wire"`
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	// Listing and ModelsDev are what NeoPi's LiteLLM manager discovered its
	// rows from (/model_group/info and the models.dev catalog).
	Listing   string `json:"listing"`
	ModelsDev string `json:"modelsDev"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		Downstream   json.RawMessage   `json:"downstream"`
		RequestedURL string            `json:"requestedURL"`
		Method       string            `json:"method"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
	} `json:"fixtures"`
}

// The Copilot executor sends, for every bundled model on each API, the
// request NeoPi's own transport sends for the same scenario: method, path,
// body bytes and headers.
func TestGitHubCopilotReplaysNeoPiRequests(t *testing.T) {
	formats := map[string]sdktranslator.Format{
		"anthropic-messages": sdktranslator.FormatClaude,
		"openai-completions": sdktranslator.FormatOpenAI,
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
	bundled, err := registry.GitHubCopilotBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, wire := range []string{"anthropic-messages", "openai-completions", "openai-responses"} {
		capture := loadGitHubCopilotCapture(t, "testdata/neopi_github_copilot/"+wire+".json.gz")
		t.Run(wire, func(t *testing.T) {
			exec := NewGitHubCopilotExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: "github-copilot-replay", Provider: "github-copilot", Metadata: map[string]any{"type": "github-copilot", "access_token": capture.APIKey}}
			for _, fixture := range capture.Fixtures {
				t.Run(fixture.Name, func(t *testing.T) {
					var row *registry.NeoPiModel
					for _, candidate := range bundled {
						if candidate.ID == fixture.Model {
							row = candidate.Clone()
						}
					}
					if row == nil {
						t.Fatalf("no bundled row %s", fixture.Model)
					}
					requested, err := url.Parse(fixture.RequestedURL)
					if err != nil {
						t.Fatal(err)
					}
					row.BaseURL = server.URL
					var downstream bytes.Buffer
					if err = json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					sent.body = nil
					_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
						Model:    fixture.Model,
						Payload:  downstream.Bytes(),
						Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitHubCopilotModelInfo(row)},
					}, cliproxyexecutor.Options{SourceFormat: formats[wire], OriginalRequest: downstream.Bytes(),
						Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: capture.SessionID}})
					if sent.body == nil {
						t.Fatal("executor sent no request")
					}
					if sent.method != fixture.Method || sent.path != requested.RequestURI() {
						t.Errorf("request = %s %s, want %s %s", sent.method, sent.path, fixture.Method, requested.RequestURI())
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
					if !reflect.DeepEqual(got, want) {
						t.Errorf("headers:\n got %v\nwant %v", got, want)
					}
				})
			}
		})
	}
}

func loadGitHubCopilotCapture(t *testing.T, path string) githubCopilotCapture {
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
	var capture githubCopilotCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatalf("%s holds no fixtures", path)
	}
	return capture
}

// NeoPi's wrapFetchForCopilotFallback on every Copilot API: a denied chat
// identity (403, or 400 model_not_supported) is reissued once as the CLI and
// the working identity is learned for the credential; a learned CLI identity
// that is denied is reissued once as chat and relearned; a failed retry
// forgets it; any other 400 and a pinned identity are never reissued.
func TestGitHubCopilotIdentityFallback(t *testing.T) {
	const chat, cli = "copilot-chat", "copilot-developer-cli"
	denied403 := func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) }
	denied400 := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"model_not_supported","message":"no"}}`)
	}
	other400 := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_request_body","message":"no"}}`)
	}
	type step struct {
		deny func(w http.ResponseWriter)
		// denies is the identity the upstream refuses; "*" refuses both.
		denies string
		want   []string
	}
	for _, tc := range []struct {
		name   string
		pinned string
		steps  []step
	}{
		{"chat 403 learns cli", "", []step{{denied403, chat, []string{chat, cli}}, {denied403, chat, []string{cli}}}},
		{"chat model_not_supported learns cli", "", []step{{denied400, chat, []string{chat, cli}}, {denied400, chat, []string{cli}}}},
		{"other 400 is not a denial", "", []step{{other400, chat, []string{chat}}, {other400, chat, []string{chat}}}},
		{"stale cli relearns chat", "", []step{{denied403, chat, []string{chat, cli}}, {denied403, cli, []string{cli, chat}}, {denied403, cli, []string{chat}}}},
		{"failed retry forgets", "", []step{{denied403, chat, []string{chat, cli}}, {denied403, "*", []string{cli, chat}}, {denied403, "*", []string{chat, cli}}}},
		{"pinned identity is kept", chat, []step{{denied403, chat, []string{chat}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COPILOT_INTEGRATION_ID", tc.pinned)
			var current step
			var sent []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := r.Header.Get("Copilot-Integration-Id")
				sent = append(sent, id)
				if current.denies == "*" || current.denies == id {
					current.deny(w)
					return
				}
				// A complete stream on each API, so no executor retry adds requests.
				w.Header().Set("Content-Type", "text/event-stream")
				switch {
				case strings.HasSuffix(r.URL.Path, "/messages"):
					_, _ = io.WriteString(w, githubCopilotMessagesStream)
				case strings.HasSuffix(r.URL.Path, "/responses"):
					_, _ = io.WriteString(w, githubCopilotResponsesStream)
				default:
					_, _ = io.WriteString(w, githubCopilotCompletionsStream)
				}
			}))
			defer server.Close()
			for _, wire := range []struct {
				api, payload string
				format       sdktranslator.Format
			}{
				{"anthropic-messages", `{"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, sdktranslator.FormatClaude},
				{"openai-completions", `{"messages":[{"role":"user","content":"hi"}]}`, sdktranslator.FormatOpenAI},
				{"openai-responses", `{"input":"hi"}`, sdktranslator.FormatOpenAIResponse},
			} {
				t.Run(wire.api, func(t *testing.T) {
					row := githubCopilotRowOnAPI(t, wire.api)
					row.BaseURL = server.URL
					exec := NewGitHubCopilotExecutor(&config.Config{})
					// A token per case and API keeps each learned identity its own.
					auth := &cliproxyauth.Auth{ID: "copilot-fallback", Provider: "github-copilot",
						Metadata: map[string]any{"type": "github-copilot", "access_token": "ghu_" + strings.ReplaceAll(tc.name, " ", "_") + "_" + wire.api}}
					payload := []byte(`{"model":"` + row.ID + `",` + wire.payload[1:])
					for i, s := range tc.steps {
						current, sent = s, nil
						_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: row.ID, Payload: payload,
							Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitHubCopilotModelInfo(row)}},
							cliproxyexecutor.Options{SourceFormat: wire.format, OriginalRequest: payload})
						if !reflect.DeepEqual(sent, s.want) {
							t.Fatalf("request %d sent identities %v, want %v", i+1, sent, s.want)
						}
					}
				})
			}
		})
	}
}

// Headers the account configures are NeoPi's caller headers on every
// Copilot API: they reach the upstream over the model's own headers (a
// configured X-GitHub-Api-Version included), and their X-Initiator decides
// the turn's initiator and interaction type over the inferred one, as
// NeoPi's own streamSimple sends with options.headers set.
func TestGitHubCopilotConfiguredHeaders(t *testing.T) {
	t.Setenv("COPILOT_INTEGRATION_ID", "")
	var sent http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Clone()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	for _, wire := range []struct {
		api, payload string
		format       sdktranslator.Format
	}{
		{"anthropic-messages", `{"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, sdktranslator.FormatClaude},
		{"openai-completions", `{"messages":[{"role":"user","content":"hi"}]}`, sdktranslator.FormatOpenAI},
		{"openai-responses", `{"input":"hi"}`, sdktranslator.FormatOpenAIResponse},
	} {
		t.Run(wire.api, func(t *testing.T) {
			sent = nil
			row := githubCopilotRowOnAPI(t, wire.api)
			row.BaseURL = server.URL
			auth := &cliproxyauth.Auth{ID: "copilot-configured", Provider: "github-copilot",
				Metadata: map[string]any{"type": "github-copilot", "access_token": "ghu_configured_" + wire.api},
				Attributes: map[string]string{"header:X-Initiator": "agent", "header:X-Configured": "present", "header:X-GitHub-Api-Version": "2099-01-01",
					"header:X-Claude-Code-Session-Id": "caller-session"}}
			payload := []byte(`{"model":"` + row.ID + `",` + wire.payload[1:])
			_, _ = NewGitHubCopilotExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{Model: row.ID, Payload: payload,
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitHubCopilotModelInfo(row)}},
				cliproxyexecutor.Options{SourceFormat: wire.format, OriginalRequest: payload,
					Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "request-session"}})
			if sent == nil {
				t.Fatal("no upstream request")
			}
			got := []string{sent.Get("X-Initiator"), sent.Get("X-Interaction-Type"), sent.Get("X-Configured"), sent.Get("X-GitHub-Api-Version")}
			if want := []string{"agent", "conversation-agent", "present", "2099-01-01"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("X-Initiator, X-Interaction-Type, X-Configured, X-GitHub-Api-Version = %q, want %q", got, want)
			}
			// Only the Anthropic protocol carries the conversation id, and
			// NeoPi's applyInferenceHeaders sets the request's over the caller's.
			session := sent.Get("X-Claude-Code-Session-Id")
			if wire.api == "anthropic-messages" && session != "request-session" {
				t.Fatalf("X-Claude-Code-Session-Id = %q, want the request's request-session", session)
			}
			if wire.api != "anthropic-messages" && session != "caller-session" {
				t.Fatalf("X-Claude-Code-Session-Id = %q, want the configured caller-session", session)
			}
		})
	}
}

// A configured Copilot-Integration-Id that forwards a client header is
// resolved against the client's request before the identity is chosen: it
// pins the identity, so a denial is never reissued under the other one, as
// NeoPi sends with that value in options.headers.
func TestGitHubCopilotForwardedIdentityPin(t *testing.T) {
	t.Setenv("COPILOT_INTEGRATION_ID", "")
	var sent []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = append(sent, r.Header.Get("Copilot-Integration-Id"))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	for _, wire := range []struct {
		api, payload string
		format       sdktranslator.Format
	}{
		{"anthropic-messages", `{"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, sdktranslator.FormatClaude},
		{"openai-completions", `{"messages":[{"role":"user","content":"hi"}]}`, sdktranslator.FormatOpenAI},
		{"openai-responses", `{"input":"hi"}`, sdktranslator.FormatOpenAIResponse},
	} {
		t.Run(wire.api, func(t *testing.T) {
			sent = nil
			row := githubCopilotRowOnAPI(t, wire.api)
			row.BaseURL = server.URL
			auth := &cliproxyauth.Auth{ID: "copilot-forwarded-pin", Provider: "github-copilot",
				Metadata:   map[string]any{"type": "github-copilot", "access_token": "ghu_forwarded_pin_" + wire.api},
				Attributes: map[string]string{"header:Copilot-Integration-Id": "$X-Client-Integration"}}
			payload := []byte(`{"model":"` + row.ID + `",` + wire.payload[1:])
			_, _ = NewGitHubCopilotExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{Model: row.ID, Payload: payload,
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.GitHubCopilotModelInfo(row)}},
				cliproxyexecutor.Options{SourceFormat: wire.format, OriginalRequest: payload,
					Headers: http.Header{"X-Client-Integration": {"copilot-chat"}}})
			if want := []string{"copilot-chat"}; !reflect.DeepEqual(sent, want) {
				t.Fatalf("sent identities %v, want %v", sent, want)
			}
		})
	}
}

func githubCopilotRowOnAPI(t *testing.T, api string) *registry.NeoPiModel {
	t.Helper()
	bundled, err := registry.GitHubCopilotBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range bundled {
		if row.API == api {
			return row.Clone()
		}
	}
	t.Fatalf("no bundled Copilot model on %s", api)
	return nil
}

// Complete upstream streams on each Copilot API.
const (
	githubCopilotMessagesStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	githubCopilotResponsesStream = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"
	githubCopilotCompletionsStream = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
)

// Copilot discovery sends what NeoPi's own model manager sent in each
// captured scenario (internal/registry/testdata/
// capture_github_copilot_discovery.ts): the plan endpoint probe on the
// personal host only, then GET /models on the endpoint it advertised (or the
// account's own host), with the same headers; a rejected or unreadable
// listing fails and an empty one is short-lived. Every host is answered in
// process: nothing is dialed.
func TestFetchGitHubCopilotModelsMatchesNeoPiRequests(t *testing.T) {
	raw, err := os.ReadFile("../../registry/testdata/github_copilot_discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	type reply struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	var capture struct {
		Results []struct {
			Scenario string `json:"scenario"`
			APIKey   string `json:"apiKey"`
			BaseURL  string `json:"baseUrl"`
			User     *reply `json:"user"`
			Models   reply  `json:"models"`
			Requests []struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"requests"`
			Source   string            `json:"source"`
			Resolved []json.RawMessage `json:"resolved"`
		} `json:"results"`
	}
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	for _, result := range capture.Results {
		if result.BaseURL != "" {
			// A custom provider baseUrl is NeoPi configuration NeoProxy's
			// Copilot accounts do not carry.
			continue
		}
		t.Run(result.Scenario, func(t *testing.T) {
			// NeoPi's apiKey is the raw token or its JSON envelope.
			metadata := map[string]any{"type": "github-copilot", "access_token": result.APIKey}
			var envelope struct{ Token, EnterpriseURL, APIEndpoint string }
			if json.Unmarshal([]byte(result.APIKey), &envelope) == nil && envelope.Token != "" {
				metadata = map[string]any{"type": "github-copilot", "access_token": envelope.Token, "enterprise_url": envelope.EnterpriseURL, "api_endpoint": envelope.APIEndpoint}
			}
			type sentRequest struct {
				Method, URL string
				Headers     map[string]string
			}
			var sent []sentRequest
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				headers := map[string]string{}
				for name := range req.Header {
					headers[strings.ToLower(name)] = req.Header.Get(name)
				}
				sent = append(sent, sentRequest{Method: req.Method, URL: req.URL.String(), Headers: headers})
				answer := result.Models
				if req.URL.Path == "/copilot_internal/user" {
					answer = reply{Status: http.StatusNotFound, Body: "{}"}
					if result.User != nil {
						answer = *result.User
					}
				}
				return &http.Response{StatusCode: answer.Status, Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(answer.Body)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			models, err := FetchGitHubCopilotModels(ctx, &config.Config{}, &cliproxyauth.Auth{ID: "copilot-discovery", Provider: "github-copilot", Metadata: metadata})
			var want []sentRequest
			for _, request := range result.Requests {
				want = append(want, sentRequest{Method: request.Method, URL: request.URL, Headers: request.Headers})
			}
			if !reflect.DeepEqual(sent, want) {
				t.Errorf("requests\n port %+v\nNeoPi %+v", sent, want)
			}
			if empty := (*registry.NeoPiEmptyListing)(nil); errors.As(err, &empty) {
				models, err = empty.Models, nil
			}
			switch {
			case result.Source == "bundled":
				if err == nil {
					t.Errorf("NeoPi kept the bundled roster; port resolved %d models", len(models))
				}
			case err != nil:
				t.Errorf("port failed a discovery NeoPi completed: %v", err)
			case len(models) != len(result.Resolved):
				t.Errorf("port resolved %d models, NeoPi %d", len(models), len(result.Resolved))
			}
		})
	}
}
