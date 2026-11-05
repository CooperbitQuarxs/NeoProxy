package registry

import (
	"fmt"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/githubcopilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
	"github.com/tidwall/gjson"
)

// NeoPi's GitHub Copilot model discovery (pi-catalog provider-models/
// openai-compat.ts githubCopilotModelManagerOptions) and the model manager's
// merge of its listing into the bundled roster. The HTTP requests live with
// the caller; parity against NeoPi's own manager is captured by
// testdata/capture_github_copilot_discovery.ts.

// githubCopilotLongContextIDSuffix and githubCopilotLongContextNameSuffix
// mark a synthesized long-context sibling (COPILOT_LONG_CONTEXT_*_SUFFIX).
const (
	githubCopilotLongContextIDSuffix   = "-1m"
	githubCopilotLongContextNameSuffix = " (1M)"
)

// githubCopilotPriceTier is one billing.token_prices tier; prices are
// hundredths of a dollar per 1M tokens. A has* flag is false where NeoPi's
// toNumber found nothing.
type githubCopilotPriceTier struct {
	contextMax, inputPrice, outputPrice, cachePrice float64
	hasContextMax, hasInput, hasOutput, hasCache    bool
}

// githubCopilotTier is parseCopilotTokenPriceTier; ok is false for a
// non-object.
func githubCopilotTier(value gjson.Result) (githubCopilotPriceTier, bool) {
	if !value.IsObject() {
		return githubCopilotPriceTier{}, false
	}
	var tier githubCopilotPriceTier
	if number, ok := neoPiToNumber(value.Get("max_prompt_tokens")); ok {
		tier.contextMax, tier.hasContextMax = number, true
	} else if number, ok = neoPiToNumber(value.Get("context_max")); ok {
		tier.contextMax, tier.hasContextMax = number, true
	}
	tier.inputPrice, tier.hasInput = neoPiToNumber(value.Get("input_price"))
	tier.outputPrice, tier.hasOutput = neoPiToNumber(value.Get("output_price"))
	tier.cachePrice, tier.hasCache = neoPiToNumber(value.Get("cache_price"))
	return tier, true
}

// githubCopilotTierCost is copilotTierCost: the tier's tariff in dollars per
// 1M tokens, cache writes kept from fallback; ok is false without both an
// input and an output price.
func githubCopilotTierCost(tier githubCopilotPriceTier, present bool, cacheWrite float64) (NeoPiCost, bool) {
	if !present || !tier.hasInput || !tier.hasOutput {
		return NeoPiCost{}, false
	}
	cost := NeoPiCost{Input: tier.inputPrice / 100, Output: tier.outputPrice / 100, CacheWrite: cacheWrite}
	if tier.hasCache {
		cost.CacheRead = tier.cachePrice / 100
	}
	return cost, true
}

// githubCopilotReference is resolveReference: the bundled Copilot row for id,
// else NeoPi's global bundled reference.
func githubCopilotReference(bundled map[string]*NeoPiModel, id string) (*NeoPiModel, error) {
	if row := bundled[id]; row != nil {
		return row.Clone(), nil
	}
	return neoPiGlobalReference(id)
}

// githubCopilotMap is the manager's mapModel for one listed entry served from
// requestBaseURL, with the long-context sibling it synthesizes; ok is false
// for a non-chat model.
func githubCopilotMap(entry gjson.Result, requestBaseURL string, bundled map[string]*NeoPiModel) (base neoPiSpec, variant *neoPiSpec, ok bool, err error) {
	capabilities := entry.Get("capabilities")
	if capabilities.IsObject() {
		// isCopilotChatModel: Copilot's /models mixes chat and embeddings.
		if kind := capabilities.Get("type"); kind.Type == gjson.String && kind.String() != "chat" {
			return neoPiSpec{}, nil, false, nil
		}
	}
	defaults := neoPiDefaultSpec(entry)
	reference, err := githubCopilotReference(bundled, defaults.id)
	if err != nil {
		return neoPiSpec{}, nil, false, err
	}
	var limits gjson.Result
	if capabilities.IsObject() && capabilities.Get("limits").IsObject() {
		limits = capabilities.Get("limits")
	}
	// The total window first; max_prompt_tokens is Copilot's prompt budget
	// and only a fallback when no window field is reported.
	windowFallback, outputFallback := defaults.contextWindow, defaults.maxTokens
	if reference != nil && reference.ContextWindow > 0 {
		windowFallback = reference.ContextWindow
	}
	if reference != nil && reference.MaxTokens > 0 {
		outputFallback = reference.MaxTokens
	}
	contextWindow := neoPiPositive(limits.Get("max_context_window_tokens"), neoPiPositive(entry.Get("context_length"),
		neoPiPositive(limits.Get("max_prompt_tokens"), windowFallback)))
	maxTokens := neoPiPositive(limits.Get("max_output_tokens"), neoPiPositive(entry.Get("max_completion_tokens"),
		neoPiPositive(limits.Get("max_non_streaming_output_tokens"), outputFallback)))
	name := defaults.name
	if reference != nil {
		name = reference.Name
	}
	if listed := entry.Get("name"); listed.Type == gjson.String && neoPiTrimmedNonEmpty(listed.String()) {
		name = listed.String()
	}
	api, err := neoPiAPIRouteFor(GitHubCopilotProvider, defaults.id)
	if err != nil {
		return neoPiSpec{}, nil, false, err
	}
	if api != neopicompat.APIAnthropicMessages && api != neopicompat.APIOpenAIResponses {
		api = neopicompat.APIOpenAICompletions
	}
	// Copilot pre-applies the image fallback for an omitted supports.vision:
	// off the personal host the row is text-only.
	input := defaults.input
	if reference != nil {
		input = reference.Input
	}
	vision := capabilities.Get("supports.vision")
	if !capabilities.IsObject() || !capabilities.Get("supports").IsObject() {
		vision = gjson.Result{}
	}
	switch {
	case vision.Type == gjson.True:
		input = []string{"text", "image"}
	case vision.Type == gjson.False || requestBaseURL != githubcopilot.PersonalBaseURL:
		input = []string{"text"}
	}
	// The served window is the long-context ceiling; the base row is capped to
	// the default tier, the long tier being the opt-in -1m sibling. On tiered
	// legacy rows the prompt budget may be tighter than the billed default.
	var defaultTier, longTier githubCopilotPriceTier
	var hasDefaultTier, hasLongTier bool
	if billing := entry.Get("billing"); billing.IsObject() {
		if prices := billing.Get("token_prices"); prices.IsObject() {
			defaultTier, hasDefaultTier = githubCopilotTier(prices.Get("default"))
			longTier, hasLongTier = githubCopilotTier(prices.Get("long_context"))
		}
	}
	promptBudget, hasPromptBudget := neoPiToNumber(limits.Get("max_prompt_tokens"))
	tieredPromptBudget := 0.0
	if hasLongTier && longTier.hasContextMax && longTier.contextMax > 0 && hasPromptBudget {
		tieredPromptBudget = promptBudget
	}
	defaultContextMax, hasDefaultContextMax := defaultTier.contextMax, hasDefaultTier && defaultTier.hasContextMax
	if tieredPromptBudget > 0 && (!hasDefaultContextMax || defaultContextMax <= 0 || tieredPromptBudget < defaultContextMax) {
		defaultContextMax, hasDefaultContextMax = tieredPromptBudget, true
	}
	defaultTierWindow := contextWindow
	if hasDefaultContextMax && defaultContextMax > 0 && contextWindow > 0 && maxTokens > 0 {
		defaultTierWindow = min(contextWindow, int(defaultContextMax)+maxTokens)
	}

	if reference != nil {
		base = neoPiSpec{id: defaults.id, reasoning: reference.Reasoning, thinking: reference.Thinking.Clone(),
			thinkingRequiresEffortSet: reference.thinkingRequiresEffortSet, requestModelID: reference.RequestModelID,
			kind: reference.Kind, cost: reference.cost}
		if reference.Provider != GitHubCopilotProvider {
			// A cross-provider reference's wire routing does not transfer.
			base.requestModelID = ""
			if base.thinking != nil {
				base.thinking.EffortRouting = nil
			}
		}
	} else {
		base = defaults
		// /models advertises no reasoning bit: a Claude past the thinking cut
		// with no bundled reference is marked so the ladder is derived.
		if api == neopicompat.APIAnthropicMessages {
			if identity := neopitaxonomy.Classify(GitHubCopilotProvider, defaults.id); identity.Class == "anthropic" && identity.AtLeast("3.7") {
				base.reasoning = true
			}
		}
	}
	base.api, base.baseURL, base.name, base.input = api, "", name, slices.Clone(input)
	base.contextWindow, base.maxTokens = defaultTierWindow, maxTokens
	base.compat = nil
	if api == neopicompat.APIOpenAICompletions {
		base.compat = map[string]any{"supportsStore": false, "supportsDeveloperRole": false, "supportsReasoningEffort": false}
	}
	if cost, ok := githubCopilotTierCost(defaultTier, hasDefaultTier, base.cost.CacheWrite); ok {
		base.cost = cost
	}
	// createCopilotLongContextVariant: the long tier as a local sibling that
	// keeps the upstream id on the wire.
	if hasLongTier && longTier.hasContextMax && longTier.contextMax > 0 && contextWindow > 0 && maxTokens > 0 {
		variantWindow := min(contextWindow, int(longTier.contextMax)+maxTokens)
		if base.contextWindow > 0 && variantWindow > base.contextWindow {
			long := base
			long.input, long.thinking = slices.Clone(base.input), base.thinking.Clone()
			long.id, long.requestModelID = base.id+githubCopilotLongContextIDSuffix, base.id
			long.name, long.contextWindow = base.name+githubCopilotLongContextNameSuffix, variantWindow
			if cost, ok := githubCopilotTierCost(longTier, true, base.cost.CacheWrite); ok {
				long.cost = cost
			}
			variant = &long
		}
	}
	return base, variant, true, nil
}

func neoPiTrimmedNonEmpty(value string) bool {
	for _, r := range value {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' && r != '\f' && r != '\v' && r != 0xA0 && r != 0xFEFF && r != 0x2028 && r != 0x2029 {
			return true
		}
	}
	return false
}

// GitHubCopilotListing resolves Copilot's GET /models body, fetched from
// requestBaseURL, into the roster NeoPi's model manager produces: the bundled
// rows with every listed chat model merged in (a listed id rebuilt over its
// bundled row, an unknown id added), each listed model with a long-context
// tier gaining its -1m sibling. An unusable body is ErrNeoPiListing, after
// which NeoPi keeps the bundled roster; a listing naming no chat model is a
// *NeoPiEmptyListing.
func GitHubCopilotListing(requestBaseURL string, body []byte) ([]*ModelInfo, error) {
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("%w: %s: unreadable body", ErrNeoPiListing, GitHubCopilotProvider)
	}
	entries, ok := neoPiExtractEntries(gjson.ParseBytes(body))
	if !ok {
		return nil, fmt.Errorf("%w: %s: no model list", ErrNeoPiListing, GitHubCopilotProvider)
	}
	statics, err := GitHubCopilotBundledModels()
	if err != nil {
		return nil, err
	}
	bundled := make(map[string]*NeoPiModel, len(statics))
	for _, row := range statics {
		bundled[row.ID] = row
	}
	var specs, variants []neoPiSpec
	index := map[string]int{}
	for _, entry := range entries {
		spec, variant, chat, errMap := githubCopilotMap(entry, requestBaseURL, bundled)
		if errMap != nil {
			return nil, errMap
		}
		if !chat {
			continue
		}
		if at, dup := index[spec.id]; dup {
			specs[at] = spec
		} else {
			index[spec.id] = len(specs)
			specs = append(specs, spec)
		}
		if variant != nil {
			variants = append(variants, *variant)
		}
	}
	// A real upstream id wins over a synthesized sibling of the same id.
	taken := map[string]bool{}
	for _, spec := range specs {
		taken[spec.id] = true
	}
	for _, variant := range variants {
		if !taken[variant.id] {
			taken[variant.id] = true
			specs = append(specs, variant)
		}
	}
	neoPiSortStableByLocale(specs, func(spec neoPiSpec) string { return spec.id })

	infos := func(rows []*NeoPiModel) []*ModelInfo {
		out := make([]*ModelInfo, 0, len(rows))
		for _, row := range rows {
			out = append(out, GitHubCopilotModelInfo(row))
		}
		return out
	}
	if len(specs) == 0 {
		return nil, &NeoPiEmptyListing{Models: infos(statics)}
	}
	// buildDiscoveredModelSpecs: a spec NeoPi cannot build is skipped; a
	// listing none of whose specs build fails.
	built := make([]*NeoPiModel, 0, len(specs))
	builtSpecs := make(map[string]*neoPiSpec, len(specs))
	var firstErr error
	for i := range specs {
		row, errBuild := neoPiBuildRow(GitHubCopilotProvider, requestBaseURL, &specs[i])
		if errBuild != nil {
			if firstErr == nil {
				firstErr = errBuild
			}
			continue
		}
		built = append(built, row)
		builtSpecs[row.ID] = &specs[i]
	}
	if len(built) == 0 {
		return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, GitHubCopilotProvider, firstErr)
	}
	byID := make(map[string]*NeoPiModel, len(built))
	for _, row := range built {
		byID[row.ID] = row
	}
	// mergeDynamicModels: Copilot's live input modality is authoritative.
	authority := &NeoPiCompletionsCatalog{}
	authority.Discovery.InputAuthoritative = true
	rows := make([]*NeoPiModel, 0, len(statics)+len(built))
	seen := map[string]bool{}
	for _, static := range statics {
		dynamic := byID[static.ID]
		if dynamic == nil {
			rows = append(rows, static)
			continue
		}
		seen[static.ID] = true
		merged, errMerge := neoPiMergedRow(GitHubCopilotProvider, requestBaseURL, authority, static, dynamic, builtSpecs[static.ID])
		if errMerge != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, GitHubCopilotProvider, errMerge)
		}
		rows = append(rows, merged)
	}
	for _, row := range built {
		if !seen[row.ID] {
			rows = append(rows, row)
		}
	}
	return infos(rows), nil
}
