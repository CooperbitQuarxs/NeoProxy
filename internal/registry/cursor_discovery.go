package registry

import (
	"encoding/binary"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicatalog"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// This file ports NeoPi's Cursor model resolution: pi-catalog
// discovery/cursor.ts fetchCursorUsableModels (the GetUsableModels,
// AvailableModels and GetDefaultModelForCli join) and the model manager's
// merge with the bundled catalog, authoritative retention and
// collapseBuiltVariants (model-manager.ts resolveProviderModels).

const (
	cursorDefaultContextWindow = 200_000
	cursorDefaultMaxTokens     = 64_000
	cursor1MContextWindow      = 1_000_000
)

// CursorDiscoveryResponses are the bodies of the three discovery RPCs; a nil
// body is a failed request.
type CursorDiscoveryResponses struct {
	Usable, Available, Default []byte
}

// CursorRoster resolves an account's Cursor roster the way NeoPi's model
// manager does. ok is false when neither model-list RPC decoded; the roster
// is then the bundled catalog.
func CursorRoster(responses CursorDiscoveryResponses, baseURLOverride string, now time.Time) (roster []*neopicatalog.Spec, ok bool) {
	policy := cursorPolicy{now: now.UnixMilli()}
	discovered, ok := cursorDiscoveredSpecs(responses, baseURLOverride, policy)
	models := slices.Clone(cursorCatalog.bundled)
	if ok {
		dynamic := make([]*neopicatalog.Spec, 0, len(discovered))
		for _, spec := range discovered {
			dynamic = append(dynamic, policy.build(spec))
		}
		models = cursorRetain(cursorMergeDynamic(models, dynamic, policy), dynamic)
	}
	return cursorCollapseBuilt(models, policy), ok
}

// cursorCollapseBuilt is collapseBuiltVariants: rows collapsing created are
// rebuilt; rows that passed through are kept as they are.
func cursorCollapseBuilt(models []*neopicatalog.Spec, policy cursorPolicy) []*neopicatalog.Spec {
	inputs := map[*neopicatalog.Spec]bool{}
	for _, model := range models {
		inputs[model] = true
	}
	collapsed := neopicatalog.CollapseVariants("cursor", models, cursorCatalog.table, policy)
	for i, model := range collapsed {
		if !inputs[model] {
			collapsed[i] = policy.build(model)
		}
	}
	return collapsed
}

// cursorMergeDynamic is mergeDynamicModels with Cursor's default authority.
func cursorMergeDynamic(base, dynamic []*neopicatalog.Spec, policy cursorPolicy) []*neopicatalog.Spec {
	if len(dynamic) == 0 {
		return slices.Clone(base)
	}
	if len(base) == 0 {
		return slices.Clone(dynamic)
	}
	merged := slices.Clone(base)
	index := map[string]int{}
	for i, model := range merged {
		index[model.ID] = i
	}
	for _, model := range dynamic {
		if model.ID == "" {
			continue
		}
		at, ok := index[model.ID]
		if !ok {
			index[model.ID] = len(merged)
			merged = append(merged, model)
			continue
		}
		merged[at] = cursorMergeDynamicModel(merged[at], model, policy)
	}
	return merged
}

// cursorMergeDynamicModel is mergeDynamicModel: a rebuild of
// {...existing, ...dynamic} with the additive merge rules. A built dynamic
// row always carries its thinking key, so its thinking always wins.
func cursorMergeDynamicModel(existing, dynamic *neopicatalog.Spec, policy cursorPolicy) *neopicatalog.Spec {
	spec := existing.Clone()
	d := dynamic.Clone()
	spec.ID, spec.API, spec.Provider, spec.BaseURL = d.ID, d.API, d.Provider, d.BaseURL
	spec.Thinking = d.Thinking
	if d.RequestModelID != nil {
		spec.RequestModelID = d.RequestModelID
	}
	if d.IsProviderDefault != nil {
		spec.IsProviderDefault = d.IsProviderDefault
	}
	if d.CursorMaxMode != nil {
		spec.CursorMaxMode = d.CursorMaxMode
	}
	if d.CursorMaxModeRoutes != nil {
		spec.CursorMaxModeRoutes = d.CursorMaxModeRoutes
	}
	if d.CursorModelParameters != nil {
		spec.CursorModelParameters = d.CursorModelParameters
	}
	if d.CursorModelRoutes != nil {
		spec.CursorModelRoutes = d.CursorModelRoutes
	}
	if d.RequiresCursorToolSchemaProjection != nil {
		spec.RequiresCursorToolSchemaProjection = d.RequiresCursorToolSchemaProjection
	}
	spec.Name = cursorPreferDiscoveryName(d.Name, existing.Name, d.ID)
	spec.Reasoning = existing.Reasoning || d.Reasoning
	image := slices.Contains(d.Input, "image")
	if existing.BaseURL == d.BaseURL {
		image = image || slices.Contains(existing.Input, "image")
	}
	spec.Input = []string{"text"}
	if image {
		spec.Input = append(spec.Input, "image")
	}
	cost := neopicatalog.Cost{
		Input:      cursorPreferDiscoveryCost(d.Cost.Input, existing.Cost.Input),
		Output:     cursorPreferDiscoveryCost(d.Cost.Output, existing.Cost.Output),
		CacheRead:  cursorPreferDiscoveryCost(d.Cost.CacheRead, existing.Cost.CacheRead),
		CacheWrite: cursorPreferDiscoveryCost(d.Cost.CacheWrite, existing.Cost.CacheWrite),
	}
	cost.LongContext, cost.TimeBased = d.Cost.LongContext, d.Cost.TimeBased
	if cost.LongContext == nil {
		cost.LongContext = existing.Clone().Cost.LongContext
	}
	if cost.TimeBased == nil {
		cost.TimeBased = existing.Clone().Cost.TimeBased
	}
	spec.Cost = cost
	spec.ContextWindow = cursorPreferDiscoveryLimit(d.ContextWindow, existing.ContextWindow)
	spec.MaxTokens = cursorPreferDiscoveryLimit(d.MaxTokens, existing.MaxTokens)
	return policy.build(spec)
}

func cursorPreferDiscoveryCost(discovery, fallback float64) float64 {
	if discovery > 0 {
		return discovery
	}
	return fallback
}

func cursorPreferDiscoveryName(discovery, fallback, id string) string {
	name := strings.TrimSpace(discovery)
	if name == "" || name == id && fallback != id {
		return fallback
	}
	return name
}

func cursorPreferDiscoveryLimit(discovery, fallback *int) *int {
	if discovery == nil || *discovery <= 0 {
		return fallback
	}
	if *discovery == 4096 && fallback != nil && *fallback > 4096 {
		return fallback
	}
	return discovery
}

// cursorRetain is retainModelIds: an authoritative discovery prunes every
// row it did not return.
func cursorRetain(models, retained []*neopicatalog.Spec) []*neopicatalog.Spec {
	ids := map[string]bool{}
	for _, model := range retained {
		ids[model.ID] = true
	}
	var out []*neopicatalog.Spec
	for _, model := range models {
		if ids[model.ID] {
			out = append(out, model)
		}
	}
	return out
}

// ---- fetchCursorUsableModels ----

// decodeConnectUnaryBody returns the first non-trailer frame of a
// Connect-enveloped body, or nil when the body is not one.
func decodeConnectUnaryBody(payload []byte) []byte {
	if len(payload) < 5 {
		return nil
	}
	for offset := 0; offset+5 <= len(payload); {
		flags := payload[offset]
		end := offset + 5 + int(binary.BigEndian.Uint32(payload[offset+1:offset+5]))
		if end > len(payload) || flags&cursorwire.FlagCompressed != 0 {
			return nil
		}
		if flags&cursorwire.FlagEndStream == 0 {
			return payload[offset+5 : end]
		}
		offset = end
	}
	return nil
}

type cursorMessage interface{ Unmarshal([]byte) error }

// decodeCursorUnary is decodeUnary: nil for a failed request or an
// undecodable body; an empty body is a valid, fully defaulted message.
func decodeCursorUnary[T any, PT interface {
	*T
	cursorMessage
}](payload []byte) PT {
	if payload == nil {
		return nil
	}
	body := decodeConnectUnaryBody(payload)
	if body == nil {
		body = payload
	}
	message := PT(new(T))
	if err := message.Unmarshal(body); err != nil {
		return nil
	}
	return message
}

// cursorDiscoveredSpecs is fetchCursorUsableModels over the three bodies.
func cursorDiscoveredSpecs(responses CursorDiscoveryResponses, baseURLOverride string, policy cursorPolicy) ([]*neopicatalog.Spec, bool) {
	usable := decodeCursorUnary[cursorwire.GetUsableModelsResponse](responses.Usable)
	available := decodeCursorUnary[cursorwire.AvailableModelsResponse](responses.Available)
	if usable == nil && available == nil {
		return nil, false
	}
	var defaultModel *cursorwire.ModelDetails
	if response := decodeCursorUnary[cursorwire.GetDefaultModelForCliResponse](responses.Default); response != nil {
		defaultModel = response.Model
	}
	references := map[string]*neopicatalog.Spec{}
	for _, model := range cursorCatalog.bundled {
		references[model.ID] = model
	}
	var legacy []*neopicatalog.Spec
	if usable != nil {
		legacy = cursorNormalizeLegacy(usable.Models, baseURLOverride, references)
	}
	if available == nil || len(available.Models) == 0 {
		return legacy, true
	}
	var usableIDs map[string]bool
	if usable != nil {
		usableIDs = map[string]bool{}
		for _, model := range legacy {
			usableIDs[model.ID] = true
		}
	}
	var defaultModelID *string
	var defaultMaxMode *bool
	if defaultModel != nil {
		defaultModelID, defaultMaxMode = neopicatalog.Ptr(defaultModel.ModelId), defaultModel.MaxMode
	}
	rich := cursorNormalizeRich(available.Models, usableIDs, baseURLOverride, references, defaultModelID, defaultMaxMode, policy)
	richCatalogIDs := cursorRichCatalogIDs(available.Models)
	represented := map[string]bool{}
	for _, model := range rich {
		represented[model.ID] = true
		if model.RequestModelID != nil {
			represented[*model.RequestModelID] = true
		}
		for routeID := range model.CursorModelRoutes {
			represented[routeID] = true
		}
	}
	for _, model := range legacy {
		if !represented[model.ID] && !richCatalogIDs[model.ID] {
			rich = append(rich, model)
		}
	}
	cursorSortByID(rich)
	return rich, true
}

var cursorCollator = struct {
	sync.Mutex
	*collate.Collator
}{Collator: collate.New(language.Und)}

// cursorSortByID is a stable sort by String.prototype.localeCompare.
func cursorSortByID(specs []*neopicatalog.Spec) {
	cursorCollator.Lock()
	defer cursorCollator.Unlock()
	sort.SliceStable(specs, func(i, j int) bool { return cursorCollator.CompareString(specs[i].ID, specs[j].ID) < 0 })
}

// ---- legacy GetUsableModels rows ----

var cursor1MName = regexp.MustCompile(`(?i)\b1m\b`)

func cursorNormalizeLegacy(models []*cursorwire.ModelDetails, baseURLOverride string, references map[string]*neopicatalog.Spec) []*neopicatalog.Spec {
	var order []string
	byID := map[string]*neopicatalog.Spec{}
	for _, details := range models {
		spec := cursorNormalizeLegacyModel(details, baseURLOverride, references)
		if spec == nil {
			continue
		}
		if _, ok := byID[spec.ID]; !ok {
			order = append(order, spec.ID)
		}
		byID[spec.ID] = spec
	}
	out := make([]*neopicatalog.Spec, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	cursorSortByID(out)
	return out
}

func cursorNormalizeLegacyModel(details *cursorwire.ModelDetails, baseURLOverride string, references map[string]*neopicatalog.Spec) *neopicatalog.Spec {
	id := strings.TrimSpace(details.ModelId)
	if id == "" {
		return nil
	}
	name := id
	for _, candidate := range append([]string{details.DisplayName, details.DisplayNameShort, details.DisplayModelId}, details.Aliases...) {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			name = trimmed
			break
		}
	}
	reference := references[id]
	reasoning := cursorIsKimiK3(id) || cursorIsVersionedGrok(id) || details.ThinkingDetails != nil || reference != nil && reference.Reasoning
	maxMode := details.MaxMode != nil && *details.MaxMode
	var spec *neopicatalog.Spec
	if reference != nil {
		spec = reference.Clone()
		if baseURLOverride != "" {
			spec.BaseURL = baseURLOverride
		}
		spec.Input = cursorResolveInput(id, reference.Input)
		spec.ContextWindow = cursorResolveContextWindow(details, id, maxMode, reference.ContextWindow)
	} else {
		spec = &neopicatalog.Spec{API: "cursor-agent", Provider: "cursor", BaseURL: cursorwire.DefaultBaseURL,
			Input: cursorResolveInput(id, nil), MaxTokens: neopicatalog.Ptr(cursorDefaultMaxTokens),
			ContextWindow: cursorResolveContextWindow(details, id, maxMode, neopicatalog.Ptr(cursorDefaultContextWindow))}
		if baseURLOverride != "" {
			spec.BaseURL = baseURLOverride
		}
	}
	spec.ID, spec.Name, spec.Reasoning = id, name, reasoning
	spec.CursorMaxMode = neopicatalog.Ptr(maxMode)
	return spec
}

func cursorIsKimiK3(id string) bool {
	identity := neopicatalog.Classify("cursor", id)
	return identity.Class == "kimi" && identity.Family == "k3"
}

func cursorRevisionAtLeast(identity neopicatalog.Identity, floor string) bool {
	if identity.Revision == "" {
		return false
	}
	revision, ok := neopicatalog.ParseRevision(identity.Revision)
	bound, okBound := neopicatalog.ParseRevision(floor)
	return ok && okBound && revision.Compare(bound) >= 0
}

func cursorIsVersionedGrok(id string) bool {
	identity := neopicatalog.Classify("cursor", id)
	return identity.Class == "xai" && cursorRevisionAtLeast(identity, "4")
}

func cursorIsGLM52Coding(id string) bool {
	identity := neopicatalog.Classify("cursor", id)
	if identity.Class != "glm" || identity.Revision == "" {
		return false
	}
	if identity.Family != "" && identity.Family != "air" && identity.Family != "turbo" {
		return false
	}
	return cursorRevisionAtLeast(identity, "5.2")
}

// cursorResolveContextWindow is resolveCursorContextWindow.
func cursorResolveContextWindow(details *cursorwire.ModelDetails, id string, maxMode bool, fallback *int) *int {
	labeled := cursor1MName.MatchString(id)
	for _, candidate := range append([]string{details.DisplayName, details.DisplayNameShort, details.DisplayModelId}, details.Aliases...) {
		labeled = labeled || cursor1MName.MatchString(candidate)
	}
	class := neopicatalog.Classify("cursor", id).Class
	if labeled || cursorIsGLM52Coding(id) || maxMode && (class == "anthropic" || class == "gemini") {
		window := cursor1MContextWindow
		if fallback != nil && *fallback > window {
			window = *fallback
		}
		return neopicatalog.Ptr(window)
	}
	return fallback
}

// cursorResolveInput is resolveCursorInput.
func cursorResolveInput(id string, referenceInput []string) []string {
	if referenceInput != nil {
		return slices.Clone(referenceInput)
	}
	switch neopicatalog.Classify("cursor", id).Class {
	case "anthropic", "gemini", "openai":
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// ---- rich AvailableModels lanes ----

type cursorRichVariant struct {
	id         string
	parameters []neopicatalog.ModelParameter
	variant    *cursorwire.AvailableModelsResponse_ModelVariantConfig
	effort     string // "" when the variant names no effort
}

var cursorReasoningParameterIDs = map[string]bool{"effort": true, "reasoning": true, "reasoning_effort": true, "thinking": true, "thinking_effort": true}

func cursorTrimmedParameters(values []*cursorwire.ModelParameterValue) []neopicatalog.ModelParameter {
	out := []neopicatalog.ModelParameter{}
	for _, value := range values {
		out = append(out, neopicatalog.ModelParameter{ID: strings.TrimSpace(value.Id), Value: strings.TrimSpace(value.Value)})
	}
	return out
}

func cursorParameterSuffix(parameters []neopicatalog.ModelParameter) string {
	parts := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		parts = append(parts, parameter.ID+"="+parameter.Value)
	}
	return strings.Join(parts, ",")
}

func cursorOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func cursorTrue(value *bool) bool { return value != nil && *value }

// cursorRichCatalogIDs is collectRichCursorCatalogIds.
func cursorRichCatalogIDs(models []*cursorwire.AvailableModelsResponse_ModelDetails) map[string]bool {
	ids := map[string]bool{}
	for _, details := range models {
		baseID := strings.TrimSpace(details.Name)
		if baseID != "" {
			ids[baseID] = true
		}
		for _, id := range append(slices.Clone(details.LegacySlugs), details.IdAliases...) {
			if trimmed := strings.TrimSpace(id); trimmed != "" {
				ids[trimmed] = true
			}
		}
		for index, variant := range details.Variants {
			parameters := slices.DeleteFunc(cursorTrimmedParameters(variant.ParameterValues), func(p neopicatalog.ModelParameter) bool { return p.ID == "" })
			if id := cursorVariantID(variant, baseID, parameters, index); id != "" {
				ids[id] = true
			}
		}
	}
	return ids
}

func cursorVariantID(variant *cursorwire.AvailableModelsResponse_ModelVariantConfig, baseID string, parameters []neopicatalog.ModelParameter, index int) string {
	if id := cursorOptionalString(variant.LegacySlug); id != "" {
		return id
	}
	if id := cursorOptionalString(variant.VariantStringRepresentation); id != "" {
		return id
	}
	if suffix := cursorParameterSuffix(parameters); suffix != "" {
		return baseID + "@" + suffix
	}
	return baseID + "@variant-" + strconv.Itoa(index+1)
}

type cursorParameterSelection struct {
	increasesModelCost                 *bool
	markdownTooltip, definitionTooltip *string
}

// cursorParameterValue is cursorParameterValue: the declared value a
// variant parameter selects.
func cursorParameterValue(details *cursorwire.AvailableModelsResponse_ModelDetails, parameter neopicatalog.ModelParameter) *cursorParameterSelection {
	var definition *cursorwire.ModelParameterDefinition
	for _, candidate := range details.ParameterDefinitions {
		if candidate.Id == parameter.ID {
			definition = candidate
			break
		}
	}
	if definition == nil {
		return nil
	}
	kind := definition.ParameterType
	selection := &cursorParameterSelection{definitionTooltip: definition.MarkdownTooltip}
	if kind != nil && kind.BooleanParameter != nil {
		for _, value := range kind.BooleanParameter.Values {
			if value.Value == parameter.Value {
				selection.increasesModelCost = value.IncreasesModelCost
				return cursorWithEnumTooltip(selection, kind, parameter)
			}
		}
	}
	if kind != nil && kind.EnumParameter != nil {
		for _, value := range kind.EnumParameter.Values {
			if value.Value == parameter.Value {
				selection.increasesModelCost, selection.markdownTooltip = value.IncreasesModelCost, value.MarkdownTooltip
				return selection
			}
		}
	}
	return nil
}

// cursorWithEnumTooltip keeps the enum value's tooltip beside a boolean
// match, as the TS selection object does.
func cursorWithEnumTooltip(selection *cursorParameterSelection, kind *cursorwire.ModelParameterDefinition_ModelParameterType, parameter neopicatalog.ModelParameter) *cursorParameterSelection {
	if kind.EnumParameter != nil {
		for _, value := range kind.EnumParameter.Values {
			if value.Value == parameter.Value {
				selection.markdownTooltip = value.MarkdownTooltip
				break
			}
		}
	}
	return selection
}

var cursorMultiplierPattern = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*[x×]`)

// cursorVariantCostMultiplier is cursorVariantCostMultiplier: the product
// of the "Nx" factors cost-raising parameters declare.
func cursorVariantCostMultiplier(details *cursorwire.AvailableModelsResponse_ModelDetails, entry *cursorRichVariant) float64 {
	multiplier := 1.0
	for _, parameter := range entry.parameters {
		selected := cursorParameterValue(details, parameter)
		if selected == nil || !cursorTrue(selected.increasesModelCost) {
			continue
		}
		for _, description := range []*string{selected.markdownTooltip, selected.definitionTooltip} {
			if description == nil || *description == "" {
				continue
			}
			if factor, ok := cursorFirstMultiplier(*description); ok {
				multiplier *= factor
				break
			}
		}
	}
	return multiplier
}

// cursorFirstMultiplier finds /\b(\d+(?:\.\d+)?)\s*[x×](?![a-z])/i.
func cursorFirstMultiplier(text string) (float64, bool) {
	for _, match := range cursorMultiplierPattern.FindAllStringSubmatchIndex(text, -1) {
		if end := match[1]; end < len(text) && (text[end]|0x20 >= 'a' && text[end]|0x20 <= 'z') {
			continue
		}
		factor, err := strconv.ParseFloat(text[match[2]:match[3]], 64)
		if err == nil && factor > 0 {
			return factor, true
		}
		return 0, false
	}
	return 0, false
}

func cursorScaleCost(cost neopicatalog.Cost, multiplier float64) neopicatalog.Cost {
	if multiplier == 1 {
		return cost
	}
	out := neopicatalog.Cost{Input: cost.Input * multiplier, Output: cost.Output * multiplier, CacheRead: cost.CacheRead * multiplier, CacheWrite: cost.CacheWrite * multiplier}
	if cost.TimeBased != nil {
		timeBased := *cost.TimeBased
		timeBased.EffectiveRates = nil
		for _, rate := range cost.TimeBased.EffectiveRates {
			rate.Input, rate.Output, rate.CacheRead, rate.CacheWrite = rate.Input*multiplier, rate.Output*multiplier, rate.CacheRead*multiplier, rate.CacheWrite*multiplier
			timeBased.EffectiveRates = append(timeBased.EffectiveRates, rate)
		}
		out.TimeBased = &timeBased
	}
	return out
}

// cursorCardAxes is resolveCursorCardAxes: a slug's reviewed rate cards.
func cursorCardAxes(slug string, now int64) (patch, fallback *neopicatalog.Cost) {
	entry := cursorCatalog.policy[slug]
	if entry == nil {
		return nil, nil
	}
	card := func(payload *cursorCostCard, cards []datedCostCard) *neopicatalog.Cost {
		if payload == nil || payload.Input == nil || payload.Output == nil {
			return nil
		}
		base := neopicatalog.Cost{Input: *payload.Input, Output: *payload.Output}
		if payload.CacheRead != nil {
			base.CacheRead = *payload.CacheRead
		}
		if payload.CacheWrite != nil {
			base.CacheWrite = *payload.CacheWrite
		}
		cost := datedCost(base, cards, now)
		return &cost
	}
	return card(entry.Catalog.CostPatch, entry.patchCards), card(entry.Catalog.CostFallback, entry.fallbackCards)
}

// cursorRichCardCost is resolveRichCursorCardCost.
func cursorRichCardCost(details *cursorwire.AvailableModelsResponse_ModelDetails, entry *cursorRichVariant, laneID string, now int64) *neopicatalog.Cost {
	var costAxes []string
	for _, definition := range details.ParameterDefinitions {
		raises := false
		if kind := definition.ParameterType; kind != nil {
			if kind.BooleanParameter != nil {
				for _, value := range kind.BooleanParameter.Values {
					raises = raises || cursorTrue(value.IncreasesModelCost)
				}
			}
			if kind.EnumParameter != nil {
				for _, value := range kind.EnumParameter.Values {
					raises = raises || cursorTrue(value.IncreasesModelCost)
				}
			}
		}
		if raises {
			costAxes = append(costAxes, definition.Id)
		}
	}
	priceClass := func(parameters []neopicatalog.ModelParameter) string {
		values := make([]string, 0, len(costAxes))
		for _, id := range costAxes {
			value := ""
			for _, parameter := range parameters {
				if parameter.ID == id {
					value = parameter.Value
					break
				}
			}
			values = append(values, value)
		}
		return strings.Join(values, "\x00")
	}
	entryClass := priceClass(entry.parameters)
	ownSlugs := []string{laneID, entry.id}
	baseSlugs := []string{strings.TrimSpace(details.Name)}
	for _, variant := range details.Variants {
		slug := cursorOptionalString(variant.LegacySlug)
		if slug == "" {
			continue
		}
		parameters := cursorTrimmedParameters(variant.ParameterValues)
		if priceClass(parameters) == entryClass {
			ownSlugs = append(ownSlugs, slug)
			continue
		}
		raises := false
		for _, parameter := range parameters {
			if selected := cursorParameterValue(details, parameter); selected != nil && cursorTrue(selected.increasesModelCost) {
				raises = true
			}
		}
		if !raises {
			baseSlugs = append(baseSlugs, slug)
		}
	}
	var base *neopicatalog.Cost
	for _, slug := range ownSlugs {
		patch, fallback := cursorCardAxes(slug, now)
		if patch != nil {
			return patch
		}
		if base == nil {
			base = fallback
		}
	}
	for _, slug := range baseSlugs {
		if base != nil {
			break
		}
		patch, fallback := cursorCardAxes(slug, now)
		if base = patch; base == nil {
			base = fallback
		}
	}
	if base == nil {
		return nil
	}
	scaled := cursorScaleCost(*base, cursorVariantCostMultiplier(details, entry))
	return &scaled
}

func cursorVariantEffort(parameters []neopicatalog.ModelParameter) string {
	for _, parameter := range parameters {
		if parameter.ID == "thinking" {
			if value := strings.ToLower(parameter.Value); value == "false" || value == "off" || value == "none" {
				return "off"
			}
			break
		}
	}
	raw, found := "", false
	for _, parameter := range parameters {
		if parameter.ID == "reasoning" || parameter.ID == "reasoning_effort" || parameter.ID == "thinking_effort" || parameter.ID == "effort" {
			raw, found = parameter.Value, true
			break
		}
	}
	if !found {
		return ""
	}
	normalized := strings.ToLower(cursorEffortSeparators.ReplaceAllString(raw, "-"))
	switch normalized {
	case "none", "off", "disabled":
		return "off"
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return normalized
	case "extra-high":
		return "xhigh"
	}
	return ""
}

var cursorEffortSeparators = regexp.MustCompile(`[_\s]+`)

func cursorLaneParameters(parameters []neopicatalog.ModelParameter) []neopicatalog.ModelParameter {
	out := []neopicatalog.ModelParameter{}
	for _, parameter := range parameters {
		if !cursorReasoningParameterIDs[parameter.ID] {
			out = append(out, parameter)
		}
	}
	return out
}

// cursorVariantBlocked is isRichVariantBlocked: a parameter value the
// account's admin allowlist blocks.
func cursorVariantBlocked(details *cursorwire.AvailableModelsResponse_ModelDetails, variant *cursorwire.AvailableModelsResponse_ModelVariantConfig) bool {
	for _, value := range variant.ParameterValues {
		var definition *cursorwire.ModelParameterDefinition
		for _, candidate := range details.ParameterDefinitions {
			if candidate.Id == value.Id {
				definition = candidate
				break
			}
		}
		if definition == nil || definition.ParameterType == nil {
			continue
		}
		blocked, found := false, false
		if kind := definition.ParameterType.BooleanParameter; kind != nil {
			for _, candidate := range kind.Values {
				if candidate.Value == value.Value {
					blocked, found = cursorTrue(candidate.BlockedByAdminAllowlist), true
					break
				}
			}
		}
		if kind := definition.ParameterType.EnumParameter; !found && kind != nil {
			for _, candidate := range kind.Values {
				if candidate.Value == value.Value {
					blocked = cursorTrue(candidate.BlockedByAdminAllowlist)
					break
				}
			}
		}
		if blocked {
			return true
		}
	}
	return false
}

// cursorRichVariants is normalizeRichCursorVariants.
func cursorRichVariants(details *cursorwire.AvailableModelsResponse_ModelDetails, baseID string, usableIDs map[string]bool) []*cursorRichVariant {
	if len(details.Variants) == 0 {
		if usableIDs != nil {
			found := usableIDs[baseID]
			for _, id := range append(slices.Clone(details.LegacySlugs), details.IdAliases...) {
				found = found || usableIDs[id]
			}
			if !found {
				return nil
			}
		}
		return []*cursorRichVariant{{id: baseID, parameters: []neopicatalog.ModelParameter{}}}
	}
	var variants []*cursorRichVariant
	seen := map[string]bool{}
	for index, variant := range details.Variants {
		if cursorVariantBlocked(details, variant) {
			continue
		}
		parameters := slices.DeleteFunc(cursorTrimmedParameters(variant.ParameterValues), func(p neopicatalog.ModelParameter) bool { return p.ID == "" })
		id := cursorVariantID(variant, baseID, parameters, index)
		if usableIDs != nil {
			entitled := false
			for _, candidate := range append([]string{cursorOptionalString(variant.LegacySlug), id, baseID}, details.IdAliases...) {
				entitled = entitled || candidate != "" && usableIDs[candidate]
			}
			if !entitled {
				continue
			}
		}
		signature := cursorParameterSuffix(parameters) + "\x00standard"
		if cursorTrue(variant.IsMaxMode) {
			signature = cursorParameterSuffix(parameters) + "\x00max"
		}
		if seen[signature] {
			continue
		}
		seen[signature] = true
		variants = append(variants, &cursorRichVariant{id: id, parameters: parameters, variant: variant, effort: cursorVariantEffort(parameters)})
	}
	return variants
}

// cursorLaneID is cursorLaneId.
func cursorLaneID(baseID string, parameters []neopicatalog.ModelParameter, defaults map[string]string, isMaxMode bool, claimed map[string]bool) string {
	var suffixes []string
	for _, parameter := range parameters {
		id, value := neoPiSlugify(parameter.ID), neoPiSlugify(parameter.Value)
		if id == "" || value == "" || parameter.Value == "false" {
			continue
		}
		switch {
		case parameter.Value == "true":
			suffixes = append(suffixes, id)
		case parameter.Value == defaults[parameter.ID]:
		case parameter.ID == "context":
			suffixes = append(suffixes, value)
		default:
			suffixes = append(suffixes, id+"-"+value)
		}
	}
	root := strings.Join(append([]string{baseID}, suffixes...), "-")
	candidate := root
	if claimed[candidate] {
		if isMaxMode {
			candidate = root + "-max-mode"
		} else {
			candidate = root + "-standard"
		}
	}
	for duplicate := 2; claimed[candidate]; duplicate++ {
		candidate = root + "-" + strconv.Itoa(duplicate)
	}
	claimed[candidate] = true
	return candidate
}

// cursorReviewedLaneID is reviewedCursorLaneId.
func cursorReviewedLaneID(entries []*cursorRichVariant, claimed map[string]bool) string {
	familyID := ""
	for _, entry := range entries {
		if entry.variant == nil {
			continue
		}
		slug := cursorOptionalString(entry.variant.LegacySlug)
		if slug == "" {
			continue
		}
		owner, ok := cursorCatalog.table.FamilyID(slug)
		if !ok {
			continue
		}
		if familyID != "" && owner != familyID {
			return ""
		}
		familyID = owner
	}
	if familyID == "" || claimed[familyID] {
		return ""
	}
	claimed[familyID] = true
	return familyID
}

// cursorLaneName is cursorLaneName.
func cursorLaneName(details *cursorwire.AvailableModelsResponse_ModelDetails, laneID, baseID string) string {
	baseName := cursorOptionalString(details.ClientDisplayName)
	if baseName == "" {
		baseName = cursorOptionalString(details.ServerModelName)
	}
	if baseName == "" {
		baseName = strings.TrimSpace(details.Name)
	}
	if baseName == "" {
		baseName = baseID
	}
	var parts []string
	for _, part := range strings.Split(strings.TrimPrefix(laneID[len(baseID):], "-"), "-") {
		switch {
		case part == "":
		case part == "1m":
			parts = append(parts, "1M")
		default:
			parts = append(parts, strings.ToUpper(part[:1])+part[1:])
		}
	}
	if len(parts) == 0 {
		return baseName
	}
	return baseName + " " + strings.Join(parts, " ")
}

func cursorDefaultRank(entry *cursorRichVariant) int {
	switch {
	case entry.variant != nil && cursorTrue(entry.variant.IsDefaultNonMaxConfig):
		return 0
	case entry.variant != nil && cursorTrue(entry.variant.IsDefaultMaxConfig):
		return 1
	}
	return 2
}

func cursorLaneKey(parameters []neopicatalog.ModelParameter, isMaxMode bool) string {
	key, _ := json.Marshal([]any{parameters, isMaxMode})
	return string(key)
}

// cursorNormalizeRich is normalizeRichCursorModels.
func cursorNormalizeRich(models []*cursorwire.AvailableModelsResponse_ModelDetails, usableIDs map[string]bool, baseURLOverride string,
	references map[string]*neopicatalog.Spec, defaultModelID *string, defaultMaxMode *bool, policy cursorPolicy) []*neopicatalog.Spec {
	var normalized []*neopicatalog.Spec
	for _, details := range models {
		baseID := strings.TrimSpace(details.Name)
		if baseID == "" || details.SupportsAgent != nil && !*details.SupportsAgent || cursorTrue(details.IsChatOnly) || cursorTrue(details.RequiresDataRetention) {
			continue
		}
		variants := cursorRichVariants(details, baseID, usableIDs)
		if len(variants) == 0 {
			continue
		}
		ranked := slices.Clone(variants)
		sort.SliceStable(ranked, func(i, j int) bool { return cursorDefaultRank(ranked[i]) < cursorDefaultRank(ranked[j]) })
		defaults := map[string]string{}
		for _, entry := range ranked {
			for _, parameter := range cursorLaneParameters(entry.parameters) {
				if _, ok := defaults[parameter.ID]; !ok {
					defaults[parameter.ID] = parameter.Value
				}
			}
		}
		var laneOrder []string
		lanes := map[string][]*cursorRichVariant{}
		for _, entry := range variants {
			key := cursorLaneKey(cursorLaneParameters(entry.parameters), entry.variant != nil && cursorTrue(entry.variant.IsMaxMode))
			if _, ok := lanes[key]; !ok {
				laneOrder = append(laneOrder, key)
			}
			lanes[key] = append(lanes[key], entry)
		}
		claimed := map[string]bool{}
		for _, key := range laneOrder {
			entries := lanes[key]
			first := entries[0]
			richLaneID := cursorLaneID(baseID, cursorLaneParameters(first.parameters), defaults, first.variant != nil && cursorTrue(first.variant.IsMaxMode), claimed)
			laneName := cursorLaneName(details, richLaneID, baseID)
			laneID := cursorReviewedLaneID(entries, claimed)
			if laneID == "" {
				laneID = richLaneID
			}
			normalized = append(normalized, cursorRichLane(details, entries, laneID, laneName, baseURLOverride, references, defaultModelID, defaultMaxMode, policy)...)
		}
	}
	cursorSortByID(normalized)
	return normalized
}

func cursorIsThinkingToggle(entry *cursorRichVariant) bool {
	if entry.effort != "" {
		return false
	}
	for _, parameter := range entry.parameters {
		if parameter.ID == "thinking" && strings.ToLower(parameter.Value) == "true" {
			return true
		}
	}
	return false
}

// cursorIsProviderDefault is isCursorProviderDefault.
func cursorIsProviderDefault(details *cursorwire.AvailableModelsResponse_ModelDetails, entry *cursorRichVariant, defaultModelID *string, defaultMaxMode *bool) bool {
	variant := entry.variant
	if defaultModelID != nil {
		id := *defaultModelID
		if id == entry.id {
			return defaultMaxMode == nil || *defaultMaxMode == (variant != nil && cursorTrue(variant.IsMaxMode))
		}
		if id != details.Name && !slices.Contains(details.LegacySlugs, id) && !slices.Contains(details.IdAliases, id) {
			return false
		}
		if variant == nil {
			return true
		}
		if defaultMaxMode != nil && *defaultMaxMode {
			return cursorTrue(variant.IsDefaultMaxConfig)
		}
		return cursorTrue(variant.IsDefaultNonMaxConfig)
	}
	if !details.DefaultOn {
		return false
	}
	return variant == nil || cursorTrue(variant.IsDefaultNonMaxConfig) || cursorTrue(variant.IsDefaultMaxConfig)
}

func cursorVariantPreference(details *cursorwire.AvailableModelsResponse_ModelDetails, entry *cursorRichVariant, defaultModelID *string, defaultMaxMode *bool) int {
	if cursorIsProviderDefault(details, entry, defaultModelID, defaultMaxMode) {
		return 3
	}
	if entry.variant != nil && (cursorTrue(entry.variant.IsDefaultNonMaxConfig) || cursorTrue(entry.variant.IsDefaultMaxConfig)) {
		return 2
	}
	return 1
}

var cursorContextValue = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)([km])?$`)

// cursorParseContextWindow is parseCursorContextWindow ("272k", "1m").
func cursorParseContextWindow(value string) *int {
	match := cursorContextValue.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return nil
	}
	amount, err := strconv.ParseFloat(match[1], 64)
	if err != nil || amount <= 0 {
		return nil
	}
	scale := 1.0
	switch strings.ToLower(match[2]) {
	case "m":
		scale = 1_000_000
	case "k":
		scale = 1_000
	}
	return neopicatalog.Ptr(int(jsRound(amount * scale)))
}

func cursorUint(value *uint32) *int {
	if value == nil {
		return nil
	}
	return neopicatalog.Ptr(int(*value))
}

// cursorRichLane is buildRichCursorLane.
func cursorRichLane(details *cursorwire.AvailableModelsResponse_ModelDetails, entries []*cursorRichVariant, laneID, laneName, baseURLOverride string,
	references map[string]*neopicatalog.Spec, defaultModelID *string, defaultMaxMode *bool, policy cursorPolicy) []*neopicatalog.Spec {
	var slotOrder []string
	selectedBySlot := map[string]*cursorRichVariant{}
	for _, entry := range entries {
		slot := entry.effort
		if slot == "" {
			slot = "fixed"
			if cursorIsThinkingToggle(entry) {
				slot = "thinking"
			}
		}
		current, ok := selectedBySlot[slot]
		if !ok {
			slotOrder = append(slotOrder, slot)
		}
		if !ok || cursorVariantPreference(details, entry, defaultModelID, defaultMaxMode) > cursorVariantPreference(details, current, defaultModelID, defaultMaxMode) {
			selectedBySlot[slot] = entry
		}
	}
	selected := make([]*cursorRichVariant, 0, len(slotOrder))
	for _, slot := range slotOrder {
		selected = append(selected, selectedBySlot[slot])
	}
	if len(selected) == 0 {
		return nil
	}
	modelID := strings.TrimSpace(details.Name)
	routeKeys := map[*cursorRichVariant]string{}
	used := map[string]bool{}
	routes := map[string]neopicatalog.CursorRoute{}
	for _, entry := range selected {
		routeKey := entry.id
		if used[routeKey] {
			routeKey = ""
			if entry.variant != nil {
				routeKey = cursorOptionalString(entry.variant.VariantStringRepresentation)
			}
			if routeKey == "" {
				routeKey = entry.id + "@" + cursorParameterSuffix(entry.parameters)
			}
		}
		root := routeKey
		for duplicate := 2; used[routeKey]; duplicate++ {
			routeKey = root + "#" + strconv.Itoa(duplicate)
		}
		used[routeKey] = true
		routeKeys[entry] = routeKey
		route := neopicatalog.CursorRoute{ModelID: modelID, Parameters: slices.Clone(entry.parameters)}
		if entry.variant != nil && entry.variant.IsMaxMode != nil {
			route.MaxMode = neopicatalog.Ptr(*entry.variant.IsMaxMode)
		}
		routes[routeKey] = route
	}

	var members []*neopicatalog.Spec
	routing := map[string]string{}
	defaultMember := ""
	var toggleMember *neopicatalog.Spec
	for _, entry := range selected {
		routeKey := routeKeys[entry]
		variant := entry.variant
		reference := references[entry.id]
		ownReference := reference != nil
		if reference == nil {
			reference = references[modelID]
		}
		if reference == nil {
			for _, id := range details.LegacySlugs {
				if reference = references[id]; reference != nil {
					break
				}
			}
		}
		isMaxMode := variant != nil && cursorTrue(variant.IsMaxMode)
		contextValue := ""
		for _, parameter := range entry.parameters {
			if parameter.ID == "context" {
				contextValue = parameter.Value
				break
			}
		}
		contextLimit := cursorParseContextWindow(contextValue)
		if contextLimit == nil {
			if isMaxMode {
				contextLimit = cursorUint(details.ContextTokenLimitForMaxMode)
			} else {
				contextLimit = cursorUint(details.ContextTokenLimit)
			}
		}
		discoveredContext := contextLimit
		if discoveredContext == nil {
			discoveredContext = cursorUint(details.AutoContextExtendedMaxTokens)
		}
		if discoveredContext == nil {
			discoveredContext = cursorUint(details.AutoContextMaxTokens)
		}
		fallbackContext := neopicatalog.Ptr(cursorDefaultContextWindow)
		if reference != nil && reference.ContextWindow != nil {
			fallbackContext = neopicatalog.Ptr(*reference.ContextWindow)
		}
		var input []string
		switch {
		case details.SupportsImages == nil:
			var referenceInput []string
			if reference != nil {
				referenceInput = reference.Input
			}
			input = cursorResolveInput(entry.id, referenceInput)
		case *details.SupportsImages:
			input = []string{"text", "image"}
		default:
			input = []string{"text"}
		}
		isDefault := cursorIsProviderDefault(details, entry, defaultModelID, defaultMaxMode)
		toggle := cursorIsThinkingToggle(entry)
		var reasoning bool
		switch {
		case entry.effort == "off":
			reasoning = false
		case entry.effort != "" || toggle:
			reasoning = true
		case details.SupportsThinking != nil:
			reasoning = *details.SupportsThinking
		default:
			reasoning = reference != nil && reference.Reasoning
		}
		var referenceCost *neopicatalog.Cost
		if reference != nil && (reference.Cost.Input != 0 || reference.Cost.Output != 0) {
			cost := reference.Clone().Cost
			if !ownReference {
				cost = cursorScaleCost(cost, cursorVariantCostMultiplier(details, entry))
			}
			referenceCost = &cost
		}
		cost := cursorRichCardCost(details, entry, laneID, policy.now)
		if cost == nil {
			cost = referenceCost
		}
		if cost == nil {
			cost = &neopicatalog.Cost{}
		}
		var member *neopicatalog.Spec
		if reference != nil {
			member = reference.Clone()
		} else {
			member = &neopicatalog.Spec{API: "cursor-agent", Provider: "cursor", MaxTokens: neopicatalog.Ptr(cursorDefaultMaxTokens)}
		}
		member.ID, member.Name = routeKey, laneName
		switch {
		case baseURLOverride != "":
			member.BaseURL = baseURLOverride
		case reference != nil:
			member.BaseURL = reference.BaseURL
		default:
			member.BaseURL = cursorwire.DefaultBaseURL
		}
		member.Reasoning, member.Input, member.Cost = reasoning, input, *cost
		member.ContextWindow = discoveredContext
		if member.ContextWindow == nil {
			member.ContextWindow = fallbackContext
		}
		member.CursorMaxMode = neopicatalog.Ptr(isMaxMode)
		member.CursorModelParameters = slices.Clone(entry.parameters)
		member.CursorModelRoutes = map[string]neopicatalog.CursorRoute{}
		for key, route := range routes {
			route.Parameters = slices.Clone(route.Parameters)
			member.CursorModelRoutes[key] = route
		}
		members = append(members, member)
		switch {
		case entry.effort != "":
			routing[entry.effort] = routeKey
		case toggle:
			toggleMember = member
		}
		if isDefault {
			defaultMember = routeKey
		}
	}
	toggleEfforts := 0
	if toggleMember != nil {
		var surface []string
		if toggleMember.Thinking != nil && len(toggleMember.Thinking.Efforts) > 0 {
			surface = toggleMember.Thinking.Efforts
		} else {
			probe := toggleMember.Clone()
			probe.Reasoning, probe.Thinking = true, nil
			if derived := policy.DerivedThinking(probe); derived != nil {
				surface = derived.Efforts
			}
		}
		for _, effort := range surface {
			if _, ok := routing[effort]; ok {
				continue
			}
			routing[effort] = toggleMember.ID
			toggleEfforts++
		}
	}
	if defaultMember == "" {
		for _, entry := range selected {
			if entry.variant != nil && (cursorTrue(entry.variant.IsDefaultNonMaxConfig) || cursorTrue(entry.variant.IsDefaultMaxConfig)) {
				defaultMember = routeKeys[entry]
				break
			}
		}
	}
	if defaultMember == "" {
		if off, ok := routing["off"]; ok {
			defaultMember = off
		} else {
			defaultMember = members[0].ID
		}
	}
	var efforts []string
	for _, effort := range neopicatalog.ThinkingEfforts {
		if _, ok := routing[effort]; ok {
			efforts = append(efforts, effort)
		}
	}
	family := neopicatalog.Family{ID: laneID, Name: laneName, Routing: routing, DefaultMember: defaultMember}
	for _, member := range members {
		family.Members = append(family.Members, member.ID)
	}
	if len(efforts) > 0 {
		_, hasOff := routing["off"]
		thinking := &neopicatalog.Thinking{Mode: "effort", Efforts: efforts, RequiresEffort: !hasOff}
		if toggleEfforts == 0 {
			for _, effort := range efforts {
				if routing[effort] == defaultMember {
					thinking.DefaultLevel = effort
					break
				}
			}
		}
		family.Thinking = thinking
	}
	return neopicatalog.CollapseWithTable(members, &neopicatalog.Table{Families: []neopicatalog.Family{family}})
}
