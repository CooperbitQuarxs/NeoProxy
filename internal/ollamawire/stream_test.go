package ollamawire

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
)

// runtimeCapture is testdata/ollama_cloud_runtime.json: NeoPi's OWN
// ollama-cloud streamSimple over gateway-parsed chat bodies against a
// scripted /api/chat (capture_ollama_cloud_runtime.ts).
type runtimeCapture struct {
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
		Events []struct {
			Type  string  `json:"type"`
			Delta *string `json:"delta"`
			Name  string  `json:"name"`
		} `json:"events"`
		Final struct {
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
			Content      []struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"content"`
			Usage struct {
				Input       int64 `json:"input"`
				Output      int64 `json:"output"`
				CacheRead   int64 `json:"cacheRead"`
				TotalTokens int64 `json:"totalTokens"`
			} `json:"usage"`
		} `json:"final"`
	} `json:"scenarios"`
}

func loadRuntimeCapture(t *testing.T) runtimeCapture {
	t.Helper()
	data, err := os.ReadFile("testdata/ollama_cloud_runtime.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture runtimeCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Scenarios) == 0 {
		t.Fatal("capture has no scenarios")
	}
	return capture
}

// recordingSink records the content events the stream emits, in NeoPi's
// event vocabulary.
type recordingSink struct{ events []string }

func (s *recordingSink) TextDelta(delta string) { s.events = append(s.events, "text_delta "+delta) }
func (s *recordingSink) ThinkingDelta(delta string) {
	s.events = append(s.events, "thinking_delta "+delta)
}
func (s *recordingSink) ToolCallStart(block *cursorwire.OutBlock) {
	s.events = append(s.events, "toolcall_start "+block.Name)
}
func (s *recordingSink) ToolCallDelta(_ *cursorwire.OutBlock, delta string) {
	s.events = append(s.events, "toolcall_delta "+delta)
}
func (s *recordingSink) ToolCallEnd(block *cursorwire.OutBlock) {
	s.events = append(s.events, "toolcall_end "+block.Name)
}

// The NDJSON body of each scenario's final reply streams the content events
// and settles the assistant message NeoPi's streamOllamaOnce did: thinking,
// healed markup, structured and leaked tool calls, done reasons, usage with
// the cached split, and the empty length/load and in-band error turns.
func TestReadStreamMatchesNeoPi(t *testing.T) {
	capture := loadRuntimeCapture(t)
	for _, tc := range capture.Scenarios {
		t.Run(tc.Name, func(t *testing.T) {
			reply := tc.Replies[len(tc.Replies)-1]
			if reply.Status != 200 {
				t.Skip("no stream: the request failed before one")
			}
			var model struct {
				Model string `json:"model"`
			}
			_ = json.Unmarshal([]byte(tc.Body), &model)
			sink := &recordingSink{}
			result, err := ReadStream(strings.NewReader(reply.Body), neopitaxonomy.Classify("ollama-cloud", model.Model).Class, sink)
			stopReason, message := "", ""
			if err != nil {
				stopReason, message = "error", err.Error()
			} else {
				stopReason, message = result.Output.StopReason, result.ErrorMessage
			}
			if stopReason != tc.Final.StopReason || message != tc.Final.ErrorMessage {
				t.Fatalf("settled %s %q, want %s %q", stopReason, message, tc.Final.StopReason, tc.Final.ErrorMessage)
			}
			var want []string
			for _, event := range tc.Events {
				switch event.Type {
				case "text_delta", "thinking_delta", "toolcall_delta":
					want = append(want, event.Type+" "+*event.Delta)
				case "toolcall_start", "toolcall_end":
					want = append(want, event.Type+" "+event.Name)
				}
			}
			if !reflect.DeepEqual(sink.events, want) {
				t.Errorf("events:\n got %q\nwant %q", sink.events, want)
			}
			if err != nil {
				return
			}
			if len(result.Output.Content) != len(tc.Final.Content) {
				t.Fatalf("content = %d blocks, want %d", len(result.Output.Content), len(tc.Final.Content))
			}
			for i, block := range result.Output.Content {
				want := tc.Final.Content[i]
				if block.Type != want.Type || block.Name != want.Name {
					t.Errorf("block %d = %s %s, want %s %s", i, block.Type, block.Name, want.Type, want.Name)
				}
				if block.Type == "toolCall" {
					if got := cursorwire.StringifyJSON(block.Arguments); got != want.Arguments {
						t.Errorf("block %d arguments = %s, want %s", i, got, want.Arguments)
					}
					if strings.HasPrefix(want.ID, "ollama:") && block.ID != want.ID {
						t.Errorf("block %d id = %s, want %s", i, block.ID, want.ID)
					}
					continue
				}
				if block.Text != want.Text {
					t.Errorf("block %d text = %q, want %q", i, block.Text, want.Text)
				}
			}
			usage := result.Output.Usage
			if usage.Input != tc.Final.Usage.Input || usage.Output != tc.Final.Usage.Output || usage.CacheRead != tc.Final.Usage.CacheRead || usage.Total != tc.Final.Usage.TotalTokens {
				t.Errorf("usage = %+v, want %+v", usage, tc.Final.Usage)
			}
		})
	}
}
