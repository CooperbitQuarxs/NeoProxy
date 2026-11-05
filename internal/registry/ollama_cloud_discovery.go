package registry

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	// ollamaCloudDefaultContext is the context window of a model /api/show
	// reports none for.
	ollamaCloudDefaultContext = 128_000
	// ollamaCloudFallbackMaxTokens is the historical output cap of a model
	// without a trusted limit.
	ollamaCloudFallbackMaxTokens = 8_192
	// ollamaCloudMaxOutputTokens is OLLAMA_CLOUD_MAX_OUTPUT_TOKENS.
	ollamaCloudMaxOutputTokens = 65_536
)

// ollamaCloudOutputCappedBaseIDs is OLLAMA_CLOUD_OUTPUT_CAPPED_BASE_IDS.
var ollamaCloudOutputCappedBaseIDs = map[string]bool{"deepseek-v4-flash": true, "deepseek-v4-pro": true}

// ollamaCloudOutputCapped is isOllamaCloudOutputCapped: the untagged base id
// enforces the 65536 output ceiling.
func ollamaCloudOutputCapped(id string) bool {
	base := id
	if separator := strings.IndexByte(id, ':'); separator > 0 {
		base = id[:separator]
	}
	return ollamaCloudOutputCappedBaseIDs[base]
}

// ollamaCloudContextWindow is getContextWindow: the first numeric
// model_info value whose key ends in .context_length, .num_ctx or
// .context_window.
func ollamaCloudContextWindow(info gjson.Result) (int, bool) {
	window, found := 0, false
	if !info.IsObject() {
		return 0, false
	}
	info.ForEach(func(key, value gjson.Result) bool {
		if value.Type != gjson.Number {
			return true
		}
		name := key.String()
		if strings.HasSuffix(name, ".context_length") || strings.HasSuffix(name, ".num_ctx") || strings.HasSuffix(name, ".context_window") {
			window, found = int(value.Num), true
			return false
		}
		return true
	})
	return window, found
}

// OllamaCloudTagIDs is the ids of an /api/tags listing in its order (an
// entry's model, else its name; a blank one is skipped). ok is false for a
// body NeoPi's mapper cannot read.
func OllamaCloudTagIDs(tags []byte) (entries []gjson.Result, ids []string, ok bool) {
	if !gjson.ValidBytes(tags) {
		return nil, nil, false
	}
	models := gjson.GetBytes(tags, "models")
	if models.Exists() && models.Type != gjson.Null && !models.IsArray() {
		return nil, nil, false
	}
	for _, entry := range models.Array() {
		id := entry.Get("model")
		if !id.Exists() || id.Type == gjson.Null {
			id = entry.Get("name")
		}
		entries = append(entries, entry)
		if id.Type == gjson.String && id.Str != "" {
			ids = append(ids, id.Str)
		} else {
			ids = append(ids, "")
		}
	}
	return entries, ids, true
}

// ollamaCloudSpec is fetchDynamicModels' row for one listed id: capabilities
// from /api/show (or the reference's), the reported context window, and the
// output limit NeoPi trusts for it.
func ollamaCloudSpec(id string, entry gjson.Result, show []byte, bundled map[string]*NeoPiModel) (neoPiSpec, error) {
	reference := bundled[id]
	if reference != nil {
		reference = reference.Clone()
	} else {
		global, err := neoPiGlobalReference(id)
		if err != nil {
			return neoPiSpec{}, err
		}
		reference = global
	}
	providerReference := bundled[id]
	var capabilities gjson.Result
	var context int
	discovered := false
	if show != nil && gjson.ValidBytes(show) {
		capabilities = gjson.GetBytes(show, "capabilities")
		context, discovered = ollamaCloudContextWindow(gjson.GetBytes(show, "model_info"))
	}
	if !discovered {
		context = ollamaCloudDefaultContext
	}
	spec := neoPiSpec{id: id, api: "ollama-chat", omitMaxOutputTokens: true, contextWindow: context}
	if capabilities.IsArray() {
		has := func(name string) bool {
			for _, value := range capabilities.Array() {
				if value.Type == gjson.String && value.Str == name {
					return true
				}
			}
			return false
		}
		spec.reasoning = has("thinking")
		spec.input = []string{"text"}
		if has("vision") {
			spec.input = []string{"text", "image"}
		}
	} else {
		spec.input = []string{"text"}
		if reference != nil {
			spec.reasoning = reference.Reasoning
			spec.thinking, spec.thinkingRequiresEffortSet = reference.Thinking.Clone(), reference.thinkingRequiresEffortSet
			if len(reference.Input) > 0 {
				spec.input = slices.Clone(reference.Input)
			}
		}
	}
	spec.name = id
	if name := entry.Get("name"); name.Type == gjson.String && name.Str != "" && name.Str != id {
		spec.name = name.Str
	} else if reference != nil {
		spec.name = reference.Name
	}
	if reference != nil {
		spec.cost = reference.cost
	}
	switch {
	case ollamaCloudOutputCapped(id):
		spec.maxTokens = min(context, ollamaCloudMaxOutputTokens)
	case discovered && providerReference != nil && providerReference.MaxTokens > 0:
		spec.maxTokens = providerReference.MaxTokens
	default:
		spec.maxTokens = min(context, ollamaCloudFallbackMaxTokens)
	}
	return spec, nil
}

// OllamaCloudListing resolves one /api/tags body, fetched from baseURL, and
// the /api/show body answering each listing position (shows[i] for the i-th
// entry, nil when that call failed) into the roster NeoPi's model manager
// produces: the bundled rows with every listed model merged in, by id and in
// listing order (a listed id rebuilt over its row, an unknown id added), then
// thinking pairs collapsed. A duplicate id keeps each occurrence's own answer,
// as NeoPi's per-entry fetch does. An unusable body is ErrNeoPiListing, after
// which the bundled roster stays; a listing naming no model is a
// *NeoPiEmptyListing.
func OllamaCloudListing(baseURL string, tags []byte, shows [][]byte) ([]*ModelInfo, error) {
	entries, ids, ok := OllamaCloudTagIDs(tags)
	if !ok {
		return nil, fmt.Errorf("%w: %s: unreadable /api/tags body", ErrNeoPiListing, OllamaCloudProvider)
	}
	statics, err := OllamaCloudBundledModels()
	if err != nil {
		return nil, err
	}
	bundled := make(map[string]*NeoPiModel, len(statics))
	for _, row := range statics {
		bundled[row.ID] = row
	}
	infos := func(rows []*NeoPiModel) []*ModelInfo {
		out := make([]*ModelInfo, 0, len(rows))
		for _, row := range rows {
			out = append(out, OllamaCloudModelInfo(row))
		}
		return out
	}
	var specs []neoPiSpec
	for i, id := range ids {
		if id == "" {
			continue
		}
		var show []byte
		if i < len(shows) {
			show = shows[i]
		}
		spec, errSpec := ollamaCloudSpec(id, entries[i], show, bundled)
		if errSpec != nil {
			return nil, errSpec
		}
		specs = append(specs, spec)
	}
	neoPiSortStableByLocale(specs, func(spec neoPiSpec) string { return spec.id })
	if len(specs) == 0 {
		return nil, &NeoPiEmptyListing{Models: infos(statics)}
	}
	// buildDiscoveredModelSpecs: a spec NeoPi cannot build is skipped; a
	// listing none of whose specs build fails.
	type builtSpec struct {
		row  *NeoPiModel
		spec *neoPiSpec
	}
	built := make([]builtSpec, 0, len(specs))
	var firstErr error
	for i := range specs {
		row, errBuild := neoPiBuildRow(OllamaCloudProvider, baseURL, &specs[i])
		if errBuild != nil {
			if firstErr == nil {
				firstErr = errBuild
			}
			continue
		}
		built = append(built, builtSpec{row: row, spec: &specs[i]})
	}
	if len(built) == 0 {
		return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, OllamaCloudProvider, firstErr)
	}
	// mergeDynamicModels: an insertion-ordered map seeded with the bundled
	// rows, each listed row merged over the row its id holds so far.
	rows := slices.Clone(statics)
	at := make(map[string]int, len(rows))
	for i, row := range rows {
		at[row.ID] = i
	}
	for _, dynamic := range built {
		index, exists := at[dynamic.row.ID]
		if !exists {
			at[dynamic.row.ID] = len(rows)
			rows = append(rows, dynamic.row)
			continue
		}
		merged, errMerge := neoPiMergedRow(OllamaCloudProvider, baseURL, &NeoPiCompletionsCatalog{}, rows[index], dynamic.row, dynamic.spec)
		if errMerge != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, OllamaCloudProvider, errMerge)
		}
		rows[index] = merged
	}
	return infos(neoPiCollapseThinkingPairs(OllamaCloudProvider, rows)), nil
}
