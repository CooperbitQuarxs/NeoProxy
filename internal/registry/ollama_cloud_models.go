package registry

import (
	_ "embed"
	"slices"
)

// OllamaCloudProvider is NeoPi's provider id for Ollama Cloud.
const OllamaCloudProvider = "ollama-cloud"

//go:embed models/ollama_cloud_neopi.json.gz
var ollamaCloudCatalogFile []byte

// ollamaCloudRows is NeoPi's own bundled Ollama Cloud rows: ollama-chat
// models with their effort ladders, resolved compat and omitted output
// limits.
var ollamaCloudRows = &neoPiBundledRows{name: "ollama_cloud_neopi.json.gz", file: ollamaCloudCatalogFile}

// OllamaCloudBundledModels is getBundledModels("ollama-cloud"): NeoPi's
// bundled Ollama Cloud rows, cloned. They are the roster until discovery
// answers, and the references discovery reads names, costs and limits from.
func OllamaCloudBundledModels() ([]*NeoPiModel, error) {
	return ollamaCloudRows.models()
}

// OllamaCloudModelInfo is the registered model of an Ollama Cloud row: its
// NeoPi record, limits, modalities and effort ladder.
func OllamaCloudModelInfo(row *NeoPiModel) *ModelInfo {
	model := row.Clone()
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: OllamaCloudProvider, Type: OllamaCloudProvider,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

// OllamaCloudModels is the bundled Ollama Cloud roster as registered models;
// nil when the bundle does not load.
func OllamaCloudModels() []*ModelInfo {
	rows, err := OllamaCloudBundledModels()
	if err != nil {
		return nil
	}
	out := make([]*ModelInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, OllamaCloudModelInfo(row))
	}
	return out
}
