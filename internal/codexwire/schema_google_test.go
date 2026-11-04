package codexwire

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/tidwall/gjson"
)

// toolWireSchema and normalizeSchemaForGoogle reproduce NeoPi's output byte
// for byte over schemas reaching every branch of the Google option set
// (testdata/capture_google_schema.ts).
func TestNormalizeSchemaForGoogleMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/google_schema_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Cases []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
			Wire       string          `json:"wire"`
			Google     string          `json:"google"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			wire := ToolWireSchema(gjson.ParseBytes(tc.Parameters))
			if string(wire) != tc.Wire {
				t.Fatalf("wire:\n got %s\nwant %s", wire, tc.Wire)
			}
			if got := NormalizeSchemaForGoogle(wire); string(got) != tc.Google {
				t.Errorf("google:\n got %s\nwant %s", got, tc.Google)
			}
		})
	}
}
