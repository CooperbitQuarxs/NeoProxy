package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// xaiNeoPiJSON is NeoPi's resolved xAI catalogs, exported by
// runtime/executor/testdata/capture_xai_npi_profiles.ts: the xai-oauth seed
// (catalog KDL run through buildModel) and the paid `xai` provider's bundle.
//
//go:embed models/xai_neopi.json
var xaiNeoPiJSON []byte

type xaiNeoPiExport struct {
	Models       []NeoPiModel `json:"models"`
	APIKeyModels []NeoPiModel `json:"apiKeyModels"`
}

var xaiNeoPiCatalogs = func() xaiNeoPiExport {
	var export xaiNeoPiExport
	if err := json.Unmarshal(xaiNeoPiJSON, &export); err != nil {
		panic(fmt.Sprintf("registry: xai_neopi.json: %v", err))
	}
	return export
}()

var xaiOAuthSeed = xaiNeoPiCatalogs.Models

// isXaiOAuthExcluded is NeoPi's exclude-models rule for xai-oauth: media
// models the chat picker cannot serve.
func isXaiOAuthExcluded(id string) bool {
	switch id {
	case "grok-imagine-image-quality", "grok-imagine-image-2.0":
		return true
	}
	for _, prefix := range []string{"grok-imagine-video", "grok-stt-", "grok-voice-"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	return false
}

// XaiOAuthModels is NeoPi's applyXAIOAuthCuration over a discovered
// /v1/models roster: curated seeds first in declaration order (injected when
// the roster is non-empty but omits them), then every other chat id in roster
// order. Runner seeds (image, TTS, STT) and excluded media ids never join the
// chat catalog. A nil roster (discovery has not answered) yields the static seed.
func XaiOAuthModels(listed []string) []*ModelInfo {
	var curated []NeoPiModel
	runners := map[string]bool{}
	for _, seed := range xaiOAuthSeed {
		if seed.API == "openai-responses" {
			curated = append(curated, seed)
		} else {
			runners[seed.ID] = true
		}
	}
	if listed == nil {
		out := make([]*ModelInfo, 0, len(curated))
		for i := range curated {
			out = append(out, curated[i].xaiModelInfo())
		}
		return out
	}
	var filtered []string
	for _, id := range listed {
		if !runners[id] && !isXaiOAuthExcluded(id) && !slices.Contains(filtered, id) {
			filtered = append(filtered, id)
		}
	}
	var out []*ModelInfo
	curatedIDs := map[string]bool{}
	for i := range curated {
		curatedIDs[curated[i].ID] = true
		if len(filtered) > 0 {
			out = append(out, curated[i].xaiModelInfo())
		}
	}
	for _, id := range filtered {
		if !curatedIDs[id] {
			out = append(out, xaiOAuthDiscoveredModel(id).xaiModelInfo())
		}
	}
	return out
}

// xaiOAuthDiscoveredModel is a roster id with no curated seed. NeoPi maps it
// with fetchOpenAICompatibleModels' defaults (not reasoning, text input,
// unknown limits) and withXaiOAuthCompatDefaults; the provider-wide compat of
// a non-reasoning seed stands in for the catalog's compat resolution, which
// only differs on reasoning fields that a non-reasoning model never sends.
// [INFERENCE] NeoPi's bundled models.dev reference may still enrich such ids;
// that reference is not ported.
func xaiOAuthDiscoveredModel(id string) NeoPiModel {
	return xaiDiscoveredModel(id, "xai-oauth", xaiOAuthSeed)
}

func xaiDiscoveredModel(id, provider string, catalog []NeoPiModel) NeoPiModel {
	model := NeoPiModel{ID: id, Name: id, API: "openai-responses", Provider: provider, BaseURL: "https://api.x.ai/v1", Input: []string{"text"}}
	for _, seed := range catalog {
		if seed.API == "openai-responses" && !seed.Reasoning && seed.Compat != nil {
			compat := *seed.Compat
			model.Compat = &compat
			break
		}
	}
	return model
}

// XaiAPIKeyModels is NeoPi's paid `xai` catalog for an API key: every bundled
// chat row, plus roster ids the bundle lacks (mergeDynamicModels keeps the
// static rows; the provider's discovery is not authoritative).
func XaiAPIKeyModels(listed []string) []*ModelInfo {
	var out []*ModelInfo
	bundled := map[string]bool{}
	for _, model := range xaiNeoPiCatalogs.APIKeyModels {
		bundled[model.ID] = true
		if model.API == "openai-responses" {
			out = append(out, model.xaiModelInfo())
		}
	}
	for _, id := range listed {
		if !bundled[id] {
			bundled[id] = true
			out = append(out, xaiDiscoveredModel(id, "xai", xaiNeoPiCatalogs.APIKeyModels).xaiModelInfo())
		}
	}
	return out
}

// XaiAPIKeyNeoPiModel resolves a configured API-key model id to NeoPi's
// record: the bundled row, else a discovered-style default.
func XaiAPIKeyNeoPiModel(id string) *NeoPiModel {
	for i := range xaiNeoPiCatalogs.APIKeyModels {
		if model := &xaiNeoPiCatalogs.APIKeyModels[i]; model.ID == id {
			if model.API != "openai-responses" {
				return nil
			}
			return model.Clone()
		}
	}
	model := xaiDiscoveredModel(id, "xai", xaiNeoPiCatalogs.APIKeyModels)
	return &model
}

func (m NeoPiModel) xaiModelInfo() *ModelInfo {
	model := m.Clone()
	info := &ModelInfo{ID: model.ID, Object: "model", OwnedBy: "xai", Type: "xai", Name: model.ID, DisplayName: model.Name,
		ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	if model.Reasoning && model.Thinking != nil {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: true}
	}
	return info
}
