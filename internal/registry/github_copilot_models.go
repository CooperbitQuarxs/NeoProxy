package registry

import (
	_ "embed"
	"slices"
)

// GitHubCopilotProvider is NeoPi's provider id for GitHub Copilot.
const GitHubCopilotProvider = "github-copilot"

//go:embed models/github_copilot_neopi.json.gz
var githubCopilotCatalogFile []byte

// githubCopilotRows is NeoPi's own bundled Copilot rows, each with the
// compat NeoPi resolved for its API.
var githubCopilotRows = &neoPiBundledRows{name: "github_copilot_neopi.json.gz", file: githubCopilotCatalogFile}

// GitHubCopilotBundledModels is getBundledModels("github-copilot"): NeoPi's
// bundled Copilot rows, cloned.
func GitHubCopilotBundledModels() ([]*NeoPiModel, error) {
	return githubCopilotRows.models()
}

// GitHubCopilotPolicyModelIDs are the wire ids NeoPi enables a policy for
// after sign-in: each bundled row's request id, once, in bundle order.
func GitHubCopilotPolicyModelIDs() []string {
	rows, err := GitHubCopilotBundledModels()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, row := range rows {
		id := row.ID
		if row.RequestModelID != "" {
			id = row.RequestModelID
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// GitHubCopilotModelInfo is the registered model of a Copilot row: its
// NeoPi record, limits, modalities and effort ladder.
func GitHubCopilotModelInfo(row *NeoPiModel) *ModelInfo {
	model := row.Clone()
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: GitHubCopilotProvider, Type: GitHubCopilotProvider,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

// GitHubCopilotSeedModels is the bundled roster NeoPi serves until discovery
// answers, as registered models; nil when the bundle does not load.
func GitHubCopilotSeedModels() []*ModelInfo {
	rows, err := GitHubCopilotBundledModels()
	if err != nil {
		return nil
	}
	out := make([]*ModelInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, GitHubCopilotModelInfo(row))
	}
	return out
}
