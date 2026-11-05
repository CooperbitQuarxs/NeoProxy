package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestNeoPiResponsesSourceIncludes(t *testing.T) {
	body := []byte(`{"include":["reasoning.encrypted_content","web_search_call.action.sources"],"input":[]}`)
	conversation, options := openaiwire.FromResponses(body, nil)
	if len(options.Include) != 2 || options.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("FromResponses discarded client include: %v", options.Include)
	}
	// Codex overwrites client includes with its synthetic encrypted-content default.
	translated := []byte(`{"include":["reasoning.encrypted_content"],"input":[]}`)
	conversation, options = openaiwire.FromResponses(translated, nil)
	restoreNeoPiSourceIntent(sdktranslator.FormatOpenAIResponse, body, &conversation, &options)
	if len(options.Include) != 2 || options.Include[0] != "reasoning.encrypted_content" || options.Include[1] != "web_search_call.action.sources" {
		t.Fatalf("source include = %v", options.Include)
	}
	conversation, options = openaiwire.FromResponses(translated, nil)
	restoreNeoPiSourceIntent(sdktranslator.FormatOpenAI, []byte(`{}`), &conversation, &options)
	if len(options.Include) != 0 {
		t.Fatalf("synthetic include remains: %v", options.Include)
	}
}

func TestNeoPiResponsesSummaryNoneRestoresSuppression(t *testing.T) {
	body := restoreResponsesSourceFields([]byte(`{"reasoning":{"summary":"none"}}`), []byte(`{"reasoning":{"effort":"high","summary":"auto"}}`))
	summary := gjson.GetBytes(body, "reasoning.summary")
	if !summary.Exists() || summary.Type != gjson.Null {
		t.Fatalf("summary = %s, want explicit null", body)
	}
	_, options := openaiwire.FromResponses(body, nil)
	if !options.SummarySet || options.ReasoningSummary != "" {
		t.Fatalf("summary suppression lost: %+v", options)
	}
}
