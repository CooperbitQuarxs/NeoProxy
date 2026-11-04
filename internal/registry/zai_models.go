package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// zaiNeoPiJSON is NeoPi's resolved `zai` catalog, exported by
// runtime/executor/testdata/capture_zai_npi_profiles.ts. Z.ai has no live
// discovery in NeoPi: the bundled rows, minus its exclude-models rule, are the
// roster, each on the API NeoPi routes it to.
//
//go:embed models/zai_neopi.json
var zaiNeoPiJSON []byte

var zaiNeoPiCatalog = func() []NeoPiModel {
	var export struct {
		Models []NeoPiModel `json:"models"`
	}
	if err := json.Unmarshal(zaiNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: zai_neopi.json: %v", err))
	}
	return export.Models
}()

// ZaiModels is NeoPi's Z.ai catalog as registry models for one Z.ai account,
// owned by its openai-compatibility entry name.
func ZaiModels(ownedBy string) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(zaiNeoPiCatalog))
	for i := range zaiNeoPiCatalog {
		model := zaiNeoPiCatalog[i].Clone()
		info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: ownedBy, Type: "openai-compatibility",
			DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
			SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
		if model.Reasoning && model.Thinking != nil {
			info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: true}
		}
		out = append(out, info)
	}
	return out
}

// ZaiNeoPiModel resolves a Z.ai model id to NeoPi's record; nil when NeoPi's
// catalog does not serve it.
func ZaiNeoPiModel(id string) *NeoPiModel {
	for i := range zaiNeoPiCatalog {
		if zaiNeoPiCatalog[i].ID == id {
			return zaiNeoPiCatalog[i].Clone()
		}
	}
	return nil
}
