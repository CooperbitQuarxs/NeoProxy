package helps

import (
	"strings"
	"testing"
)

// Tool names follow the CLI's JavaScript semantics: its non-Unicode regex
// replaces every UTF-16 code unit outside [a-zA-Z0-9_-], so a character
// outside the BMP becomes two underscores, and name length is counted in
// UTF-16 code units.
func TestFactoryDroidToolNamesFollowJavaScript(t *testing.T) {
	if got := FactoryDroidSanitizeToolName("a😀", 64); got != "a__" {
		t.Errorf("sanitize(a😀) = %q, want a__", got)
	}
	if got := FactoryDroidSanitizeToolName("é.x", 64); got != "__x" {
		t.Errorf("sanitize(é.x) = %q, want __x", got)
	}
	encode := func(name string) string { return FactoryDroidSanitizeToolName(name, 64) }
	byWire, err := FactoryDroidToolNamesByWire([]string{"a😀", "a_"}, encode)
	if err != nil {
		t.Fatalf("a😀 and a_ collide: %v", err)
	}
	if byWire["a__"] != "a😀" || byWire["a_"] != "a_" {
		t.Errorf("wire names = %v, want a__ and a_ apart", byWire)
	}
	// 40 UTF-16 units (80 UTF-8 bytes) is within a 64-unit bound: verbatim.
	name := strings.Repeat("é", 40)
	if got := FactoryDroidCompletionsToolName(name, 64); got != name {
		t.Errorf("completions name of 40 units = %q, want it verbatim", got)
	}
}
