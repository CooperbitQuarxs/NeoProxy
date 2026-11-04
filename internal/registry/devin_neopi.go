package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// devinNeoPiJSON is NeoPi's bundled devin seed as NeoPiModel records, exported
// by runtime/executor/testdata/capture_devin_npi_models.ts. An account serves
// it until its GetCliModelConfigs discovery answers.
//
//go:embed models/devin_neopi.json
var devinNeoPiJSON []byte

var devinBundledCatalog = func() []NeoPiModel {
	var export struct {
		Models []NeoPiModel `json:"models"`
	}
	if err := json.Unmarshal(devinNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: devin_neopi.json: %v", err))
	}
	return export.Models
}()

// DevinBundledModels is NeoPi's devin seed as registry models.
func DevinBundledModels() []*ModelInfo {
	out := make([]*ModelInfo, 0, len(devinBundledCatalog))
	for i := range devinBundledCatalog {
		out = append(out, DevinModelInfo(&devinBundledCatalog[i]))
	}
	return out
}

// DevinBundledNeoPiModel resolves an id against NeoPi's devin seed.
func DevinBundledNeoPiModel(id string) *NeoPiModel {
	for i := range devinBundledCatalog {
		if devinBundledCatalog[i].ID == id {
			return devinBundledCatalog[i].Clone()
		}
	}
	return nil
}

// DevinModelInfo is one NeoPi devin record as a registry model.
func DevinModelInfo(model *NeoPiModel) *ModelInfo {
	info := &ModelInfo{ID: model.ID, Object: "model", OwnedBy: "devin", Type: "devin", Name: model.ID, DisplayName: model.Name,
		ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model.Clone()}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}
