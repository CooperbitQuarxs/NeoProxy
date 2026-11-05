package registry

import (
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/tidwall/gjson"
)

// NeoPiModelsDevURL is MODELS_DEV_URL: the models.dev catalog as
// catalog.stencil.so serves it (a field-pruned copy, zstd-compressed).
const NeoPiModelsDevURL = "https://catalog.stencil.so/models.json.zstd"

// neoPiModelsDevReferences is the reference map a manager builds from the
// models.dev catalog for provider (loadSiliconFlowModelsDevReferences):
// mapModelsDevToModels over an openai-completions descriptor that keeps every
// model, keyed by id by createModelsDevReferenceMap. A payload that does not
// parse, or names no such provider, yields no references.
func neoPiModelsDevReferences(provider, baseURL string, payload []byte) map[string]*NeoPiModel {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return nil
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		return nil
	}
	var models gjson.Result
	root.ForEach(func(key, value gjson.Result) bool {
		if key.String() == provider {
			models = value.Get("models")
			if !value.IsObject() {
				models = gjson.Result{}
			}
		}
		return true
	})
	if !models.IsObject() {
		return nil
	}
	references := map[string]*NeoPiModel{}
	models.ForEach(func(key, raw gjson.Result) bool {
		id := key.String()
		if !raw.IsObject() {
			return true
		}
		if kind := raw.Get("kind"); kind.Exists() {
			// A kind other than chat needs the provider's runner API for it;
			// these providers author none.
			if !neoPiModelsDevChatKind(provider, baseURL, id, kind) {
				return true
			}
		}
		row := &NeoPiModel{
			ID:            id,
			Name:          neoPiModelName(raw.Get("name"), id),
			API:           "openai-completions",
			Provider:      provider,
			BaseURL:       baseURL,
			Reasoning:     raw.Get("reasoning").Type == gjson.True,
			Input:         neoPiModelsDevInput(raw.Get("modalities.input")),
			ContextWindow: neoPiPositive(raw.Get("limit.context"), 0),
			MaxTokens:     neoPiPositive(raw.Get("limit.output"), 0),
			cost: NeoPiCost{
				Input:      neoPiModelsDevRate(raw.Get("cost.input")),
				Output:     neoPiModelsDevRate(raw.Get("cost.output")),
				CacheRead:  neoPiModelsDevRate(raw.Get("cost.cache_read")),
				CacheWrite: neoPiModelsDevRate(raw.Get("cost.cache_write")),
			},
		}
		// createModelsDevReferenceMap keeps the larger context window.
		if existing := references[id]; existing == nil || row.ContextWindow > existing.ContextWindow {
			references[id] = row
		}
		return true
	})
	return references
}

// neoPiModelsDevChatKind reports whether a row that authors a kind stays a
// chat row: the compat rules' kind wins over the authored one, which must
// be a known kind.
func neoPiModelsDevChatKind(provider, baseURL, id string, authored gjson.Result) bool {
	kind := ""
	if policy, err := neopicompat.Resolve(neopicompat.Spec{Provider: provider, API: "openai-completions", ID: id, BaseURL: baseURL}); err == nil {
		kind, _ = policy.Catalog["kind"].(string)
	}
	if kind == "" {
		if authored.Type != gjson.String {
			return false
		}
		kind = authored.String()
	}
	return slices.Contains(neoPiModelKinds, kind) && kind == "chat"
}

// neoPiModelsDevInput is toInputCapabilities.
func neoPiModelsDevInput(modalities gjson.Result) []string {
	if modalities.IsArray() {
		for _, item := range modalities.Array() {
			if item.Type == gjson.String && item.String() == "image" {
				return []string{"text", "image"}
			}
		}
	}
	return []string{"text"}
}

// neoPiModelsDevRate is toNumber(value) ?? 0.
func neoPiModelsDevRate(value gjson.Result) float64 {
	if number, ok := neoPiToNumber(value); ok {
		return number
	}
	return 0
}

// neoPiMapSiliconFlow is SiliconFlow's mapModel: a models.dev reference for
// the id merges like a bundled one; otherwise a bundled upstream row found
// through NeoPi's reference index lends its capabilities and limits (never
// its pricing); otherwise the listing defaults stand.
func neoPiMapSiliconFlow(entry gjson.Result, defaults neoPiSpec, modelsDev map[string]*NeoPiModel) neoPiSpec {
	if reference := modelsDev[defaults.id]; reference != nil {
		return neoPiMapWithBundledReference(entry, defaults, reference)
	}
	canonical := neoPiResolveModelReference(defaults.id)
	if canonical == nil {
		return defaults
	}
	spec := defaults
	spec.name = neoPiModelName(entry.Get("name"), canonical.Name)
	spec.reasoning = canonical.Reasoning
	spec.input = slices.Clone(canonical.Input)
	contextWindow := canonical.ContextWindow
	if contextWindow == 0 {
		contextWindow = defaults.contextWindow
	}
	spec.contextWindow = contextWindow
	switch {
	case canonical.MaxTokens != 0 && contextWindow != 0:
		spec.maxTokens = min(canonical.MaxTokens, contextWindow)
	case canonical.MaxTokens != 0:
		spec.maxTokens = canonical.MaxTokens
	default:
		spec.maxTokens = defaults.maxTokens
	}
	return spec
}
