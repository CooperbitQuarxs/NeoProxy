package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// ollamaCloudCapture is ../../ollamawire/testdata/ollama_cloud_runtime.json:
// NeoPi's OWN ollama-cloud streamSimple over gateway-parsed chat bodies
// against a scripted /api/chat.
type ollamaCloudCapture struct {
	APIKey    string `json:"apiKey"`
	Scenarios []struct {
		Name    string `json:"name"`
		Body    string `json:"body"`
		Replies []struct {
			Status  int               `json:"status"`
			Body    string            `json:"body"`
			Headers map[string]string `json:"headers"`
		} `json:"replies"`
		Requests []struct {
			Method  string            `json:"method"`
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"requests"`
		Final struct {
			StopReason string `json:"stopReason"`
		} `json:"final"`
	} `json:"scenarios"`
}

type ollamaCloudRecorded struct {
	path    string
	headers http.Header
	body    string
}

// ollamaCloudServer answers /api/chat with scripted replies and records
// every request.
func ollamaCloudServer(t *testing.T, replies []struct {
	Status  int               `json:"status"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}) (*httptest.Server, func() []ollamaCloudRecorded) {
	var mu sync.Mutex
	var recorded []ollamaCloudRecorded
	queue := append(replies[:0:0], replies...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		recorded = append(recorded, ollamaCloudRecorded{path: r.URL.Path, headers: r.Header.Clone(), body: string(body)})
		if len(queue) == 0 {
			mu.Unlock()
			http.Error(w, "no scripted reply", 599)
			return
		}
		reply := queue[0]
		queue = queue[1:]
		mu.Unlock()
		for name, value := range reply.Headers {
			w.Header().Set(name, value)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(reply.Status)
		_, _ = io.WriteString(w, reply.Body)
	}))
	t.Cleanup(server.Close)
	return server, func() []ollamaCloudRecorded {
		mu.Lock()
		defer mu.Unlock()
		return append([]ollamaCloudRecorded(nil), recorded...)
	}
}

// runOllamaCloud streams one turn and returns the terminal error, if any.
func runOllamaCloud(t *testing.T, exec *OllamaCloudExecutor, auth *cliproxyauth.Auth, model, body string) error {
	t.Helper()
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true}
	stream, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		return err
	}
	var last error
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			last = chunk.Err
		}
	}
	return last
}

// Every gateway chat body reaches /api/chat exactly as NeoPi's streamOllama
// sends it (URL, headers and body byte for byte) for each attempt NeoPi
// makes: fetchWithRetry's 5xx retry and llama.cpp parse-failure refusal, and
// the replay-safe empty-completion retries. Error turns fail the request.
func TestOllamaCloudRequestsMatchNeoPi(t *testing.T) {
	data, err := os.ReadFile("../../ollamawire/testdata/ollama_cloud_runtime.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture ollamaCloudCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Scenarios) == 0 {
		t.Fatal("capture has no scenarios")
	}
	for _, tc := range capture.Scenarios {
		t.Run(tc.Name, func(t *testing.T) {
			server, recorded := ollamaCloudServer(t, tc.Replies)
			exec := NewOllamaCloudExecutor("", &config.Config{})
			exec.wait = func(context.Context, time.Duration) error { return nil }
			auth := &cliproxyauth.Auth{ID: "ollama-cloud-test", Provider: "ollama-cloud", Attributes: map[string]string{"api_key": capture.APIKey, "base_url": server.URL}}
			errRun := runOllamaCloud(t, exec, auth, gjson.Get(tc.Body, "model").String(), tc.Body)
			got := recorded()
			if len(got) != len(tc.Requests) {
				t.Fatalf("requests = %d, want %d", len(got), len(tc.Requests))
			}
			for i, request := range got {
				want := tc.Requests[i]
				if request.path != want.Path {
					t.Errorf("request %d path = %s, want %s", i, request.path, want.Path)
				}
				if request.body != want.Body {
					t.Errorf("request %d body:\n got %s\nwant %s", i, request.body, want.Body)
				}
				for _, name := range []string{"accept", "accept-encoding", "authorization", "connection", "content-type", "user-agent"} {
					if value := request.headers.Get(name); value != want.Headers[name] {
						t.Errorf("request %d %s = %q, want %q", i, name, value, want.Headers[name])
					}
				}
			}
			if failed := tc.Final.StopReason == "error"; failed != (errRun != nil) {
				t.Fatalf("error = %v, want failure %v", errRun, failed)
			}
			if last := tc.Replies[len(tc.Replies)-1]; last.Status != http.StatusOK {
				var status interface{ StatusCode() int }
				if !errors.As(errRun, &status) || status.StatusCode() != last.Status {
					t.Errorf("error = %v, want upstream status %d", errRun, last.Status)
				}
			}
		})
	}
}

// Configured payload rules are the last word on the /api/chat body: an
// override replaces the think level the request resolved, and nothing
// rebuilt afterwards restores it.
func TestOllamaCloudPayloadRulesApplyLast(t *testing.T) {
	server, recorded := ollamaCloudServer(t, []struct {
		Status  int               `json:"status"`
		Body    string            `json:"body"`
		Headers map[string]string `json:"headers"`
	}{{Status: http.StatusOK, Body: `{"message":{"content":"ok"},"done":true,"done_reason":"stop","eval_count":3}` + "\n"}})
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "gpt-oss:120b", Protocol: "ollama"}},
		Params: map[string]any{"think": "low", "options.num_ctx": 8192},
	}}}}
	exec := NewOllamaCloudExecutor("", cfg)
	exec.wait = func(context.Context, time.Duration) error { return nil }
	auth := &cliproxyauth.Auth{ID: "ollama-cloud-test", Provider: "ollama-cloud", Attributes: map[string]string{"api_key": "key", "base_url": server.URL}}
	body := `{"model":"gpt-oss:120b","messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"high"}`
	if err := runOllamaCloud(t, exec, auth, "gpt-oss:120b", body); err != nil {
		t.Fatal(err)
	}
	got := recorded()
	if len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	if think := gjson.Get(got[0].body, "think").String(); think != "low" {
		t.Errorf("think = %q, want the configured low", think)
	}
	if ctx := gjson.Get(got[0].body, "options.num_ctx").Int(); ctx != 8192 {
		t.Errorf("options.num_ctx = %d, want 8192", ctx)
	}
	if !strings.Contains(got[0].body, `"stream":true`) {
		t.Errorf("body lost stream: %s", got[0].body)
	}
}
