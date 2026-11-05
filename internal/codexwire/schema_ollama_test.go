package codexwire

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/tidwall/gjson"
)

// toolWireSchema and sanitizeSchemaForOllama reproduce NeoPi's output byte
// for byte over schemas reaching every branch of the Ollama sanitizer
// (testdata/capture_ollama_schema.ts).
func TestSanitizeSchemaForOllamaMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/ollama_schema_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Cases []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
			Wire       string          `json:"wire"`
			Ollama     string          `json:"ollama"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Cases) == 0 {
		t.Fatal("capture has no cases")
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			wire := ToolWireSchema(gjson.ParseBytes(tc.Parameters))
			if string(wire) != tc.Wire {
				t.Fatalf("wire:\n got %s\nwant %s", wire, tc.Wire)
			}
			if got := SanitizeSchemaForOllama(wire); string(got) != tc.Ollama {
				t.Errorf("ollama:\n got %s\nwant %s", got, tc.Ollama)
			}
		})
	}
}
