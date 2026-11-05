package registry

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
)

// GitHubCopilotProvider is NeoPi's provider id for GitHub Copilot.
const GitHubCopilotProvider = "github-copilot"

//go:embed models/github_copilot_neopi.json.gz
var githubCopilotCatalogFile []byte

// githubCopilotCatalog is models/github_copilot_neopi.json.gz: NeoPi's own
// bundled Copilot rows, each with the compat NeoPi resolved for its API.
type githubCopilotCatalog struct {
	Models []struct {
		ID             string          `json:"id"`
		Name           string          `json:"name"`
		API            string          `json:"api"`
		Provider       string          `json:"provider"`
		BaseURL        string          `json:"baseUrl"`
		Reasoning      bool            `json:"reasoning"`
		Input          []string        `json:"input"`
		ContextWindow  int             `json:"contextWindow"`
		MaxTokens      int             `json:"maxTokens"`
		RequestModelID string          `json:"requestModelId"`
		Thinking       json.RawMessage `json:"thinking"`
		Compat         json.RawMessage `json:"compat"`
		Cost           NeoPiCost       `json:"cost"`
	} `json:"models"`
}

var (
	githubCopilotOnce    sync.Once
	githubCopilotRows    []*NeoPiModel
	githubCopilotLoadErr error
)

func loadGitHubCopilotModels() {
	reader, err := gzip.NewReader(bytes.NewReader(githubCopilotCatalogFile))
	if err != nil {
		githubCopilotLoadErr = err
		return
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		githubCopilotLoadErr = err
		return
	}
	var catalog githubCopilotCatalog
	if err = json.Unmarshal(data, &catalog); err != nil {
		githubCopilotLoadErr = fmt.Errorf("registry: github_copilot_neopi.json.gz: %w", err)
		return
	}
	rows := make([]*NeoPiModel, 0, len(catalog.Models))
	for _, entry := range catalog.Models {
		thinking, requiresEffortSet, errThinking := neoPiDecodeThinking(entry.Thinking)
		if errThinking != nil {
			githubCopilotLoadErr = fmt.Errorf("registry: github-copilot %s: %w", entry.ID, errThinking)
			return
		}
		row := &NeoPiModel{ID: entry.ID, Name: entry.Name, API: entry.API, Provider: entry.Provider, BaseURL: entry.BaseURL,
			Reasoning: entry.Reasoning, Input: entry.Input, ContextWindow: entry.ContextWindow, MaxTokens: entry.MaxTokens,
			Thinking: thinking, RequestModelID: entry.RequestModelID, cost: entry.Cost, thinkingRequiresEffortSet: requiresEffortSet}
		// The compat belongs to the row's own API.
		var target any
		switch entry.API {
		case "anthropic-messages":
			row.AnthropicCompat = &NeoPiAnthropicCompat{}
			target = row.AnthropicCompat
		case "openai-completions":
			row.CompletionsCompat = &NeoPiCompletionsCompat{}
			target = row.CompletionsCompat
		case "openai-responses":
			row.Compat = &NeoPiCompat{}
			target = row.Compat
		default:
			githubCopilotLoadErr = fmt.Errorf("registry: github-copilot %s: unknown api %s", entry.ID, entry.API)
			return
		}
		if errCompat := json.Unmarshal(entry.Compat, target); errCompat != nil {
			githubCopilotLoadErr = fmt.Errorf("registry: github-copilot %s compat: %w", entry.ID, errCompat)
			return
		}
		rows = append(rows, row)
	}
	githubCopilotRows = rows
}

// GitHubCopilotBundledModels is getBundledModels("github-copilot"): NeoPi's
// bundled Copilot rows, cloned.
func GitHubCopilotBundledModels() ([]*NeoPiModel, error) {
	githubCopilotOnce.Do(loadGitHubCopilotModels)
	if githubCopilotLoadErr != nil {
		return nil, githubCopilotLoadErr
	}
	out := make([]*NeoPiModel, 0, len(githubCopilotRows))
	for _, row := range githubCopilotRows {
		out = append(out, row.Clone())
	}
	return out, nil
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
