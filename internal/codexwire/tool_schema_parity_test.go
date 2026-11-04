package codexwire

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
)

// Function tools reach the wire exactly as NeoPi's own converter emits them:
// draft-07 upgrades, wire post-processing, Responses sanitizing and strict-mode
// enforcement with its non-strict fallbacks.
func TestFunctionToolsMatchNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_tool_schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Fixtures []struct {
			Name       string          `json:"name"`
			Downstream json.RawMessage `json:"downstream"`
			Wire       json.RawMessage `json:"wire"`
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			got := convertTools([]json.RawMessage{fixture.Downstream})
			if len(got) != 1 {
				t.Fatalf("converted %d tools", len(got))
			}
			if want := jsjson.Normalize(fixture.Wire); string(got[0]) != string(want) {
				t.Errorf("wire tool\ngot    %s\nsource %s", got[0], want)
			}
		})
	}
}
