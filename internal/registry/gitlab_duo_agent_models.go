package registry

import (
	"slices"
	"strings"
)

// GitLabDuoAgentProvider is NeoPi's provider id for GitLab Duo Agent.
const GitLabDuoAgentProvider = "gitlab-duo-agent"

const (
	gitlabDuoAgentBaseURL = "https://gitlab.com"
	// gitlabDuoAgentSeedID, Name and Window are NeoPi's static seed row
	// (seedModels("gitlab-duo-agent")[0]).
	gitlabDuoAgentSeedID     = "claude_sonnet_4_6_vertex"
	gitlabDuoAgentSeedName   = "Claude Sonnet 4.6 - Vertex"
	gitlabDuoAgentSeedWindow = 1_000_000
)

// GitLabDuoAgentRow is buildModel over buildGitLabDuoWorkflowModelSpec: a
// workflow model under its GitLab ref, text only, no reasoning knob (the
// Duo Agent Platform fixes model parameters server-side), free, with no
// output limit of its own.
func GitLabDuoAgentRow(id, name, baseURL string, contextWindow int) *NeoPiModel {
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = gitlabDuoAgentBaseURL
	}
	return &NeoPiModel{ID: id, Name: name, API: GitLabDuoAgentProvider, Provider: GitLabDuoAgentProvider, BaseURL: base,
		Input: []string{"text"}, ContextWindow: contextWindow, OmitMaxOutputTokens: true}
}

// GitLabDuoAgentModelInfo is the registered model of a GitLab Duo Agent row.
func GitLabDuoAgentModelInfo(row *NeoPiModel) *ModelInfo {
	model := row.Clone()
	return &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: GitLabDuoAgentProvider, Type: GitLabDuoAgentProvider,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
}

// GitLabDuoAgentSeedModels is buildGitLabDuoWorkflowFallbackModel: the one
// row an account serves until its namespace's models are discovered.
func GitLabDuoAgentSeedModels() []*ModelInfo {
	return []*ModelInfo{GitLabDuoAgentModelInfo(GitLabDuoAgentRow(gitlabDuoAgentSeedID, gitlabDuoAgentSeedName, gitlabDuoAgentBaseURL, gitlabDuoAgentSeedWindow))}
}
