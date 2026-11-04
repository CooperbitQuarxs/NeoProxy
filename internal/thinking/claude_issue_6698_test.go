package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	claude "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

// Model-specific off encodings belong to the NeoPi wire projection, not the
// dialect-independent intent stage. Source-captured executor tests cover the
// final Opus adaptive, Sonnet between_tools and Haiku disabled bodies.
func TestClaudeThinkingOffPreservesIntentForWireProjection(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"thinking":{"type":"adaptive","display":"omitted","budget_tokens":8000,"block_binding":{"prefix_mismatch_behavior":"drop_block"}},"output_config":{"effort":"max","format":{"type":"json_schema"}}}`)
			out, err := claude.NewApplier().Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, &registry.ModelInfo{ID: model})
			if err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(out, "thinking").Raw; got != `{"type":"disabled"}` {
				t.Fatalf("off intent = %s", out)
			}
			if gjson.GetBytes(out, "output_config.effort").Exists() || !gjson.GetBytes(out, "output_config.format").Exists() {
				t.Fatalf("unrelated output configuration changed: %s", out)
			}
		})
	}
}

func TestClaudeThinkingManualBudgetIsDeferredToNeoPi(t *testing.T) {
	out, err := claude.NewApplier().Apply([]byte(`{"max_tokens":64}`), thinking.ThinkingConfig{Mode: thinking.ModeBudget, Budget: 8192}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "thinking.type").String() != "enabled" || gjson.GetBytes(out, "thinking.budget_tokens").Int() != 8192 || gjson.GetBytes(out, "max_tokens").Int() != 64 {
		t.Fatalf("intent stage clamped tokens before NeoPi's output budget calculation: %s", out)
	}
}
