package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

// devinCapture is testdata/devin_npi_capture.json: NeoPi's OWN streamDevin
// (capture_devin_npi.ts) run against a loopback Cascade fake.
type devinCapture struct {
	APIKey    string                         `json:"apiKey"`
	SessionID string                         `json:"sessionId"`
	Models    map[string]registry.NeoPiModel `json:"models"`
	Fixtures  []struct {
		Name       string          `json:"name"`
		Model      string          `json:"model"`
		Assign     string          `json:"assign"`
		Downstream json.RawMessage `json:"downstream"`
		Reply      json.RawMessage `json:"reply"`
		Requests   []struct {
			Path       string            `json:"path"`
			Headers    map[string]string `json:"headers"`
			Body       string            `json:"body"`
			FrameFlags *byte             `json:"frameFlags"`
			Proto      string            `json:"proto"`
		} `json:"requests"`
	} `json:"fixtures"`
}

type devinSentRequest struct {
	path    string
	headers map[string]string
	body    []byte
}

// TestDevinExecutorReplaysNeoPiRequests sends each captured conversation as
// the Claude request a client gives NeoProxy and requires the RPC sequence,
// headers and protobuf bodies NeoPi sent, answered the way the capture's fake
// answered.
func TestDevinExecutorReplaysNeoPiRequests(t *testing.T) {
	data, err := os.ReadFile("testdata/devin_npi_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture devinCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	var (
		mu     sync.Mutex
		sent   []devinSentRequest
		assign string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{}
		for name, values := range r.Header {
			if name != "Content-Length" {
				headers[strings.ToLower(name)] = strings.Join(values, ", ")
			}
		}
		mu.Lock()
		sent = append(sent, devinSentRequest{path: r.URL.Path, headers: headers, body: body})
		assigned := assign
		mu.Unlock()
		switch r.URL.Path {
		case devinwire.UserJWTPath:
			w.Header().Set("Content-Type", "application/proto")
			_, _ = w.Write(protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "user-jwt-capture"))
		case devinwire.AssignModelPath:
			var assignment []byte
			assignment = protowire.AppendString(protowire.AppendTag(assignment, 1, protowire.BytesType), "assignment-jwt-capture")
			assignment = protowire.AppendString(protowire.AppendTag(assignment, 2, protowire.BytesType), assigned)
			w.Header().Set("Content-Type", "application/proto")
			_, _ = w.Write(protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), assignment))
		default:
			var message []byte
			message = protowire.AppendString(protowire.AppendTag(message, 1, protowire.BytesType), "resp-capture")
			message = protowire.AppendString(protowire.AppendTag(message, 3, protowire.BytesType), "Captured.")
			w.Header().Set("Content-Type", "application/connect+proto")
			_, _ = w.Write(devinwire.Frame(message, 0))
			_, _ = w.Write(devinwire.Frame([]byte("{}"), devinwire.FlagEndStream))
		}
	}))
	defer server.Close()

	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "devin-replay", Provider: "devin", Metadata: map[string]any{"api_key": capture.APIKey}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			model := capture.Models[fixture.Model]
			model.BaseURL = server.URL
			var counter int
			exec.newID = func() string {
				counter++
				return fmt.Sprintf("00000000-0000-4000-8000-%012d", counter)
			}
			mu.Lock()
			sent, assign = nil, fixture.Assign
			mu.Unlock()
			var downstream bytes.Buffer
			if err := json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			response, errExecute := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:    fixture.Model,
				Payload:  downstream.Bytes(),
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.DevinModelInfo(&model)},
			}, cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: downstream.Bytes(),
				Headers:         http.Header{"X-Claude-Code-Session-Id": {capture.SessionID}},
			})
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			var reply bytes.Buffer
			if err := json.Compact(&reply, fixture.Reply); err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(response.Payload, "content").Raw; got != reply.String() {
				t.Errorf("reply content = %s, want %s", got, reply.String())
			}
			if len(sent) != len(fixture.Requests) {
				t.Fatalf("sent %d requests, want %d", len(sent), len(fixture.Requests))
			}
			for i, want := range fixture.Requests {
				got := sent[i]
				if got.path != want.Path {
					t.Errorf("request %d path = %s, want %s", i, got.path, want.Path)
				}
				wantHeaders := map[string]string{}
				for name, value := range want.Headers {
					if name != "host" && name != "content-length" {
						wantHeaders[name] = value
					}
				}
				if !reflect.DeepEqual(got.headers, wantHeaders) {
					t.Errorf("request %d headers:\n got %v\nwant %v", i, got.headers, wantHeaders)
				}
				payload := got.body
				if want.FrameFlags != nil {
					if len(payload) < 5 || payload[0] != *want.FrameFlags {
						t.Fatalf("request %d frame flags differ: got % x, want %d", i, payload[:min(len(payload), 5)], *want.FrameFlags)
					}
					payload = payload[5:]
					if *want.FrameFlags&devinwire.FlagCompressed != 0 {
						if payload, err = devinwire.Gunzip(payload); err != nil {
							t.Fatal(err)
						}
					}
				}
				wantProto, _ := base64.StdEncoding.DecodeString(want.Proto)
				if !bytes.Equal(payload, wantProto) {
					t.Errorf("request %d protobuf differs:\n got %x\nwant %x", i, payload, wantProto)
				}
			}
		})
	}
}

// TestFetchDevinModelsSendsNeoPiRequest requires the GetCliModelConfigs request
// NeoPi's discovery put on the wire (internal/registry/testdata/
// capture_devin_npi_discovery.ts) and the full roster from its response.
func TestFetchDevinModelsSendsNeoPiRequest(t *testing.T) {
	data, err := os.ReadFile("../../registry/testdata/devin_npi_discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		APIKey  string `json:"apiKey"`
		Request struct {
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"request"`
		Models []json.RawMessage `json:"models"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	compressed, err := os.ReadFile("../../registry/testdata/devin_cli_model_configs.bin.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var sent devinSentRequest
	var method string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{}
		for name, values := range r.Header {
			if name != "Content-Length" {
				headers[strings.ToLower(name)] = strings.Join(values, ", ")
			}
		}
		method, sent = r.Method, devinSentRequest{path: r.URL.Path, headers: headers, body: body}
		w.Header().Set("Content-Type", "application/proto")
		_, _ = w.Write(response)
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: "devin-discovery", Provider: "devin",
		Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"api_key": capture.APIKey}}
	models, err := FetchDevinModels(context.Background(), &config.Config{}, auth)
	if err != nil {
		t.Fatal(err)
	}
	if method != capture.Request.Method || sent.path != capture.Request.Path {
		t.Errorf("request = %s %s, want %s %s", method, sent.path, capture.Request.Method, capture.Request.Path)
	}
	wantHeaders := map[string]string{}
	for name, value := range capture.Request.Headers {
		if name != "host" && name != "content-length" {
			wantHeaders[name] = value
		}
	}
	if !reflect.DeepEqual(sent.headers, wantHeaders) {
		t.Errorf("headers:\n got %v\nwant %v", sent.headers, wantHeaders)
	}
	if wantBody, _ := base64.StdEncoding.DecodeString(capture.Request.Body); !bytes.Equal(sent.body, wantBody) {
		t.Errorf("body:\n got %x\nwant %x", sent.body, wantBody)
	}
	if len(models) != len(capture.Models) {
		t.Errorf("discovered %d models, want %d", len(models), len(capture.Models))
	}
	for _, model := range models {
		if model.NeoPi.BaseURL != server.URL {
			t.Fatalf("%s base URL = %s, want the configured %s", model.ID, model.NeoPi.BaseURL, server.URL)
		}
	}
}

// A client effort the account's family does not serve is fitted to its ladder,
// as NeoPi's gateway does (normalizeGatewayThinking); a reasoning model with no
// ladder keeps NeoPi's strict rejection.
func TestDevinGatewayClampsEffort(t *testing.T) {
	family := &registry.NeoPiModel{ID: "swe-2-flash", API: "devin-agent", Provider: "devin", Reasoning: true, RequestModelID: "swe-2-flash-medium",
		Thinking: &registry.NeoPiThinking{Mode: "effort", Efforts: []string{"medium", "high"}, RequiresEffort: true,
			EffortRouting: map[string]string{"medium": "swe-2-flash-medium", "high": "swe-2-flash-high"}}}
	unladdered := &registry.NeoPiModel{ID: "swe-1-6-slow", API: "devin-agent", Provider: "devin", Reasoning: true}
	exec := NewDevinExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "devin-clamp", Provider: "devin", Metadata: map[string]any{"api_key": "eyJ.a.b"}}
	prepare := func(model *registry.NeoPiModel, effort string) (*devinPrepared, error) {
		body := fmt.Sprintf(`{"model":%q,"max_tokens":16,"output_config":{"effort":%q},"messages":[{"role":"user","content":"hi"}]}`, model.ID, effort)
		return exec.prepare(context.Background(), auth, cliproxyexecutor.Request{
			Model: model.ID, Payload: []byte(body),
			Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.DevinModelInfo(model)},
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: []byte(body)})
	}
	for effort, want := range map[string]string{"low": "swe-2-flash-medium", "medium": "swe-2-flash-medium", "xhigh": "swe-2-flash-high", "max": "swe-2-flash-high"} {
		prepared, err := prepare(family, effort)
		if err != nil {
			t.Fatalf("effort %s: %v", effort, err)
		}
		if prepared.chatUID != want {
			t.Errorf("effort %s routed to %s, want %s", effort, prepared.chatUID, want)
		}
	}
	if _, err := prepare(unladdered, "low"); err == nil || !strings.Contains(err.Error(), "Thinking effort low is not supported by devin/swe-1-6-slow") {
		t.Fatalf("effort on a model without a ladder = %v, want NeoPi's rejection", err)
	}
}
