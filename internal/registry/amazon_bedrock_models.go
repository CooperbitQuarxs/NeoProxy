package registry

import (
	_ "embed"
	"slices"
)

// AmazonBedrockProvider is NeoPi's provider id for Amazon Bedrock's Converse API.
const AmazonBedrockProvider = "amazon-bedrock"

//go:embed models/amazon_bedrock_neopi.json.gz
var amazonBedrockCatalogFile []byte

// amazonBedrockRows is NeoPi's own bundled Amazon Bedrock rows:
// bedrock-converse-stream models with their effort ladders and resolved compat.
var amazonBedrockRows = &neoPiBundledRows{name: "amazon_bedrock_neopi.json.gz", file: amazonBedrockCatalogFile}

// AmazonBedrockBundledModels is getBundledModels("amazon-bedrock"): NeoPi's
// bundled Amazon Bedrock rows, cloned.
func AmazonBedrockBundledModels() ([]*NeoPiModel, error) {
	return amazonBedrockRows.models()
}

// AmazonBedrockModelInfo is the registered model of an Amazon Bedrock row:
// its NeoPi record, limits, modalities and effort ladder.
func AmazonBedrockModelInfo(row *NeoPiModel) *ModelInfo {
	model := row.Clone()
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: AmazonBedrockProvider, Type: AmazonBedrockProvider,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

// AmazonBedrockModels is the bundled Amazon Bedrock roster as registered
// models; nil when the bundle does not load.
func AmazonBedrockModels() []*ModelInfo {
	rows, err := AmazonBedrockBundledModels()
	if err != nil {
		return nil
	}
	out := make([]*ModelInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, AmazonBedrockModelInfo(row))
	}
	return out
}
