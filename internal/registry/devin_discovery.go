package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
)

// This file ports NeoPi's Devin discovery (pi-catalog discovery/devin.ts
// normalizeDevinModels) and the parts of compat/collapse.ts it reaches:
// server-declared family lanes, the reviewed devin variant table, and the
// automatic X / X-thinking pair rule. Pricing is not carried: NeoProxy's
// NeoPi records hold no cost, so cost is only computed for the pair rule's
// same-price gate.

// devinCollapseJSON is the reviewed devin variant table and the taxonomy
// vocabulary the pair rule reads, exported by
// testdata/capture_devin_npi_collapse.ts.
//
//go:embed models/devin_collapse.json
var devinCollapseJSON []byte

type devinVariantFamily struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Members       []string          `json:"members"`
	DefaultMember string            `json:"defaultMember,omitempty"`
	Routing       map[string]string `json:"routing"`
	Thinking      *NeoPiThinking    `json:"thinking,omitempty"`
}

type devinSuffixRule struct {
	Suffix           string `json:"suffix"`
	Thinking         bool   `json:"thinking"`
	ExceptBarePrefix string `json:"exceptBarePrefix"`
}

var devinCollapse = func() struct {
	Families      []devinVariantFamily `json:"families"`
	PairTokens    []string             `json:"pairTokens"`
	Suffixes      []devinSuffixRule    `json:"suffixes"`
	EffortAliases []string             `json:"effortAliases"`
} {
	var data struct {
		Families      []devinVariantFamily `json:"families"`
		PairTokens    []string             `json:"pairTokens"`
		Suffixes      []devinSuffixRule    `json:"suffixes"`
		EffortAliases []string             `json:"effortAliases"`
	}
	if err := json.Unmarshal(devinCollapseJSON, &data); err != nil {
		panic(fmt.Sprintf("registry: devin_collapse.json: %v", err))
	}
	return data
}()

// devinRoutingKeys is VARIANT_ROUTING_KEYS: "off" then THINKING_EFFORTS.
var devinRoutingKeys = append([]string{"off"}, neoPiThinkingEfforts...)

const (
	devinDefaultContextWindow = 200_000
	devinDefaultMaxTokens     = 64_000
)

// devinCost is the per-million rate card, read only by the pair rule.
type devinCost struct{ input, output, cacheRead, cacheWrite float64 }

// devinSpec is a ModelSpec<"devin-agent"> as discovery and collapsing build it.
type devinSpec struct {
	model             NeoPiModel
	supportsTools     bool
	cost              devinCost
	isProviderDefault bool
}

var (
	devinReasoningLabel   = regexp.MustCompile(`(?i)think|thinking|minimal|high|medium|low|xhigh|max|reasoning`)
	devinNoReasoningLabel = regexp.MustCompile(`(?i)\bno thinking\b`)
	devinCostDenominator  = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*([kmb])?`)
	devinNonAlnum         = regexp.MustCompile(`[^a-z0-9]+`)
)

// devinImageBlindUIDs advertise supports_images but drop ChatMessagePrompt.images.
var devinImageBlindUIDs = map[string]bool{"swe-1-6": true, "swe-1-6-fast": true}

var devinFamilyEffortByName = map[string]string{
	"none": "off", "nothinking": "off", "minimal": "minimal", "low": "low",
	"medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max",
}

// DevinDiscoveredModels is fetchDevinModels' normalization of a
// GetCliModelConfigs response into NeoPi records, as registry models. An
// empty result is NeoPi's stale-client-identity signature.
func DevinDiscoveredModels(response devinwire.CliModelConfigs, baseURL string) []*ModelInfo {
	specs := devinNormalize(response, baseURL)
	out := make([]*ModelInfo, 0, len(specs))
	for i := range specs {
		out = append(out, DevinModelInfo(&specs[i].model))
	}
	return out
}

func devinNormalize(response devinwire.CliModelConfigs, baseURL string) []devinSpec {
	if baseURL == "" {
		baseURL = devinwire.DefaultBaseURL
	}
	accountDefault := strings.TrimSpace(response.DefaultUID)
	live := map[string]*devinwire.ClientModelConfig{}
	for i := range response.Configs {
		config := &response.Configs[i]
		uid := strings.TrimSpace(config.ModelUID)
		if _, ok := live[uid]; !config.Disabled && uid != "" && !ok {
			live[uid] = config
		}
	}
	var specs []devinSpec
	seen := map[string]bool{}
	var lanes []*devinLane
	for i := range response.Configs {
		config := &response.Configs[i]
		if config.Disabled {
			continue
		}
		display := devinwire.DisplayUnspecified
		if config.Info != nil {
			display = config.Info.DisplayOption
		}
		if display == devinwire.DisplayQuickReview || display == devinwire.DisplayInternalDefault {
			continue
		}
		uid := strings.TrimSpace(config.ModelUID)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		isRouter := display == devinwire.DisplayModelRouter || (config.Info != nil && config.Info.IsModelRouter)
		// Only harness-less routers take AssignModel; harness-backed composites
		// (fusion) are themselves chat uids.
		isAssignRouter := isRouter && (config.Info == nil || len(config.Info.HarnessUIDs) == 0)
		lead, pairing := devinFusionLead(uid, live)
		if pairing && lead == "" {
			continue
		}
		spec := devinModelSpec(config, uid, baseURL, isAssignRouter)
		if pairing {
			devinRouteFusionLead(&spec, devinModelSpec(live[lead], lead, baseURL, false))
		}
		if uid == accountDefault {
			spec.isProviderDefault = true
		}
		specs = append(specs, spec)
		// A router is a dispatcher, not an effort tier: it stays standalone.
		if !isRouter {
			lanes = devinCollectLane(lanes, config, uid)
		}
	}

	if families := devinDynamicFamilies(lanes); len(families) > 0 {
		specs = devinCollapseWithTable(specs, families)
	}
	specs = devinCollapseWithTable(specs, devinCollapse.Families)
	if derived := devinPairFamilies(specs); len(derived) > 0 {
		specs = devinCollapseWithTable(specs, derived)
	}

	// The account default outranks its family's own default lane when an
	// effort routes to it.
	for i := range specs {
		spec := &specs[i]
		if !spec.isProviderDefault || spec.model.Thinking == nil || spec.model.Thinking.EffortRouting == nil {
			continue
		}
		for _, effort := range neoPiThinkingEfforts {
			if spec.model.Thinking.EffortRouting[effort] != accountDefault {
				continue
			}
			thinking := spec.model.Thinking.Clone()
			thinking.DefaultLevel = effort
			spec.model.Thinking = thinking
			if spec.model.ID == accountDefault {
				spec.model.RequestModelID = ""
			} else {
				spec.model.RequestModelID = accountDefault
			}
			break
		}
		break
	}
	// NeoPi sorts with localeCompare; ids order bytewise here.
	slices.SortStableFunc(specs, func(a, b devinSpec) int { return strings.Compare(a.model.ID, b.model.ID) })
	return specs
}

// devinSupportsThinking: server features are authoritative; the label
// heuristic covers configs that ship none.
func devinSupportsThinking(config *devinwire.ClientModelConfig) bool {
	if config.Info != nil && config.Info.Features != nil {
		return config.Info.Features.SupportsThinking
	}
	if devinNoReasoningLabel.MatchString(config.Label) {
		return false
	}
	return devinReasoningLabel.MatchString(config.Label)
}

// devinCostDenominatorTokens is the token count one cost dimension covers.
func devinCostDenominatorTokens(denominator string) float64 {
	match := devinCostDenominator.FindStringSubmatch(denominator)
	if match == nil {
		return 1_000_000
	}
	scale := 1.0
	switch strings.ToLower(match[2]) {
	case "k":
		scale = 1_000
	case "m":
		scale = 1_000_000
	case "b":
		scale = 1_000_000_000
	}
	count, _ := strconv.ParseFloat(match[1], 64)
	if tokens := count * scale; tokens > 0 {
		return tokens
	}
	return 1_000_000
}

// jsRound is Math.round: nearest integer, ties toward +Infinity.
func jsRound(value float64) float64 {
	floor := math.Floor(value)
	if value-floor >= 0.5 {
		return floor + 1
	}
	return floor
}

// devinModelCost reads the config's own rate card, stopping at the Sidekick
// marker that separates a composite's components.
func devinModelCost(config *devinwire.ClientModelConfig) devinCost {
	var cost devinCost
	for _, dimension := range config.Dimensions {
		label := strings.ToLower(strings.TrimSpace(dimension.Label))
		if label == "sidekick" {
			break
		}
		if dimension.Kind != devinwire.DimensionCost && dimension.Kind != devinwire.DimensionCostFuzzy {
			continue
		}
		perMillion := jsRound(dimension.Value*1_000_000/devinCostDenominatorTokens(dimension.Denominator)*1e6) / 1e6
		switch label {
		case "input":
			cost.input = perMillion
		case "cached input":
			cost.cacheRead = perMillion
		case "output":
			cost.output = perMillion
		}
	}
	return cost
}

// devinModelSpec is one config as a raw spec keyed on its wire uid.
func devinModelSpec(config *devinwire.ClientModelConfig, uid, baseURL string, isAssignRouter bool) devinSpec {
	var features *devinwire.ModelFeatures
	if config.Info != nil {
		features = config.Info.Features
	}
	supportsImages := config.SupportsImages
	if features != nil {
		supportsImages = features.SupportsImages
	}
	input := []string{"text"}
	if supportsImages && !devinImageBlindUIDs[uid] {
		input = append(input, "image")
	}
	var maxOutput, infoContext int32
	if config.Info != nil {
		maxOutput, infoContext = config.Info.MaxOutputTokens, config.Info.MaxTokens
	}
	// Current catalogs carry each lane's context window on ModelInfo.maxTokens.
	contextWindow := int(infoContext)
	if config.MaxTokens > 0 {
		contextWindow = int(config.MaxTokens)
	}
	if contextWindow <= 0 {
		contextWindow = devinDefaultContextWindow
	}
	maxTokens := int(maxOutput)
	if maxTokens <= 0 {
		maxTokens = devinDefaultMaxTokens
	}
	name := strings.TrimSpace(config.Label)
	if name == "" {
		name = uid
	}
	return devinSpec{
		model: NeoPiModel{ID: uid, Name: name, API: "devin-agent", Provider: "devin", BaseURL: baseURL,
			Reasoning: devinSupportsThinking(config), Input: input, ContextWindow: contextWindow, MaxTokens: maxTokens,
			DevinCompat: &NeoPiDevinCompat{ModelRouter: isAssignRouter, SupportsParallelToolCalls: features != nil && features.SupportsParallelToolCalls}},
		// Routers ship no features; Cascade serves only tool-calling models.
		supportsTools: features == nil || features.SupportsToolCalls,
		cost:          devinModelCost(config),
	}
}

// devinFusionLead resolves fusion-<lead>[-fast]-sidekick-<sidekick> to its
// live lead uid. pairing reports whether uid is a pairing at all; an empty
// lead with pairing set means the lead is not live and the pairing is not
// listed.
func devinFusionLead(uid string, live map[string]*devinwire.ClientModelConfig) (lead string, pairing bool) {
	if !strings.HasPrefix(uid, "fusion-") {
		return "", false
	}
	cut := strings.Index(uid, "-sidekick-")
	if cut <= len("fusion-") {
		return "", false
	}
	lead = uid[len("fusion-"):cut]
	if live[lead] != nil {
		return lead, true
	}
	if base, ok := strings.CutSuffix(lead, "-fast"); ok {
		if live[base+"-priority"] != nil {
			return base + "-priority", true
		}
		if live[base] != nil {
			return base, true
		}
	}
	return "", true
}

// devinRouteFusionLead points a pairing at its lead: only the lead runs, so
// its limits and tool support are the ones a caller budgets against.
func devinRouteFusionLead(spec *devinSpec, lead devinSpec) {
	spec.model.RequestModelID = lead.model.ID
	spec.model.Reasoning = lead.model.Reasoning
	spec.model.Input = lead.model.Input
	spec.supportsTools = lead.supportsTools
	spec.cost = lead.cost
	spec.model.ContextWindow = lead.model.ContextWindow
	spec.model.MaxTokens = lead.model.MaxTokens
	spec.model.DevinCompat = &NeoPiDevinCompat{ModelRouter: spec.model.DevinCompat.ModelRouter,
		SupportsParallelToolCalls: lead.model.DevinCompat.SupportsParallelToolCalls}
}

// devinLane is one server-declared family lane (DevinFamilyLane).
type devinLane struct {
	id, name      string
	members       []string
	defaultMember string
	routing       map[string]string
}

// devinCollectLane files config under its server-declared family lane.
func devinCollectLane(lanes []*devinLane, config *devinwire.ClientModelConfig, uid string) []*devinLane {
	metadata := config.Family
	if metadata == nil {
		return lanes
	}
	label := strings.TrimSpace(metadata.Label)
	if label == "" {
		return lanes
	}
	effort, hasEffort := "", false
	thinking, hasThinking := false, false
	fast, oneMillion := false, false
	for _, entry := range metadata.Entries {
		if entry.Value == nil {
			continue
		}
		key := strings.TrimSpace(devinNonAlnum.ReplaceAllString(strings.ToLower(entry.Key), " "))
		switch key {
		case "fast mode":
			fast = entry.Value.Order == 1
		case "thinking":
			thinking, hasThinking = entry.Value.Order == 1, true
		case "1m context":
			oneMillion = entry.Value.Order == 1
		case "effort", "reasoning effort":
			effort, hasEffort = devinFamilyEffortByName[devinNonAlnum.ReplaceAllString(strings.ToLower(entry.Value.Name), "")]
		}
	}
	// Claude pairs share one effort label; the Thinking axis decides "off".
	if hasThinking && !thinking {
		effort, hasEffort = "off", true
	}
	baseID := neoPiSlugify(label)
	if baseID == "" {
		return lanes
	}
	laneID, name := baseID, label
	if oneMillion {
		laneID, name = laneID+"-1m", name+" 1M"
	}
	if fast {
		laneID, name = laneID+"-fast", name+" Fast"
	}
	var lane *devinLane
	for _, candidate := range lanes {
		if candidate.id == laneID {
			lane = candidate
			break
		}
	}
	if lane == nil {
		lane = &devinLane{id: laneID, name: name, routing: map[string]string{}}
		lanes = append(lanes, lane)
	}
	lane.members = append(lane.members, uid)
	if lane.defaultMember == "" && (config.IsDefaultModelInFamily || metadata.IsDefaultModelInFamily) {
		lane.defaultMember = uid
	}
	if _, taken := lane.routing[effort]; hasEffort && !taken {
		lane.routing[effort] = uid
	}
	return lanes
}

// neoPiSlugify is pi-utils slugify without a length bound.
func neoPiSlugify(input string) string {
	return strings.Trim(devinNonAlnum.ReplaceAllString(strings.ToLower(input), "-"), "-")
}

// devinDynamicFamilies collapses the lanes that declare an effort ladder,
// hoisting the server default member so it becomes the default wire id.
func devinDynamicFamilies(lanes []*devinLane) []devinVariantFamily {
	var families []devinVariantFamily
	for _, lane := range lanes {
		var efforts []string
		for _, effort := range neoPiThinkingEfforts {
			if _, ok := lane.routing[effort]; ok {
				efforts = append(efforts, effort)
			}
		}
		if len(efforts) == 0 {
			continue
		}
		members := lane.members
		thinking := &NeoPiThinking{Mode: "effort", Efforts: efforts}
		if lane.defaultMember != "" {
			members = append([]string{lane.defaultMember}, slices.DeleteFunc(slices.Clone(lane.members), func(uid string) bool { return uid == lane.defaultMember })...)
			for _, effort := range efforts {
				if lane.routing[effort] == lane.defaultMember {
					thinking.DefaultLevel = effort
					break
				}
			}
		}
		// No off route means no wire id serves the family without thinking.
		if _, ok := lane.routing["off"]; !ok {
			thinking.RequiresEffort = true
		}
		families = append(families, devinVariantFamily{ID: lane.id, Name: lane.name, Members: members,
			DefaultMember: lane.defaultMember, Routing: maps.Clone(lane.routing), Thinking: thinking})
	}
	return families
}

// devinIsCollapsed is collapseWithTable's existingCollapsed test.
func devinIsCollapsed(spec devinSpec) bool {
	return spec.model.RequestModelID != "" || (spec.model.Thinking != nil && spec.model.Thinking.EffortRouting != nil)
}

// devinReconcileDefaultMember re-points a collapsed row's default wire id to
// the family's declared default when that member is a live route. present
// nil means every member counts as present.
func devinReconcileDefaultMember(spec devinSpec, family *devinVariantFamily, present map[string]bool) (devinSpec, bool) {
	defaultMember := family.DefaultMember
	if defaultMember == "" || defaultMember == spec.model.ID {
		return spec, false
	}
	target := defaultMember
	if present != nil && !present[defaultMember] {
		target = ""
		for _, id := range family.Members {
			if present[id] {
				target = id
				break
			}
		}
	}
	if target == "" || spec.model.RequestModelID == target || spec.model.Thinking == nil || spec.model.Thinking.EffortRouting == nil {
		return spec, false
	}
	for _, key := range devinRoutingKeys {
		if spec.model.Thinking.EffortRouting[key] == target {
			spec.model.RequestModelID = target
			return spec, true
		}
	}
	return spec, false
}

// devinCollapseWithTable is collapseWithTable for the family shapes Devin
// produces: no templates, retired members, extra aliases or effort budgets
// (capture_devin_npi_collapse.ts refuses a table that has them).
func devinCollapseWithTable(specs []devinSpec, families []devinVariantFamily) []devinSpec {
	byID := map[string]int{}
	for i, spec := range specs {
		if _, ok := byID[spec.model.ID]; !ok {
			byID[spec.model.ID] = i
		}
	}
	replacement := map[string]devinSpec{}
	familyBySpec := map[string]string{}
	for f := range families {
		family := &families[f]
		existingIndex, hasExisting := byID[family.ID]
		var existing devinSpec
		if hasExisting {
			existing = specs[existingIndex]
		}
		existingCollapsed := hasExisting && devinIsCollapsed(existing)
		var rawPresent []string
		for _, id := range family.Members {
			if _, ok := byID[id]; ok && !(id == family.ID && existingCollapsed) {
				rawPresent = append(rawPresent, id)
			}
		}
		if len(rawPresent) == 0 {
			// Pass-through of an already collapsed row; only its default
			// member is reconciled for Devin's family shapes.
			if existingCollapsed {
				if refreshed, changed := devinReconcileDefaultMember(existing, family, nil); changed {
					familyBySpec[family.ID] = family.ID
					replacement[family.ID] = refreshed
				}
			}
			continue
		}
		for _, id := range rawPresent {
			familyBySpec[id] = family.ID
		}
		if hasExisting {
			familyBySpec[family.ID] = family.ID
		}
		present := map[string]bool{}
		for _, id := range rawPresent {
			present[id] = true
		}
		if existingCollapsed {
			// Mixed input: the collapsed row wins and stale raw members dedupe away.
			replacement[family.ID], _ = devinReconcileDefaultMember(existing, family, present)
			continue
		}
		members := make([]devinSpec, 0, len(rawPresent))
		for _, id := range rawPresent {
			members = append(members, specs[byID[id]])
		}
		routing := map[string]string{}
		hasEffortRoute := false
		for _, key := range devinRoutingKeys {
			if target := family.Routing[key]; target != "" && present[target] {
				routing[key] = target
				if key != "off" {
					hasEffortRoute = true
				}
			}
		}
		reasoning := hasEffortRoute
		textInput, imageInput := false, false
		collapsed := members[0]
		for _, member := range members {
			reasoning = reasoning || member.model.Reasoning
			textInput = textInput || slices.Contains(member.model.Input, "text")
			imageInput = imageInput || slices.Contains(member.model.Input, "image")
			collapsed.model.ContextWindow = max(collapsed.model.ContextWindow, member.model.ContextWindow)
			collapsed.model.MaxTokens = max(collapsed.model.MaxTokens, member.model.MaxTokens)
			collapsed.isProviderDefault = collapsed.isProviderDefault || member.isProviderDefault
		}
		collapsed.model.ID, collapsed.model.Name, collapsed.model.Reasoning = family.ID, family.Name, reasoning
		collapsed.model.Input = nil
		if textInput {
			collapsed.model.Input = append(collapsed.model.Input, "text")
		}
		if imageInput {
			collapsed.model.Input = append(collapsed.model.Input, "image")
		}
		defaultWire := rawPresent[0]
		if family.DefaultMember != "" && present[family.DefaultMember] {
			defaultWire = family.DefaultMember
		}
		collapsed.model.RequestModelID = ""
		if defaultWire != family.ID {
			collapsed.model.RequestModelID = defaultWire
		}
		// A surface-less family keeps reasoning but carries no thinking.
		collapsed.model.Thinking = nil
		if reasoning && family.Thinking != nil {
			thinking := family.Thinking.Clone()
			if len(routing) > 0 {
				thinking.EffortRouting = routing
			}
			collapsed.model.Thinking = thinking
		}
		replacement[family.ID] = collapsed
	}
	if len(replacement) == 0 {
		return specs
	}
	emitted := map[string]bool{}
	out := make([]devinSpec, 0, len(specs))
	for _, spec := range specs {
		familyID, ok := familyBySpec[spec.model.ID]
		if !ok {
			out = append(out, spec)
			continue
		}
		if emitted[familyID] {
			continue
		}
		emitted[familyID] = true
		if collapsed, ok := replacement[familyID]; ok {
			out = append(out, collapsed)
		}
	}
	return out
}

// devinThinkingVariantBase is deriveThinkingPairFamilies' base id for spec:
// collapseVariantId's thinking-suffix logical id, else the first pair-token
// strip; "" when id has no thinking variant form.
func devinThinkingVariantBase(id string) string {
	lower := strings.ToLower(id)
	if !slices.Contains(devinCollapse.EffortAliases, lower) {
		bare := lower[strings.LastIndex(lower, "/")+1:]
		var winner *devinSuffixRule
		for i := range devinCollapse.Suffixes {
			rule := &devinCollapse.Suffixes[i]
			if !strings.HasSuffix(lower, rule.Suffix) || (rule.ExceptBarePrefix != "" && strings.HasPrefix(bare, rule.ExceptBarePrefix)) {
				continue
			}
			if winner == nil || len(rule.Suffix) > len(winner.Suffix) {
				winner = rule
			}
		}
		if winner != nil && winner.Thinking {
			return id[:len(id)-len(winner.Suffix)]
		}
	}
	return devinStripThinkingToken(id)
}

func devinTokenChar(c byte) bool { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') }

// devinStripThinkingToken is stripThinkingVariantSuffix: the first
// "-<pairToken>" not followed by a token character nor negated by "no"/"non".
func devinStripThinkingToken(id string) string {
	lower := strings.ToLower(id)
	for _, token := range devinCollapse.PairTokens {
		needle := "-" + token
		for from := 0; from < len(lower); {
			index := strings.Index(lower[from:], needle)
			if index < 0 {
				break
			}
			index += from
			end := index + len(needle)
			start := index
			for start > 0 && devinTokenChar(lower[start-1]) {
				start--
			}
			preceding := lower[start:index]
			if (end >= len(lower) || !devinTokenChar(lower[end])) && preceding != "non" && preceding != "no" {
				return id[:index] + id[end:]
			}
			from = index + 1
		}
	}
	return ""
}

// devinPairFamilies is deriveThinkingPairFamilies over the devin rows: every
// live X / X-thinking pair the reviewed table does not claim and whose known
// prices agree.
func devinPairFamilies(specs []devinSpec) []devinVariantFamily {
	byID := map[string]devinSpec{}
	for _, spec := range specs {
		if _, ok := byID[spec.model.ID]; !ok {
			byID[spec.model.ID] = spec
		}
	}
	claimed, familyIDs := map[string]bool{}, map[string]bool{}
	for _, family := range devinCollapse.Families {
		familyIDs[family.ID] = true
		for _, member := range family.Members {
			if member != family.ID {
				claimed[strings.ToLower(member)] = true
			}
		}
	}
	var families []devinVariantFamily
	for _, spec := range specs {
		baseID := devinThinkingVariantBase(spec.model.ID)
		if baseID == "" || baseID == spec.model.ID {
			continue
		}
		base, ok := byID[baseID]
		if !ok {
			continue
		}
		if claimed[strings.ToLower(spec.model.ID)] || claimed[strings.ToLower(baseID)] || familyIDs[spec.model.ID] || familyIDs[baseID] {
			continue
		}
		specPriced := spec.cost.input != 0 || spec.cost.output != 0
		basePriced := base.cost.input != 0 || base.cost.output != 0
		if specPriced && basePriced && spec.cost != base.cost {
			continue
		}
		// Devin rows trust explicit thinking only, so NeoPi's policy deriver
		// yields nothing and an unbaked pair falls back to the budget ladder.
		surface := &NeoPiThinking{Mode: "budget", Efforts: []string{"minimal", "low", "medium", "high"}}
		baked := spec.model.Thinking
		if baked == nil {
			baked = base.model.Thinking
		}
		if baked != nil && len(baked.Efforts) > 0 {
			surface = baked.Clone()
			surface.EffortRouting, surface.RequiresEffort = nil, false
		}
		routing := map[string]string{"off": base.model.ID}
		for _, effort := range surface.Efforts {
			routing[effort] = spec.model.ID
		}
		families = append(families, devinVariantFamily{ID: base.model.ID, Name: base.model.Name,
			Members: []string{base.model.ID, spec.model.ID}, Routing: routing, Thinking: surface})
	}
	return families
}
