package executor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type responsesCapture struct {
	Credentials map[string]string `json:"credentials"`
	SessionID   string            `json:"sessionId"`
	Response    string            `json:"response"`
	Fixtures    []struct {
		Name         string `json:"name"`
		RequestedURL string `json:"requestedURL"`
		Scenario     struct {
			Provider   string          `json:"provider"`
			Model      string          `json:"model"`
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

// bearerFor is the key NeoPi's transport puts on the wire for a stored credential:
// the muse-code credential is the JSON bearer of the device token and the key.
func bearerFor(provider, credential string) string {
	if provider == registry.ResponsesMuseCode {
		var wrapped struct {
			APIKey string `json:"apiKey"`
		}
		_ = json.Unmarshal([]byte(credential), &wrapped)
		return wrapped.APIKey
	}
	return credential
}

// TestNeoPiResponsesMatchesNeoPiSource replays the scenarios captured from
// NeoPi's own streamOpenAIResponses (testdata/capture_responses_npi.ts):
// method, URL, headers and body bytes of every downstream turn for Meta, Muse
// Code, Sakana and Abliteration.
func TestNeoPiResponsesMatchesNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/responses_npi_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture responsesCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) < 114 {
		t.Fatalf("only %d fixtures; expected expanded native-input coverage", len(capture.Fixtures))
	}
	seen := make(map[string]bool)
	for _, fixture := range capture.Fixtures {
		seen[fixture.Name] = true
		if len(fixture.Requests) != 1 {
			t.Fatalf("%s captured %d requests; expected one loopback request", fixture.Name, len(fixture.Requests))
		}
		body := fixture.Requests[0].RawBody
		switch {
		case strings.HasSuffix(fixture.Name, "-explicit-encrypted-include"):
			if got := gjson.Get(body, "include").Raw; got != `["reasoning.encrypted_content"]` {
				t.Errorf("%s explicit include = %s", fixture.Name, got)
			}
		case strings.HasSuffix(fixture.Name, "-summary-none"):
			if got := gjson.Get(body, "reasoning.summary"); got.Exists() {
				t.Errorf("%s summary = %s; want omitted after server normalization", fixture.Name, got.Raw)
			}
		case strings.HasSuffix(fixture.Name, "-image-original"):
			if gjson.GetBytes(fixture.Scenario.Downstream, "input.0.type").String() != "message" || gjson.Get(body, "input.0.type").String() != "message" {
				t.Errorf("%s must preserve explicitly typed native user message", fixture.Name)
			}
			if got := gjson.GetBytes(fixture.Scenario.Downstream, "input.0.content.1.detail").String(); got != "original" {
				t.Errorf("%s downstream detail = %q; want original", fixture.Name, got)
			}
			if got := gjson.Get(body, "input.0.content.1.detail").String(); got != "original" {
				t.Errorf("%s native image detail = %q; want original", fixture.Name, got)
			}
		case strings.HasSuffix(fixture.Name, "-reconstructed-assistant-history"):
			if gjson.Get(body, "input.1.id").Exists() || gjson.Get(body, "input.2.id").Exists() {
				t.Errorf("%s reconstructed history unexpectedly preserves native item IDs", fixture.Name)
			}
			if gjson.Get(body, "input.1.status").String() != "completed" || gjson.Get(body, "input.1.content.0.annotations").Raw != "[]" ||
				gjson.Get(body, "input.2.call_id").String() != "call_1" || gjson.Get(body, "input.3.call_id").String() != "call_1" {
				t.Errorf("%s incomplete reconstructed message/tool history: %s", fixture.Name, body)
			}
		}
	}
	for _, provider := range []string{"meta", "muse-code", "sakana", "abliteration"} {
		for _, suffix := range []string{"explicit-encrypted-include", "image-original", "summary-none", "reconstructed-assistant-history"} {
			if name := provider + "-" + suffix; !seen[name] {
				t.Errorf("missing source capture %s", name)
			}
		}
	}
	framing := map[string]bool{"host": true, "content-length": true, "accept-encoding": true, "connection": true}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			provider := fixture.Scenario.Provider
			model := registry.ResponsesNeoPiModel(provider, fixture.Scenario.Model, nil)
			if model == nil {
				t.Fatalf("model %s/%s missing from the catalog", provider, fixture.Scenario.Model)
			}
			ctx, opts := openaiwire.FromResponses(fixture.Scenario.Downstream, model)
			ctx.ReconstructAssistantHistory = strings.HasSuffix(fixture.Name, "-reconstructed-assistant-history")
			opts.SessionID = capture.SessionID
			request := openaiwire.ShapeResponses(model, ctx, opts, bearerFor(provider, capture.Credentials[provider]))
			(&NeoPiResponsesExecutor{identifier: "meta"}).applyTransportHeaders(request.Header, nil, model, nil)
			want := fixture.Requests[0]
			if request.URL != fixture.RequestedURL {
				t.Errorf("URL %s; source %s", request.URL, fixture.RequestedURL)
			}
			if string(request.Body) != want.RawBody {
				t.Errorf("body\ngot    %s\nsource %s", request.Body, want.RawBody)
			}
			got := map[string]string{}
			for name, values := range request.Header {
				got[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			source := map[string]string{}
			for name, value := range want.Headers {
				if !framing[name] {
					source[name] = value
				}
			}
			if !reflect.DeepEqual(got, source) {
				t.Errorf("headers\ngot    %v\nsource %v", got, source)
			}
			if want.Method != http.MethodPost {
				t.Errorf("source method %s", want.Method)
			}
		})
	}
}

// TestNeoPiResponsesExecuteMatchesNeoPiSource runs the same matrix through
// ExecuteStream against a loopback server, so translation, thinking, payload
// finalization and the transport are part of what is compared with NeoPi.
func TestNeoPiResponsesExecuteMatchesNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/responses_npi_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture responsesCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	framing := map[string]bool{"host": true, "content-length": true, "accept-encoding": true, "connection": true}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			provider := fixture.Scenario.Provider
			var gotMethod, gotPath, gotBody string
			gotHeaders := http.Header{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotMethod, gotPath, gotBody, gotHeaders = r.Method, r.URL.Path, string(body), r.Header.Clone()
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, capture.Response)
			}))
			defer server.Close()
			model := registry.ResponsesNeoPiModel(provider, fixture.Scenario.Model, nil)
			if model == nil {
				t.Fatalf("model %s/%s missing", provider, fixture.Scenario.Model)
			}
			model.BaseURL = server.URL + "/v1"
			info := &registry.ModelInfo{ID: model.ID, NeoPi: model}
			if model.Reasoning && model.Thinking != nil {
				info.Thinking = &registry.ThinkingSupport{Levels: model.Thinking.Efforts, ZeroAllowed: true}
			}
			payload, errSet := sjson.SetBytes(fixture.Scenario.Downstream, "prompt_cache_key", capture.SessionID)
			if errSet != nil {
				t.Fatal(errSet)
			}
			auth := &cliproxyauth.Auth{ID: "parity", Provider: "responses-parity", Attributes: map[string]string{"api_key": bearerFor(provider, capture.Credentials[provider]), "base_url": server.URL + "/v1"}}
			exec := NewNeoPiResponsesExecutor("responses-parity", &config.Config{})
			req := cliproxyexecutor.Request{Model: fixture.Scenario.Model, Payload: payload, Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": info}}
			result, errStream := exec.ExecuteStream(t.Context(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if errStream != nil {
				t.Fatal(errStream)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
			}
			want := fixture.Requests[0]
			if gotMethod != want.Method || gotPath != want.Path {
				t.Errorf("sent %s %s; source %s %s", gotMethod, gotPath, want.Method, want.Path)
			}
			if gotBody != want.RawBody {
				t.Errorf("body\ngot    %s\nsource %s", gotBody, want.RawBody)
			}
			got := map[string]string{}
			for name, values := range gotHeaders {
				if lower := strings.ToLower(name); !framing[lower] {
					got[lower] = strings.Join(values, ", ")
				}
			}
			source := map[string]string{}
			for name, value := range want.Headers {
				if !framing[name] {
					source[name] = value
				}
			}
			if !reflect.DeepEqual(got, source) {
				t.Errorf("headers\ngot    %v\nsource %v", got, source)
			}
		})
	}
}
