package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
)

// vertexNeoPiJSON is NeoPi's bundled `google-vertex` Gemini catalog (the rows
// it serves over Vertex AI's generateContent), exported by
// runtime/executor/testdata/capture_vertex_npi_profiles.ts. NeoPi has no
// discovery for the provider; this roster is what it serves.
//
//go:embed models/google_vertex_neopi.json
var vertexNeoPiJSON []byte

// VertexProvider is NeoPi's provider id for Vertex AI.
const VertexProvider = "google-vertex"

// VertexGeminiBaseURL is the base NeoPi's google-vertex Gemini rows carry; a
// request derives its host from the resolved location instead.
const VertexGeminiBaseURL = "https://{location}-aiplatform.googleapis.com"

var vertexBundledCatalog = func() []NeoPiModel {
	var export struct {
		Models []NeoPiModel `json:"models"`
	}
	if err := json.Unmarshal(vertexNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: google_vertex_neopi.json: %v", err))
	}
	return export.Models
}()

// VertexBundledModels is NeoPi's bundled google-vertex Gemini catalog as
// registry models.
func VertexBundledModels() []*ModelInfo {
	out := make([]*ModelInfo, 0, len(vertexBundledCatalog))
	for i := range vertexBundledCatalog {
		out = append(out, VertexModelInfo(&vertexBundledCatalog[i]))
	}
	return out
}

// VertexNeoPiModelFor is the NeoPi record for a Vertex Gemini model id: the
// bundled row, else the row NeoPi's buildModel resolves for a bare
// non-reasoning text spec of the id under google-vertex (how NeoProxy builds
// every NeoPi provider's configured id). Nil when the rules cannot build it.
func VertexNeoPiModelFor(id string) *NeoPiModel {
	for i := range vertexBundledCatalog {
		if vertexBundledCatalog[i].ID == id {
			return vertexBundledCatalog[i].Clone()
		}
	}
	row, err := vertexBuildConfigured(id)
	if err != nil {
		return nil
	}
	return row
}

// vertexBuildConfigured is buildModel for a bare google-vertex spec of id.
func vertexBuildConfigured(id string) (*NeoPiModel, error) {
	policy, err := neopicompat.Resolve(neopicompat.Spec{Provider: VertexProvider, API: neopicompat.APIGoogleVertex, ID: id, BaseURL: VertexGeminiBaseURL})
	if err != nil {
		return nil, err
	}
	row := &NeoPiModel{ID: id, Name: cleanModelName(id), API: neopicompat.APIGoogleVertex, Provider: VertexProvider,
		BaseURL: VertexGeminiBaseURL, Reasoning: policy.Reasoning, Input: []string{"text"}, GoogleCompat: &NeoPiGoogleCompat{}}
	if err = neoPiDecodePolicy(policy.Compat, row.GoogleCompat); err != nil {
		return nil, err
	}
	if policy.Thinking != nil {
		row.Thinking = &NeoPiThinking{}
		if err = neoPiDecodePolicy(policy.Thinking, row.Thinking); err != nil {
			return nil, err
		}
		_, row.thinkingRequiresEffortSet = policy.Thinking["requiresEffort"]
	}
	neoPiApplyCatalog(row, policy.Catalog)
	return row, nil
}

// VertexModelInfo materializes a Vertex Gemini row as a registered model.
func VertexModelInfo(model *NeoPiModel) *ModelInfo {
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: "google", Type: "vertex", DisplayName: model.Name,
		ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model.Clone()}
	if model.Reasoning && model.Thinking != nil {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort || model.Thinking.SuppressWhenOff}
	}
	return info
}
