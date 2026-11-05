package registry

import (
	_ "embed"
	"slices"
)

// GitLabDuoProvider is NeoPi's provider id for GitLab Duo (non-agentic).
const GitLabDuoProvider = "gitlab-duo"

//go:embed models/gitlab_duo_neopi.json.gz
var gitlabDuoCatalogFile []byte

// gitlabDuoRows is NeoPi's own bundled GitLab Duo rows. Each carries the
// model streamGitLabDuo streams with: the API apiRouteFor routes the id to,
// the upstream id the GitLab proxy takes (RequestModelID), and the thinking
// and compat NeoPi resolves for that route.
var gitlabDuoRows = &neoPiBundledRows{name: "gitlab_duo_neopi.json.gz", file: gitlabDuoCatalogFile}

// GitLabDuoBundledModels is getBundledModels("gitlab-duo"): NeoPi's bundled
// GitLab Duo rows, cloned. GitLab Duo has no discovery; this is its roster.
func GitLabDuoBundledModels() ([]*NeoPiModel, error) {
	return gitlabDuoRows.models()
}

// GitLabDuoModelInfo is the registered model of a GitLab Duo row: its NeoPi
// record, limits, modalities and effort ladder.
func GitLabDuoModelInfo(row *NeoPiModel) *ModelInfo {
	model := row.Clone()
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: GitLabDuoProvider, Type: GitLabDuoProvider,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

// GitLabDuoModels is the GitLab Duo roster as registered models; nil when
// the bundle does not load.
func GitLabDuoModels() []*ModelInfo {
	rows, err := GitLabDuoBundledModels()
	if err != nil {
		return nil
	}
	out := make([]*ModelInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, GitLabDuoModelInfo(row))
	}
	return out
}
