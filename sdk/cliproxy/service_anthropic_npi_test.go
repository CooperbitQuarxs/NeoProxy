package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestAnthropicNPIModelsArePrefixOnly(t *testing.T) {
	original := registry.GetClaudeModels()
	experimental := registry.GetAnthropicNPIModels()
	if len(original) != len(experimental) {
		t.Fatal("experimental provider lost Claude model ids")
	}
	published := applyModelPrefixes(experimental, "anthropic-npi", true)
	if len(published) != len(original) {
		t.Fatal("experimental provider published extra bare model ids")
	}
	found := false
	for index, model := range published {
		if model.ID != "anthropic-npi/"+original[index].ID {
			t.Fatalf("model routing differs: %s", model.ID)
		}
		if model.ID == "anthropic-npi/claude-opus-5-5" {
			found = true
		}
	}
	if !found {
		t.Fatal("distinct Opus 5.5 address is absent")
	}
	for index, model := range registry.GetClaudeModels() {
		if strings.HasPrefix(model.ID, "anthropic-npi/") {
			t.Fatal("existing Claude model id changed")
		}
		if original[index].Thinking != nil && original[index].Thinking.ZeroAllowed != model.Thinking.ZeroAllowed {
			t.Fatal("experimental model capability changed existing Claude")
		}
	}
}
