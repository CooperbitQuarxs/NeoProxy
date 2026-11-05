package executor

import (
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// The apply_patch round trip over a Gemini-shaped upstream, shared by the
// Google executors' tests: a Responses request declaring the custom tool, the
// upstream function call answering it, and the client-facing assertions.
const executorPatchRequest = `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"start: patch"}}]}],"input":"patch a file"}`
const executorPatchResponse = `{"responseId":"patch","candidates":[{"content":{"parts":[{"functionCall":{"name":"functions__apply_patch","args":{"input":"  *** Begin Patch\n*** End Patch\n "}}}]},"finishReason":"STOP"}]}`
const executorPatchInput = "  *** Begin Patch\n*** End Patch\n "

func assertExecutorPatchDeclaration(t *testing.T, payload []byte) {
	t.Helper()
	declaration := gjson.GetBytes(payload, "tools.0.functionDeclarations.0")
	if declaration.Get("name").String() != "functions__apply_patch" || !strings.Contains(declaration.Get("description").String(), "*** Begin Patch") || !strings.Contains(declaration.Get("description").String(), "start: patch") || declaration.Get("parametersJsonSchema.properties.input.type").String() != "string" || !declaration.Get("parametersJsonSchema.additionalProperties").Exists() || declaration.Get("parametersJsonSchema.additionalProperties").Bool() {
		t.Fatalf("missing standard patch declaration: %s", payload)
	}
}

func assertExecutorPatchOutput(t *testing.T, payload []byte) {
	t.Helper()
	item := gjson.GetBytes(payload, `output.#(type=="custom_tool_call")`)
	if item.Get("type").String() != "custom_tool_call" || item.Get("input").String() != executorPatchInput || item.Get("name").String() != "apply_patch" || item.Get("namespace").String() != "functions" {
		t.Fatalf("wrong patch output: %s", payload)
	}
}

func assertExecutorPatchStream(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk, acknowledgeSnapshot ...func()) {
	t.Helper()
	acknowledged := len(acknowledgeSnapshot) == 0
	var delta strings.Builder
	var done, item, final gjson.Result
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		for _, line := range strings.Split(string(chunk.Payload), "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			ev := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			switch ev.Get("type").String() {
			case "response.output_text.delta":
				if !acknowledged {
					acknowledged = true
					acknowledgeSnapshot[0]()
				}
			case "response.custom_tool_call_input.delta":
				if !acknowledged {
					t.Fatal("fabricated patch progress before upstream snapshot")
				}
				delta.WriteString(ev.Get("delta").String())
			case "response.custom_tool_call_input.done":
				done = ev
			case "response.output_item.done":
				if ev.Get("item.type").String() == "custom_tool_call" {
					item = ev.Get("item")
				}
			case "response.completed":
				final = ev.Get("response")
			}
		}
	}
	if !acknowledged || delta.String() != executorPatchInput || done.Get("input").String() != executorPatchInput || item.Get("input").String() != executorPatchInput || done.Get("item_id").String() != item.Get("id").String() || done.Get("call_id").String() != item.Get("call_id").String() {
		t.Fatalf("inconsistent stream delta=%q done=%s item=%s", delta.String(), done.Raw, item.Raw)
	}
	assertExecutorPatchOutput(t, []byte(final.Raw))
}
