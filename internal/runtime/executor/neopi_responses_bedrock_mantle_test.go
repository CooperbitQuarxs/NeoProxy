package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/sjson"
)

// setMantleEnv isolates NeoPi's AWS environment inputs: region (empty selects
// us-east-1), env bearer, and a shared config file that does not exist.
func setMantleEnv(t *testing.T, region, bearer string) {
	t.Helper()
	for _, name := range []string{"AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_SDK_LOAD_CONFIG"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_REGION", region)
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", bearer)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
}

// mantleAccount is a Bedrock Mantle key as the dashboard adds it: an
// openai-compatibility entry at the catalog's `{region}` address.
func mantleAccount(key string) *cliproxyauth.Auth {
	attributes := map[string]string{"base_url": registry.ResponsesProviderBaseURL(registry.ResponsesBedrockMantle), "compat_name": "bedrock-mantle"}
	if key != "" {
		attributes["api_key"] = key
	}
	return &cliproxyauth.Auth{ID: "mantle", Provider: "bedrock-mantle", Attributes: attributes}
}

type mantleSent struct {
	method, url, body string
	headers           map[string]string
}

// mantleTransport answers every request with reply and records what was sent.
func mantleTransport(sent *[]mantleSent, contentType, reply string) context.Context {
	return context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var body []byte
		if req.Body != nil {
			body, _ = io.ReadAll(req.Body)
		}
		headers := map[string]string{}
		for name, values := range req.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		*sent = append(*sent, mantleSent{method: req.Method, url: req.URL.String(), body: string(body), headers: headers})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(reply)), Request: req}, nil
	}))
}

// withoutFraming drops the headers the transport, not the provider, owns.
func withoutFraming(headers map[string]string) map[string]string {
	out := map[string]string{}
	for name, value := range headers {
		switch name {
		case "host", "content-length", "accept-encoding", "connection":
		default:
			out[name] = value
		}
	}
	return out
}

// A Bedrock Mantle account sends what NeoPi's bedrock-mantle transport sends
// with no region configured: us-east-1's endpoint, the Bedrock API key as the
// bearer (the account's, else AWS_BEARER_TOKEN_BEDROCK), NeoPi's body.
func TestBedrockMantleAccountMatchesNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/responses_npi_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture responsesCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	var fixture *struct {
		Name         string `json:"name"`
		RequestedURL string `json:"requestedURL"`
		Scenario     struct {
			Provider        string          `json:"provider"`
			Model           string          `json:"model"`
			Downstream      json.RawMessage `json:"downstream"`
			ProviderOptions struct {
				Region string `json:"region"`
			} `json:"providerOptions"`
		} `json:"scenario"`
		Requests []struct {
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			RawBody string            `json:"rawBody"`
		} `json:"requests"`
	}
	for i := range capture.Fixtures {
		if capture.Fixtures[i].Name == "bedrock-mantle-default-region" {
			fixture = &capture.Fixtures[i]
		}
	}
	if fixture == nil {
		t.Fatal("missing bedrock-mantle-default-region capture")
	}
	credential := capture.Credentials[registry.ResponsesBedrockMantle]
	payload, err := sjson.SetBytes(fixture.Scenario.Downstream, "prompt_cache_key", capture.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, key, envBearer string }{
		{name: "account key", key: credential},
		{name: "AWS_BEARER_TOKEN_BEDROCK", envBearer: credential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setMantleEnv(t, "", tc.envBearer)
			var sent []mantleSent
			ctx := mantleTransport(&sent, "text/event-stream", capture.Response)
			exec := NewNeoPiResponsesExecutor("bedrock-mantle", &config.Config{})
			result, errStream := exec.ExecuteStream(ctx, mantleAccount(tc.key), cliproxyexecutor.Request{Model: fixture.Scenario.Model, Payload: payload},
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
			if errStream != nil {
				t.Fatal(errStream)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
			}
			if len(sent) != 1 {
				t.Fatalf("sent %d requests, want 1", len(sent))
			}
			want := fixture.Requests[0]
			if sent[0].method != want.Method || sent[0].url != fixture.RequestedURL {
				t.Errorf("sent %s %s; source %s %s", sent[0].method, sent[0].url, want.Method, fixture.RequestedURL)
			}
			if sent[0].body != want.RawBody {
				t.Errorf("body\ngot    %s\nsource %s", sent[0].body, want.RawBody)
			}
			if got, source := withoutFraming(sent[0].headers), withoutFraming(want.Headers); !reflect.DeepEqual(got, source) {
				t.Errorf("headers\ngot    %v\nsource %v", got, source)
			}
		})
	}
}

// Without any Bedrock API key the account cannot be served until SigV4 is
// supported; the request is refused before anything is sent.
func TestBedrockMantleWithoutBearerIsRefused(t *testing.T) {
	setMantleEnv(t, "", "")
	var sent []mantleSent
	ctx := mantleTransport(&sent, "text/event-stream", "")
	exec := NewNeoPiResponsesExecutor("bedrock-mantle", &config.Config{})
	_, err := exec.ExecuteStream(ctx, mantleAccount(""), cliproxyexecutor.Request{Model: "openai.gpt-5.6-terra", Payload: []byte(`{"model":"openai.gpt-5.6-terra","input":"hi"}`)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	var status statusErr
	if err == nil || !errors.As(err, &status) || status.code != http.StatusUnauthorized || !strings.Contains(status.msg, "AWS_BEARER_TOKEN_BEDROCK") {
		t.Fatalf("err = %v; want a 401 naming AWS_BEARER_TOKEN_BEDROCK", err)
	}
	if len(sent) != 0 {
		t.Fatalf("sent %d requests without a bearer", len(sent))
	}
}

// Discovery is NeoPi's bedrock-mantle model manager: with a bearer, GET the
// region's /v1/models (not the inference /openai/v1) with fetch's headers, and
// the authoritative roster mapped over the seed rows; without one, no request
// and the seed rows.
func TestBedrockMantleDiscoveryMatchesNeoPi(t *testing.T) {
	raw, err := os.ReadFile("testdata/responses_npi_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var profiles struct {
		Discovery []struct {
			Provider  string            `json:"provider"`
			Name      string            `json:"name"`
			RosterRaw []json.RawMessage `json:"rosterRaw"`
			Requests  []struct {
				Method  string            `json:"method"`
				Path    string            `json:"path"`
				Headers map[string]string `json:"headers"`
			} `json:"requests"`
			Models []registry.NeoPiModel `json:"models"`
		} `json:"discovery"`
	}
	if err = json.Unmarshal(raw, &profiles); err != nil {
		t.Fatal(err)
	}
	scenarios := map[string]int{}
	for i, scenario := range profiles.Discovery {
		if scenario.Provider == registry.ResponsesBedrockMantle {
			scenarios[scenario.Name] = i
		}
	}
	for _, tc := range []struct{ name, scenario, key, envBearer string }{
		{name: "account key", scenario: "new-and-unknown", key: "discovery-key"},
		{name: "AWS_BEARER_TOKEN_BEDROCK", scenario: "new-and-unknown", envBearer: "discovery-key"},
		{name: "no bearer", scenario: "iam-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, ok := scenarios[tc.scenario]
			if !ok {
				t.Fatalf("missing bedrock-mantle/%s discovery capture", tc.scenario)
			}
			scenario := profiles.Discovery[index]
			setMantleEnv(t, "eu-west-1", tc.envBearer)
			roster, _ := json.Marshal(map[string]any{"object": "list", "data": scenario.RosterRaw})
			var sent []mantleSent
			ctx := mantleTransport(&sent, "application/json", string(roster))
			models, errFetch := FetchResponsesModels(ctx, &config.Config{}, mantleAccount(tc.key))
			if errFetch != nil {
				t.Fatal(errFetch)
			}
			if len(sent) != len(scenario.Requests) {
				t.Fatalf("sent %d requests; NeoPi sent %d", len(sent), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				if wantURL := "https://bedrock-mantle.eu-west-1.api.aws" + want.Path; sent[i].method != want.Method || sent[i].url != wantURL {
					t.Errorf("sent %s %s; want %s %s", sent[i].method, sent[i].url, want.Method, wantURL)
				}
				if got, source := withoutFraming(sent[i].headers), withoutFraming(want.Headers); !reflect.DeepEqual(got, source) {
					t.Errorf("headers\ngot    %v\nsource %v", got, source)
				}
			}
			if len(models) != len(scenario.Models) {
				t.Fatalf("listed %d models; NeoPi %d", len(models), len(scenario.Models))
			}
			for i := range models {
				got, _ := json.Marshal(models[i].NeoPi)
				want, _ := json.Marshal(scenario.Models[i])
				if string(got) != string(want) {
					t.Errorf("model %d\ngot  %s\nwant %s", i, got, want)
				}
				if models[i].OwnedBy != "bedrock-mantle" {
					t.Errorf("model %s owned by %q", models[i].ID, models[i].OwnedBy)
				}
			}
		})
	}
}
