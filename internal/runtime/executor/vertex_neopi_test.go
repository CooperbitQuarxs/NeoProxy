package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// vertexCapture is testdata/vertex_npi_capture.json.gz: NeoPi's OWN
// streamSimple over its bundled google-vertex Gemini rows, every request
// redirected to a loopback server, each scenario with the Vertex environment
// it ran under, the account key NeoPi was handed, and the statuses the server
// answered with.
type vertexCapture struct {
	Fixtures []struct {
		Name       string            `json:"name"`
		Model      string            `json:"model"`
		Env        map[string]string `json:"env"`
		APIKey     string            `json:"apiKey"`
		Statuses   []int             `json:"statuses"`
		Downstream json.RawMessage   `json:"downstream"`
		Error      string            `json:"error"`
		Requests   []struct {
			RequestedURL string            `json:"requestedURL"`
			Method       string            `json:"method"`
			Headers      map[string]string `json:"headers"`
			RawBody      string            `json:"rawBody"`
		} `json:"requests"`
	} `json:"fixtures"`
}

type vertexWireRequest struct {
	method, url, host string
	header            http.Header
	contentLength     int64
	body              []byte
}

// vertexWireRecorder records every request as the transport would put it on
// the wire and answers with the scenario's statuses, then a canned stream.
type vertexWireRecorder struct {
	statuses []int
	requests []vertexWireRequest
}

func (r *vertexWireRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	recorded := vertexWireRequest{method: req.Method, url: req.URL.String(), header: req.Header.Clone(), contentLength: req.ContentLength, host: req.Host}
	if recorded.host == "" {
		recorded.host = req.URL.Host
	}
	recorded.body, _ = io.ReadAll(req.Body)
	r.requests = append(r.requests, recorded)
	if len(r.statuses) > 0 {
		status := r.statuses[0]
		r.statuses = r.statuses[1:]
		if status != http.StatusOK {
			body := `{"error":{"code":404,"message":"Publisher model not found.","status":"NOT_FOUND"}}`
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
		}
	}
	stream := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Captured."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}` + "\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(stream)), Request: req}, nil
}

// Every captured NeoPi Vertex request is reproduced by the request NeoProxy
// sends: method, full URL, authority, headers (content-length value included)
// and body, for each row's request shapes, every endpoint and credential the
// environment resolves (API key or ADC token; global, regional and
// multi-regional hosts; the global retry after a regional 404), and NeoPi's
// configuration and upstream errors. Only `connection` is excluded (see
// TestGeminiNeoPiRequestParity).
func TestVertexNeoPiRequestParity(t *testing.T) {
	file, err := os.Open("testdata/vertex_npi_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture vertexCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("capture has no fixtures")
	}
	exec := NewVertexExecutor(&config.Config{})
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			for _, name := range []string{"GOOGLE_CLOUD_API_KEY", "GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT", "GOOGLE_VERTEX_LOCATION",
				"GOOGLE_CLOUD_LOCATION", "VERTEX_LOCATION", "GOOGLE_CLOUD_ACCESS_TOKEN", "CLOUDSDK_AUTH_ACCESS_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS"} {
				t.Setenv(name, fixture.Env[name])
			}
			recorder := &vertexWireRecorder{statuses: append([]int(nil), fixture.Statuses...)}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", recorder)
			auth := &cliproxyauth.Auth{ID: "parity", Provider: "vertex", Attributes: map[string]string{}}
			if fixture.APIKey != "" {
				auth.Attributes["api_key"] = fixture.APIKey
			}
			var downstream bytes.Buffer
			if err := json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			req := cliproxyexecutor.Request{Model: fixture.Model, Payload: downstream.Bytes()}
			_, err := exec.Execute(ctx, auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
			gotErr := ""
			if err != nil {
				gotErr = err.Error()
			}
			if gotErr != fixture.Error {
				t.Errorf("error = %q, NeoPi %q", gotErr, fixture.Error)
			}
			if len(recorder.requests) != len(fixture.Requests) {
				t.Fatalf("sent %d requests, NeoPi %d", len(recorder.requests), len(fixture.Requests))
			}
			for i, want := range fixture.Requests {
				got := recorder.requests[i]
				if got.method != want.Method || got.url != want.RequestedURL || got.host != want.Headers["host"] {
					t.Errorf("request %d: %s %s (host %s), want %s %s (host %s)", i, got.method, got.url, got.host, want.Method, want.RequestedURL, want.Headers["host"])
				}
				if string(got.body) != want.RawBody {
					t.Errorf("request %d body:\n got %s\nwant %s", i, got.body, want.RawBody)
				}
				if length := strconv.FormatInt(got.contentLength, 10); length != want.Headers["content-length"] {
					t.Errorf("request %d content-length = %s, want %s", i, length, want.Headers["content-length"])
				}
				headers := map[string]string{}
				for name, values := range got.header {
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				for name, value := range want.Headers {
					if name == "host" || name == "content-length" || name == "connection" {
						continue
					}
					if headers[name] != value {
						t.Errorf("request %d header %s = %q, want %q", i, name, headers[name], value)
					}
				}
				for name, value := range headers {
					if _, ok := want.Headers[name]; !ok {
						t.Errorf("request %d extra header %s = %q", i, name, value)
					}
				}
			}
		})
	}
}

// vertexModelGardenEnv is a process ADC environment for Model Garden rows:
// the explicit access token NeoPi's getVertexAccessToken reads first.
func vertexModelGardenEnv(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"GOOGLE_CLOUD_PROJECT": "review-project", "GCP_PROJECT": "", "GCLOUD_PROJECT": "",
		"GOOGLE_CLOUD_LOCATION": "us-central1", "GOOGLE_VERTEX_LOCATION": "", "VERTEX_LOCATION": "",
		"GOOGLE_CLOUD_ACCESS_TOKEN": "review-adc-token", "GOOGLE_CLOUD_API_KEY": "",
	} {
		t.Setenv(name, value)
	}
}

// vertexClaudeRow is a bundled Claude rawPredict row id.
func vertexClaudeRow(t *testing.T) string {
	t.Helper()
	for _, info := range registry.VertexBundledModels() {
		if info.NeoPi.API == "anthropic-messages" {
			return info.ID
		}
	}
	t.Fatal("no bundled Claude row")
	return ""
}

// A thinking suffix names the same row in both dispatch layers: a Claude
// request without registered NeoPi metadata stays on Anthropic Messages.
func TestVertexModelGardenSuffixedModelKeepsItsRow(t *testing.T) {
	vertexModelGardenEnv(t)
	e := NewVertexExecutor(&config.Config{})
	req := cliproxyexecutor.Request{Model: vertexClaudeRow(t) + "(high)", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":100}`)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	if got := e.RequestToFormat(req, opts); got != sdktranslator.FormatClaude {
		t.Fatalf("format = %s, want claude", got)
	}
	if row := e.garden.row(nil, req); row == nil || row.API != "anthropic-messages" {
		t.Fatalf("garden row = %+v, want the Claude row", row)
	}
}

// NeoPi's Vertex fetch sets the ADC bearer on the finished request, so an
// account's configured Authorization header never reaches rawPredict.
func TestVertexRawPredictBearerOverridesConfiguredAuthorization(t *testing.T) {
	vertexModelGardenEnv(t)
	e := NewVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "review-account-key", "header:Authorization": "Bearer stale-account-header"}}
	prepared, err := e.garden.messages.prepareThirdParty(context.Background(), auth, cliproxyexecutor.Request{Model: vertexClaudeRow(t), Payload: []byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":100}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatal(err)
	}
	if got := prepared.headers.Get("Authorization"); got != "Bearer review-adc-token" {
		t.Fatalf("Authorization = %q, want the ADC bearer", got)
	}
}

// An arbitrary request to a Model Garden endpoint carries the ADC bearer, not
// the Gemini API key. Its URL is already placed, so no project or location
// is needed (resolveVertexRequest leaves such a URL as it is).
func TestVertexHttpRequestModelGardenUsesADC(t *testing.T) {
	vertexModelGardenEnv(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	e := NewVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "review-account-key"}}
	for _, target := range []string{
		"https://us-central1-aiplatform.googleapis.com/v1/projects/review-project/locations/us-central1/endpoints/openapi/chat/completions",
		"https://us-central1-aiplatform.googleapis.com/v1/projects/review-project/locations/us-central1/publishers/anthropic/models/claude:streamRawPredict",
	} {
		var headers http.Header
		transport := vertexRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			headers = r.Header.Clone()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		response, err := e.HttpRequest(ctx, auth, req)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if got, key := headers.Get("Authorization"), headers.Get("X-Goog-Api-Key"); got != "Bearer review-adc-token" || key != "" {
			t.Errorf("%s: Authorization = %q, X-Goog-Api-Key = %q; want the ADC bearer alone", target, got, key)
		}
	}
}

type vertexRoundTripFunc func(*http.Request) (*http.Response, error)

func (f vertexRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
