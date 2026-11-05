package openaiwire

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func TestBuildResponsesAssistantHistoryBoundary(t *testing.T) {
	model := &registry.NeoPiModel{ID: "test", Compat: &registry.NeoPiCompat{}}
	body := []byte(`{"input":[{"type":"message","id":"msg_old","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[{"type":"url_citation","url":"https://example.com"}]}],"status":"in_progress","phase":"final_answer"}]}`)
	for _, reconstruct := range []bool{false, true} {
		name := "native"
		if reconstruct {
			name = "reconstructed"
		}
		t.Run(name, func(t *testing.T) {
			ctx, opts := FromResponses(body, model)
			if ctx.ReconstructAssistantHistory {
				t.Fatal("FromResponses must default to native replay")
			}
			ctx.ReconstructAssistantHistory = reconstruct
			item := gjson.GetBytes(BuildResponses(model, ctx, opts), "input.0")
			if item.Get("id").Exists() {
				t.Fatal("unpaired assistant message id survived replay")
			}
			if item.Get("phase").String() != "final_answer" {
				t.Fatalf("phase lost: %s", item.Raw)
			}
			if reconstruct {
				want := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}],"status":"completed","phase":"final_answer"}`
				if item.Raw != want {
					t.Fatalf("reconstructed item\ngot  %s\nwant %s", item.Raw, want)
				}
			} else {
				if item.Get("status").Exists() {
					t.Fatalf("native replay retained output-only status: %s", item.Raw)
				}
				if len(item.Get("content.0.annotations").Array()) != 1 {
					t.Fatalf("native annotations were reconstructed: %s", item.Raw)
				}
			}
		})
	}
}

func TestReconstructedAssistantTextMatchesServerOutputTextOf(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "string convenience shape",
			raw:  `{"role":"assistant","content":"hello","phase":"commentary"}`,
			want: `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}],"status":"completed","phase":"commentary"}]`,
		},
		{
			name: "concatenated text and refusal with unsupported blocks filtered",
			raw:  `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first"},{"type":"input_text","text":"ignored"},{"type":"text","text":" second"},{"type":"refusal","refusal":"no"},{"type":"input_image","image_url":"ignored"}],"phase":"invalid"}`,
			want: `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first second[refusal: no]","annotations":[]}],"status":"completed"}]`,
		},
		{
			name: "phase without an incoming signature id",
			raw:  `{"role":"assistant","content":[{"type":"text","text":" "}],"phase":"final_answer"}`,
			want: `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":" ","annotations":[]}],"status":"completed","phase":"final_answer"}]`,
		},
		{name: "empty string", raw: `{"role":"assistant","content":""}`, want: `[]`},
		{name: "empty array", raw: `{"role":"assistant","content":[]}`, want: `[]`},
		{name: "absent content", raw: `{"role":"assistant"}`, want: `[]`},
		{name: "unsupported content", raw: `{"role":"assistant","content":[{"type":"input_text","text":"ignored"}]}`, want: `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := buildInput([]json.RawMessage{json.RawMessage(tc.raw)}, &registry.NeoPiCompat{}, true, reasoningReplay{})
			got, err := json.Marshal(items)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("input\ngot  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestReconstructedAssistantHistoryDoesNotMarkNativeToolsOrRepairNotesCompleted(t *testing.T) {
	items := buildInput([]json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":"first"}`),
		json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"text","text":"second"}]}`),
		json.RawMessage(`{"type":"function_call","call_id":"call_1","name":"run","arguments":"{}","status":"completed"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"ok"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"orphan","output":"lost"}`),
		json.RawMessage(`{"type":"message","role":"user","content":"continue"}`),
	}, &registry.NeoPiCompat{}, true, reasoningReplay{})
	if len(items) != 6 {
		t.Fatalf("got %d items, want 6: %s", len(items), items)
	}
	for index, text := range []string{"first", "second"} {
		item := gjson.ParseBytes(items[index])
		if item.Get("content.0.text").String() != text || item.Get("status").String() != "completed" {
			t.Fatalf("adjacent assistant messages merged or lost status: %s", items[index])
		}
	}
	for index := 2; index < len(items); index++ {
		if gjson.GetBytes(items[index], "status").Exists() {
			t.Fatalf("non-reconstructed item received status: %s", items[index])
		}
	}
	if note := gjson.GetBytes(items[4], "content").String(); note != "[Orphan tool result; call_id=orphan]: lost" {
		t.Fatalf("orphan repair changed: %s", items[4])
	}
}
