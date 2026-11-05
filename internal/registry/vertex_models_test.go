package registry

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// A Vertex Gemini id the bundle lacks must resolve to the row NeoPi's
// buildModel builds for a bare text spec of it under google-vertex, and a
// bundled id to its bundled row (testdata/vertex_npi_profiles.json, from
// capture_vertex_npi_profiles.ts).
func TestVertexNeoPiModelForMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("../runtime/executor/testdata/vertex_npi_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Models     []NeoPiModel `json:"models"`
		Configured []NeoPiModel `json:"configured"`
	}
	if err = json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if len(want.Models) == 0 || len(want.Configured) == 0 {
		t.Fatal("empty capture")
	}
	check := func(label string, expected NeoPiModel, actual *NeoPiModel) {
		t.Helper()
		if actual == nil {
			t.Errorf("%s %s: no row", label, expected.ID)
			return
		}
		if actual.Name != expected.Name || actual.API != expected.API || actual.Provider != expected.Provider || actual.BaseURL != expected.BaseURL ||
			actual.Reasoning != expected.Reasoning || !reflect.DeepEqual(actual.Input, expected.Input) ||
			actual.ContextWindow != expected.ContextWindow || actual.MaxTokens != expected.MaxTokens {
			t.Errorf("%s %s: row = %+v, want %+v", label, expected.ID, *actual, expected)
		}
		if !reflect.DeepEqual(actual.Thinking, expected.Thinking) {
			t.Errorf("%s %s: thinking = %+v, want %+v", label, expected.ID, actual.Thinking, expected.Thinking)
		}
		if !reflect.DeepEqual(actual.GoogleCompat, expected.GoogleCompat) {
			t.Errorf("%s %s: compat = %+v, want %+v", label, expected.ID, actual.GoogleCompat, expected.GoogleCompat)
		}
	}
	for _, expected := range want.Models {
		check("bundled", expected, VertexNeoPiModelFor(expected.ID))
	}
	for _, expected := range want.Configured {
		actual, err := vertexBuildConfigured(expected.ID)
		if err != nil {
			t.Errorf("configured %s: %v", expected.ID, err)
			continue
		}
		check("configured", expected, actual)
	}
}
