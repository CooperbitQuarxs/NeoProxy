package registry

import (
	"slices"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicatalog"
)

// CursorNeoPiModel is one resolved cursor row as the NeoPi record the
// executor routes from.
func CursorNeoPiModel(spec *neopicatalog.Spec) *NeoPiModel {
	model := &NeoPiModel{ID: spec.ID, Name: spec.Name, API: spec.API, Provider: spec.Provider, BaseURL: spec.BaseURL,
		Reasoning: spec.Reasoning, Input: slices.Clone(spec.Input)}
	if spec.ContextWindow != nil {
		model.ContextWindow = *spec.ContextWindow
	}
	if spec.MaxTokens != nil {
		model.MaxTokens = *spec.MaxTokens
	}
	if spec.RequestModelID != nil {
		model.RequestModelID = *spec.RequestModelID
	}
	if t := spec.Thinking; t != nil {
		model.Thinking = &NeoPiThinking{Efforts: slices.Clone(t.Efforts), Mode: t.Mode, RequiresEffort: t.RequiresEffort,
			EffortRouting: t.EffortRouting, DefaultLevel: t.DefaultLevel}
	}
	routed := spec.Clone()
	model.Cursor = &NeoPiCursor{MaxMode: routed.CursorMaxMode, MaxModeRoutes: routed.CursorMaxModeRoutes, ModelParameters: routed.CursorModelParameters,
		ModelRoutes: routed.CursorModelRoutes, RequiresToolSchemaProjection: spec.RequiresCursorToolSchemaProjection != nil && *spec.RequiresCursorToolSchemaProjection}
	return model
}

// CursorModelInfo is one resolved cursor row as a registry model.
func CursorModelInfo(spec *neopicatalog.Spec) *ModelInfo {
	model := CursorNeoPiModel(spec)
	info := &ModelInfo{ID: model.ID, Object: "model", OwnedBy: "cursor", Type: "cursor", Name: model.ID, DisplayName: model.Name,
		ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

func cursorModelInfos(roster []*neopicatalog.Spec) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(roster))
	for _, spec := range roster {
		out = append(out, CursorModelInfo(spec))
	}
	return out
}

// CursorBundledModels is the roster NeoPi serves before discovery answers:
// its bundled cursor catalog.
func CursorBundledModels() []*ModelInfo {
	roster, _ := CursorRoster(CursorDiscoveryResponses{}, "", time.Now())
	return cursorModelInfos(roster)
}

// CursorBundledNeoPiModel resolves an id against the bundled roster.
func CursorBundledNeoPiModel(id string) *NeoPiModel {
	roster, _ := CursorRoster(CursorDiscoveryResponses{}, "", time.Now())
	for _, spec := range roster {
		if spec.ID == id {
			return CursorNeoPiModel(spec)
		}
	}
	return nil
}

// CursorDiscoveredModels resolves an account's discovery bodies into its
// roster; ok is false when neither model-list RPC answered.
func CursorDiscoveredModels(responses CursorDiscoveryResponses, baseURLOverride string, now time.Time) ([]*ModelInfo, bool) {
	roster, ok := CursorRoster(responses, baseURLOverride, now)
	if !ok {
		return nil, false
	}
	return cursorModelInfos(roster), true
}
