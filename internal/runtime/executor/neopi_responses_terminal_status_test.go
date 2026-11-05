package executor

import (
	"fmt"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// NeoPi interprets the nested response status on all three terminal aliases;
// the event name alone must not turn a rejected response into a successful turn.
func neoPiTerminalStatusSSE(alias, status, detail string, withDelta bool) string {
	stream := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_terminal_status\",\"status\":\"in_progress\",\"output\":[]}}\n\n"
	if withDelta {
		stream += "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_terminal_status\",\"output_index\":0,\"content_index\":0,\"delta\":\"Partial answer\"}\n\n"
	}
	return stream + fmt.Sprintf("data: {\"type\":%q,\"response\":{\"id\":\"resp_terminal_status\",\"object\":\"response\",\"model\":\"muse-spark-1.3\",\"status\":%q,%s,\"output\":[{\"id\":\"msg_terminal_status\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Partial answer\"}]}],\"usage\":{\"input_tokens\":17,\"output_tokens\":5,\"total_tokens\":22,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n", alias, status, detail)
}

func TestNeoPiResponsesTerminalStatusRejectsFailedAndContentFilteredAliases(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		detail string
	}{
		{"failed_invalid_request", "failed", `"error":{"code":"invalid_request_error","message":"Request rejected."}`},
		{"incomplete_content_filter", "incomplete", `"incomplete_details":{"reason":"content_filter"}`},
	} {
		for _, alias := range []string{"response.done", "response.completed", "response.incomplete"} {
			for _, mode := range []string{"nonstream", "stream_before_output", "stream_after_output"} {
				t.Run(tc.name+"/"+alias+"/"+mode, func(t *testing.T) {
					exec, auth := neoPiTerminalLoopback(t, neoPiTerminalStatusSSE(alias, tc.status, tc.detail, mode == "stream_after_output"))
					usageRecords := neoPiTerminalCaptureUsage(t, auth.ID)
					req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi"}`)}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
					var consumerErr error
					if mode == "nonstream" {
						response, err := exec.Execute(t.Context(), auth, req, opts)
						consumerErr = err
						if len(response.Payload) != 0 {
							t.Errorf("rejected terminal returned a successful response payload: %s", response.Payload)
						}
					} else {
						result, err := exec.ExecuteStream(t.Context(), auth, req, opts)
						consumerErr = err
						if result != nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									consumerErr = chunk.Err
								}
								for _, line := range strings.Split(string(chunk.Payload), "\n") {
									data := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
									switch gjson.Get(data, "type").String() {
									case "response.done", "response.completed", "response.incomplete":
										t.Errorf("rejected terminal emitted a successful terminal event: %s", data)
									}
									if data == "[DONE]" {
										t.Error("rejected terminal emitted a successful [DONE] sentinel")
									}
								}
							}
						}
					}
					if consumerErr == nil {
						t.Error("rejected terminal was reported as success, want consumer-visible error")
					}
					records := usageRecords()
					if len(records) != 1 {
						t.Fatalf("usage records = %d, want exactly one failed turn record", len(records))
					}
					if !records[0].Failed {
						t.Errorf("rejected terminal published successful usage: %+v", records[0])
					}
					detail := records[0].Detail
					if detail.InputTokens != 17 || detail.OutputTokens != 5 || detail.TotalTokens != 22 || detail.CachedTokens != 3 || detail.ReasoningTokens != 2 {
						t.Errorf("rejected terminal lost token usage: %+v", detail)
					}
				})
			}
		}
	}
}

func TestNeoPiResponsesTerminalStatusAllowsMaxOutputTokensAliases(t *testing.T) {
	for _, alias := range []string{"response.done", "response.completed", "response.incomplete"} {
		for _, mode := range []string{"nonstream", "stream"} {
			t.Run(alias+"/"+mode, func(t *testing.T) {
				exec, auth := neoPiTerminalLoopback(t, neoPiTerminalStatusSSE(alias, "incomplete", `"incomplete_details":{"reason":"max_output_tokens"}`, true))
				usageRecords := neoPiTerminalCaptureUsage(t, auth.ID)
				req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: []byte(`{"model":"muse-spark-1.3","input":"hi"}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				if mode == "nonstream" {
					response, err := exec.Execute(t.Context(), auth, req, opts)
					if err != nil {
						t.Errorf("max_output_tokens must remain successful: %v", err)
					}
					if gjson.GetBytes(response.Payload, "status").String() != "incomplete" || gjson.GetBytes(response.Payload, "incomplete_details.reason").String() != "max_output_tokens" || gjson.GetBytes(response.Payload, "output.0.content.0.text").String() != "Partial answer" || gjson.GetBytes(response.Payload, "usage.total_tokens").Int() != 22 {
						t.Errorf("max_output_tokens lost successful partial response or usage: %s", response.Payload)
					}
				} else {
					result, err := exec.ExecuteStream(t.Context(), auth, req, opts)
					if err != nil {
						t.Errorf("max_output_tokens must remain successful: %v", err)
					}
					terminals := 0
					if result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Errorf("max_output_tokens failed the stream: %v", chunk.Err)
							}
							for _, line := range strings.Split(string(chunk.Payload), "\n") {
								data := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
								switch gjson.Get(data, "type").String() {
								case "response.done", "response.completed", "response.incomplete":
									terminals++
									if gjson.Get(data, "response.status").String() != "incomplete" || gjson.Get(data, "response.incomplete_details.reason").String() != "max_output_tokens" || gjson.Get(data, "response.usage.total_tokens").Int() != 22 {
										t.Errorf("max_output_tokens lost terminal status or usage: %s", data)
									}
								}
							}
						}
					}
					if terminals != 1 {
						t.Errorf("successful partial response emitted %d terminal events, want one", terminals)
					}
				}
				records := usageRecords()
				if len(records) != 1 {
					t.Fatalf("usage records = %d, want exactly one successful turn record", len(records))
				}
				if records[0].Failed {
					t.Errorf("max_output_tokens published failed usage: %+v", records[0])
				}
				detail := records[0].Detail
				if detail.InputTokens != 17 || detail.OutputTokens != 5 || detail.TotalTokens != 22 || detail.CachedTokens != 3 || detail.ReasoningTokens != 2 {
					t.Errorf("max_output_tokens lost token usage: %+v", detail)
				}
			})
		}
	}
}
