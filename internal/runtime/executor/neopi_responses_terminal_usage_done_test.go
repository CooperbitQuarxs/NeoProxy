package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// The barrier is dispatched after the turn's records, so assertions do not
// depend on a sleep or on the usage plugin goroutine winning a scheduling race.
type neoPiTerminalUsageCapture struct {
	id      string
	records chan usage.Record
}

func (p *neoPiTerminalUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.AuthID == p.id || record.RequestID == p.id+"-barrier" {
		p.records <- record
	}
}

func neoPiTerminalCaptureUsage(t *testing.T, id string) func() []usage.Record {
	t.Helper()
	capture := &neoPiTerminalUsageCapture{id: id, records: make(chan usage.Record, 32)}
	usage.RegisterNamedPlugin("neopi-terminal-usage-done", capture)
	return func() []usage.Record {
		t.Helper()
		usage.PublishRecord(context.Background(), usage.Record{RequestID: id + "-barrier"})
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		var records []usage.Record
		for {
			select {
			case record := <-capture.records:
				if record.RequestID == id+"-barrier" {
					return records
				}
				records = append(records, record)
			case <-deadline.C:
				t.Fatal("usage dispatch barrier did not arrive")
			}
		}
	}
}

func neoPiTerminalLoopback(t *testing.T, sse string) (*NeoPiResponsesExecutor, *cliproxyauth.Auth) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(server.Close)
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "meta", Attributes: map[string]string{
		"api_key": "test-key", "base_url": server.URL, "auth_kind": "apikey",
	}}
	return NewNeoPiResponsesExecutor("meta", &config.Config{}), auth
}

func TestNeoPiResponsesFailedPublishesFailureTokenDetail(t *testing.T) {
	const failed = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_failed_usage\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_failed_usage\",\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"upstream generation failed\"},\"usage\":{\"input_tokens\":17,\"output_tokens\":5,\"total_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n"
	for _, streaming := range []bool{false, true} {
		name := "nonstream"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			exec, auth := neoPiTerminalLoopback(t, failed)
			usageRecords := neoPiTerminalCaptureUsage(t, auth.ID)
			req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			if streaming {
				result, err := exec.ExecuteStream(t.Context(), auth, req, opts)
				failed := err != nil
				if result != nil {
					for chunk := range result.Chunks {
						failed = failed || chunk.Err != nil
						if strings.Contains(string(chunk.Payload), "response.completed") {
							t.Errorf("failed response emitted successful completion: %s", chunk.Payload)
						}
					}
				}
				if !failed {
					t.Error("response.failed did not fail the stream")
				}
			} else {
				response, err := exec.Execute(t.Context(), auth, req, opts)
				if err == nil || len(response.Payload) != 0 {
					t.Errorf("response.failed returned success: payload=%s err=%v", response.Payload, err)
				}
			}
			records := usageRecords()
			if len(records) != 1 {
				t.Fatalf("usage records = %d, want exactly one", len(records))
			}
			record := records[0]
			if !record.Failed {
				t.Errorf("response.failed published successful usage: %+v", record)
			}
			detail := record.Detail
			if detail.InputTokens != 17 || detail.OutputTokens != 5 || detail.TotalTokens != 22 || detail.CachedTokens != 3 || detail.ReasoningTokens != 2 {
				t.Errorf("failure lost terminal token detail: %+v", detail)
			}
		})
	}
}

// Keep all other frames identical to the existing completed fixture: only the
// terminal event's alias changes, not the response's completed status or usage.
func neoPiDoneTestSSE() string {
	return strings.ReplaceAll(neoPiResponsesTestSSE, "response.completed", "response.done")
}

func TestNeoPiResponsesDoneReturnsNormalNonstreamResponse(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI} {
		t.Run(format.String(), func(t *testing.T) {
			exec, auth := neoPiTerminalLoopback(t, neoPiDoneTestSSE())
			payload := []byte(`{"model":"muse-spark-1.3","input":"hi"}`)
			if format == sdktranslator.FormatOpenAI {
				payload = []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}]}`)
			}
			response, err := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: payload}, cliproxyexecutor.Options{SourceFormat: format})
			if err != nil {
				t.Fatalf("response.done should complete normally: %v", err)
			}
			if !gjson.ValidBytes(response.Payload) {
				t.Fatalf("invalid response JSON: %s", response.Payload)
			}
			if format == sdktranslator.FormatOpenAIResponse {
				if gjson.GetBytes(response.Payload, "id").String() != "resp_1" || gjson.GetBytes(response.Payload, "status").String() != "completed" || gjson.GetBytes(response.Payload, "output.0.content.0.text").String() != "Hello from Muse." || gjson.GetBytes(response.Payload, "usage.total_tokens").Int() != 7 {
					t.Errorf("response.done did not yield normal Responses output: %s", response.Payload)
				}
			} else if gjson.GetBytes(response.Payload, "choices.0.message.content").String() != "Hello from Muse." || gjson.GetBytes(response.Payload, "choices.0.finish_reason").String() != "stop" || gjson.GetBytes(response.Payload, "usage.total_tokens").Int() != 7 {
				t.Errorf("response.done did not yield normal chat completion: %s", response.Payload)
			}
		})
	}
}

func TestNeoPiResponsesDoneEmitsChatStreamingFinishChunk(t *testing.T) {
	exec, auth := neoPiTerminalLoopback(t, neoPiDoneTestSSE())
	req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","messages":[{"role":"user","content":"hi"}],"stream":true}`)}
	result, err := exec.ExecuteStream(t.Context(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	finishes := 0
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Errorf("response.done failed the stream: %v", chunk.Err)
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			data := strings.TrimSpace(line)
			if strings.HasPrefix(data, "data:") {
				data = strings.TrimSpace(strings.TrimPrefix(data, "data:"))
			}
			finish := gjson.Get(data, "choices.0.finish_reason")
			if finish.Type != gjson.String {
				continue
			}
			finishes++
			if finish.String() != "stop" || gjson.Get(data, "object").String() != "chat.completion.chunk" {
				t.Errorf("unexpected chat terminal chunk: %s", data)
			}
		}
	}
	if finishes != 1 {
		t.Errorf("response.done emitted %d chat finish chunks, want exactly one", finishes)
	}
}
