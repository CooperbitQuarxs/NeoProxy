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
