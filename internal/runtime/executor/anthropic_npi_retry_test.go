package executor

import (
	"context"
	"encoding/json"
	"errors"
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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type neoPiRetryCase struct {
	Name     string            `json:"name"`
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body"`
	Failures int               `json:"failures"`
	Attempts int               `json:"attempts"`
	Waits    []float64         `json:"waits"`
	Success  bool              `json:"success"`
}

type neoPiRetryFixture struct {
	Source string           `json:"source"`
	Now    int64            `json:"now"`
	Random float64          `json:"random"`
	Cases  []neoPiRetryCase `json:"cases"`
}

func TestAnthropicNPIRetryCapturedParity(t *testing.T) {
	data, err := os.ReadFile("testdata/anthropic_npi_retries.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture neoPiRetryFixture
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	wire := neoPiLoadFixture(t)
	var baseline neoPiCapturedRequest
	for _, capture := range wire.Fixtures {
		if capture.Name == "official/plain-high" {
			baseline = capture
		}
	}
	if baseline.RequestedURL == "" {
		t.Fatal("missing Messages request baseline")
	}
	var mu sync.Mutex
	var active neoPiRetryCase
	var requests []neoPiCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/__retry/setup":
			active = neoPiRetryCase{}
			if err := json.NewDecoder(r.Body).Decode(&active); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			requests = nil
			_, _ = io.WriteString(w, `{ "ok": true }`)
			return
		case "/__retry/capture":
			_ = json.NewEncoder(w).Encode(requests)
			return
		}
		body, _ := io.ReadAll(r.Body)
		headers := make(map[string]string, len(r.Header)+1)
		for name, values := range r.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		headers["host"] = r.Host
		requests = append(requests, neoPiCapturedRequest{Method: r.Method, Path: r.URL.RequestURI(), Headers: headers, RawBody: string(body)})
		if len(requests) <= active.Failures {
			for name, value := range active.Headers {
				w.Header().Set(name, value)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(active.Status)
			_, _ = io.WriteString(w, active.Body)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, wire.Response)
	}))
	defer server.Close()
	if os.Getenv("NEOPI_SOURCE_MODULES") != "" {
		home := os.Getenv("NEOPI_CAPTURE_HOME")
		if home == "" {
			t.Fatal("NEOPI_CAPTURE_HOME must be an isolated capture home")
		}
		script, err := filepath.Abs("testdata/capture_anthropic_npi_retry.ts")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("bun", "--preserve-symlinks", script)
		cmd.Env = append(os.Environ(), "HOME="+home, "NEOPI_CAPTURE_URL="+server.URL, "PI_CODING_AGENT_DIR="+t.TempDir(), "PI_PROFILE=", "NPI_PROFILE=")
		output, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				t.Fatalf("NeoPi retry capture: %v: %s", err, exit.Stderr)
			}
			t.Fatal(err)
		}
		var live neoPiRetryFixture
		if err := json.Unmarshal(output, &live); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(live, fixture) {
			for i, actual := range live.Cases {
				if i >= len(fixture.Cases) || !reflect.DeepEqual(actual, fixture.Cases[i]) {
					t.Errorf("current NeoPi case %d: %+v; pinned: %+v", i, actual, fixture.Cases[min(i, len(fixture.Cases)-1)])
				}
			}
			t.Fatal("current NeoPi retry behavior differs from the committed capture")
		}
		t.Log("Current NeoPi public stream and Go executor use the same local capture server; every retry request matches the pinned Messages wire capture")
	}
	target, _ := url.Parse(server.URL)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			mu.Lock()
			active, requests = tc, nil
			mu.Unlock()
			transport := &neoPiCaptureTransport{target: target, base: http.DefaultTransport}
			auth := &cliproxyauth.Auth{ID: "retry-parity", Provider: anthropicnpi.Provider, Metadata: map[string]any{"access_token": wire.Token, "neopi_install_id": wire.InstallID, "account_uuid": wire.AccountID}}
			executor := NewAnthropicNPIExecutor(nil)
			executor.now = func() time.Time { return time.UnixMilli(fixture.Now) }
			executor.random = func() float64 { return fixture.Random }
			waits := make([]float64, 0)
			executor.wait = func(_ context.Context, delay time.Duration) error {
				waits = append(waits, float64(delay)/float64(time.Millisecond))
				return nil
			}
			ctx := context.Background()
			prepared, err := executor.prepare(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: baseline.Input}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude"), Headers: http.Header{"X-Claude-Code-Session-Id": []string{wire.SessionID}}})
			if err != nil {
				t.Fatal(err)
			}
			stream, err := executor.open(ctx, &http.Client{Transport: transport}, prepared)
			if err == nil {
				err = stream.consume(func([]byte) error { return nil })
			}
			if (err == nil) != tc.Success {
				t.Errorf("success = %v, want %v; error = %v", err == nil, tc.Success, err)
			}
			if !reflect.DeepEqual(waits, tc.Waits) {
				t.Errorf("waits = %v, want %v", waits, tc.Waits)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != tc.Attempts {
				t.Errorf("wire attempts = %d, want %d", len(requests), tc.Attempts)
			}
			for i, request := range requests {
				if transport.seenURL != baseline.RequestedURL || request.Method != baseline.Method || request.Path != baseline.Path || !reflect.DeepEqual(request.Headers, baseline.Headers) || request.RawBody != baseline.RawBody {
					t.Errorf("attempt %d changed the captured Messages URL, method, headers, or body", i)
				}
			}
		})
	}
}

func TestAnthropicNPIRetryDoesNotReplayCallerCancellation(t *testing.T) {
	executor := NewAnthropicNPIExecutor(nil)
	executor.wait = func(context.Context, time.Duration) error {
		t.Fatal("cancelled request scheduled a retry")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := errors.New("connection closed")
	if got := executor.retry(ctx, failure, 0); got != failure {
		t.Fatalf("cancellation lost original failure: %v", got)
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		if got := executor.retry(context.Background(), err, 0); got != err {
			t.Fatalf("caller error was retried: %v", got)
		}
	}
}
