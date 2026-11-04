package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

// kimiNeoPiJSON is NeoPi's bundled kimi-code catalog as the Anthropic Messages
// records its shim sends, exported by
// runtime/executor/testdata/capture_kimi_npi_profiles.ts --registry. NeoPi
// serves an account from it until that account's discovery answers.
//
//go:embed models/kimi_neopi.json
var kimiNeoPiJSON []byte

var kimiBundledCatalog = func() []NeoPiModel {
	var export struct {
		Models []NeoPiModel `json:"models"`
	}
	if err := json.Unmarshal(kimiNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: kimi_neopi.json: %v", err))
	}
	return export.Models
}()

// KimiAnthropicBaseURL is where NeoPi sends Kimi Code models whose API format
// is anthropic: the coding OpenAI base without its /v1 suffix.
const KimiAnthropicBaseURL = "https://api.kimi.com/coding"

// neoPiThinkingEfforts is THINKING_EFFORTS, least to most intensive.
var neoPiThinkingEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// kimiAnthropicCompat is the compat NeoPi resolves for every Kimi Code row the
// OpenAI/Anthropic shim rebuilds as an Anthropic Messages model
// (capture_kimi_npi_profiles.ts). The rows differ only in
// requiresThinkingEnabled, which never changes a request whose thinking is
// already mandatory.
var kimiAnthropicCompat = NeoPiAnthropicCompat{
	InjectClaudeCodeInstruction: true,
	ReplayUnsignedThinking:      true,
	SupportsContextManagement:   true,
	SupportsOutputEffort:        true,
	SupportsSamplingParams:      true,
	SupportsForcedToolChoice:    true,
}

// kimiMaxTokens is kimiCodeMaxTokens: the documented per-family output caps
// (runtime/behavior.kdl model-limits), else KIMI_CODE_DEFAULT_MAX_TOKENS.
func kimiMaxTokens(id string) int {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "k3", "k3-256k", "k3.1-pro", "k3.1-pro-highspeed":
		return 131072
	case "kimi-for-coding", "kimi-for-coding-highspeed":
		return 32768
	}
	return 32000
}

// KimiNeoPiModel is one /coding/v1/models row as NeoPi resolves it: the
// kimiCodeModelManagerOptions mapper, the kimi-code rules, and the shim's
// Anthropic Messages rebuild (thinking mode adaptive when the row has an
// effort ladder). Every row without a protocol takes the rule default,
// Anthropic Messages.
func KimiNeoPiModel(entry gjson.Result) NeoPiModel {
	id := entry.Get("id").String()
	name := id
	if display := entry.Get("display_name"); display.Type == gjson.String {
		name = display.String()
	}
	reasoning := false
	switch entry.Get("supports_thinking_type").String() {
	case "only", "both":
		reasoning = true
	case "no":
	default:
		reasoning = entry.Get("supports_reasoning").Type == gjson.True
	}
	input := []string{"text"}
	// K2.5's vision support is rule-owned (/models omits it).
	if entry.Get("supports_image_in").Type == gjson.True || strings.HasPrefix(strings.ToLower(id), "kimi-k2.5") {
		input = []string{"text", "image"}
	}
	contextWindow := 262144
	if length := entry.Get("context_length"); length.Type == gjson.Number {
		contextWindow = int(length.Int())
	}
	model := NeoPiModel{ID: id, Name: name, API: "anthropic-messages", Provider: "kimi-code", BaseURL: KimiAnthropicBaseURL,
		Reasoning: reasoning, Input: input, ContextWindow: contextWindow, MaxTokens: kimiMaxTokens(id)}
	if efforts := entry.Get("think_efforts"); reasoning && efforts.Get("support").Type == gjson.True && efforts.Get("valid_efforts").IsArray() {
		var valid []string
		for _, effort := range efforts.Get("valid_efforts").Array() {
			valid = append(valid, effort.String())
		}
		var ladder []string
		for _, effort := range neoPiThinkingEfforts {
			if slices.Contains(valid, effort) {
				ladder = append(ladder, effort)
			}
		}
		if len(ladder) > 0 {
			model.Thinking = &NeoPiThinking{Efforts: ladder, Mode: "anthropic-adaptive", RequiresEffort: entry.Get("supports_thinking_type").String() == "only"}
		}
	}
	compat := kimiAnthropicCompat
	model.AnthropicCompat = &compat
	return model
}

// KimiModels maps a /coding/v1/models envelope onto registry models.
func KimiModels(envelope []byte) []*ModelInfo {
	rows := gjson.GetBytes(envelope, "data").Array()
	out := make([]*ModelInfo, 0, len(rows))
	for _, row := range rows {
		if row.Get("id").String() == "" {
			continue
		}
		model := KimiNeoPiModel(row)
		out = append(out, kimiModelInfo(&model))
	}
	return out
}

// KimiBundledModels is NeoPi's bundled kimi-code catalog as registry models.
func KimiBundledModels() []*ModelInfo {
	out := make([]*ModelInfo, 0, len(kimiBundledCatalog))
	for i := range kimiBundledCatalog {
		out = append(out, kimiModelInfo(&kimiBundledCatalog[i]))
	}
	return out
}

// KimiBundledNeoPiModel resolves an id against NeoPi's bundled kimi-code
// catalog; nil when the bundle does not carry it.
func KimiBundledNeoPiModel(id string) *NeoPiModel {
	for i := range kimiBundledCatalog {
		if kimiBundledCatalog[i].ID == id {
			return kimiBundledCatalog[i].Clone()
		}
	}
	return nil
}

func kimiModelInfo(model *NeoPiModel) *ModelInfo {
	info := &ModelInfo{ID: model.ID, Object: "model", OwnedBy: "kimi", Type: "kimi", Name: model.ID, DisplayName: model.Name,
		ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model.Clone()}
	if model.Thinking != nil {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: true}
	}
	return info
}
