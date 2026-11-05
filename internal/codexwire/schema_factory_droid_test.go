package codexwire

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/tidwall/gjson"
)

// FactoryDroidToolSchema reproduces NeoPi's Factory Gemini projection byte for
// byte over schemas reaching its union, conjunction, reference, nullable and
// type-inference branches (testdata/capture_factory_droid_schemas.ts).
func TestFactoryDroidToolSchemaMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/factory_droid_schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Cases []struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
			Wire       string          `json:"wire"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if got := FactoryDroidToolSchema(gjson.ParseBytes(tc.Parameters)); string(got) != tc.Wire {
				t.Errorf("schema:\n got %s\nwant %s", got, tc.Wire)
			}
		})
	}
}
