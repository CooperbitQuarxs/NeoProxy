package openaiwire

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func TestBuildResponsesNativeUserImagesBypassNonVisionGuard(t *testing.T) {
	model := &registry.NeoPiModel{ID: "non-vision", Input: []string{"text"}, Compat: &registry.NeoPiCompat{}}
	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"https://example.com/user.png","detail":"original"}]},{"type":"message","role":"developer","content":[{"type":"input_image","file_id":"file_native"}]},{"type":"message","role":"assistant","content":[{"type":"input_image","image_url":"https://example.com/constructed.png"}]},{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"https://example.com/tool.png"}]}]}`)
	for _, native := range []bool{false, true} {
		name := "constructed"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			ctx, opts := FromResponses(body, model)
			if ctx.NativeUserHistory {
				t.Fatal("FromResponses must leave source provenance to the caller")
			}
			if gjson.GetBytes(ctx.Input[0], "content.1.type").String() != "input_image" {
				t.Fatal("FromResponses irreversibly guarded images before provenance was set")
			}
			ctx.NativeUserHistory = native
			input := gjson.GetBytes(BuildResponses(model, ctx, opts), "input").Array()
			if len(input) != 5 {
				t.Fatalf("got %d input items, want 5", len(input))
			}
			for index, imageIndex := range []int{1, 0} {
				content := input[index].Get("content").Array()
				part := content[imageIndex]
				if native {
					if part.Get("type").String() != "input_image" {
						t.Fatalf("native role image omitted: %s", input[index].Raw)
					}
				} else if part.Get("type").String() != "input_text" || part.Get("text").String() != nonVisionImagePlaceholder {
					t.Fatalf("constructed role image escaped vision guard: %s", input[index].Raw)
				}
			}
			if native {
				if detail := input[0].Get("content.1.detail").String(); detail != "auto" {
					t.Fatalf("native image must still pass replay compatibility adaptation: %s", input[0].Raw)
				}
				if file := input[1].Get("content.0.file_id").String(); file != "file_native" {
					t.Fatalf("native developer file reference lost: %s", input[1].Raw)
				}
			}
			for _, path := range []string{"2.content.0", "4.output.0"} {
				part := gjson.GetBytes(BuildResponses(model, ctx, opts), "input."+path)
				if part.Get("type").String() != "input_text" || part.Get("text").String() != nonVisionImagePlaceholder {
					t.Fatalf("non-user or tool image escaped guard at %s: %s", path, part.Raw)
				}
			}
		})
	}
}
