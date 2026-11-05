package neopicatalog

import (
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// This file ports compat/collapse.ts. Reviewed families that use retired
// members, effort budgets, thinking suppression or absent-route preservation
// are rejected by the export (capture_cursor_npi_catalog.ts), so the paths
// only those features reach (reconcileRetiredRouting, refreshCollapsedThinking,
// the extra-alias refresh) are identities here and are not ported.

// Family is EffortVariantFamily.
type Family struct {
	ID            string
	Name          string
	Members       []string
	DefaultMember string
	Routing       map[string]string
	Thinking      *Thinking // nil: no capability surface
	ExtraAliases  []string
}

// Template is VariantFamilyTemplate: a family whose ids carry {rev}.
type Template struct {
	Family   Family
	Revision []RevisionTerm // nil: unbounded
	pattern  *regexp.Regexp
}

// Table is VariantCollapseTable.
type Table struct {
	Families  []Family
	Templates []Template

	once  sync.Once
	index *aliasIndex
}

const revisionPlaceholder = "{rev}"

func compiledFamily(c CompiledFamily) Family {
	family := Family{ID: c.ID, Name: c.Name, Members: c.Members, Routing: c.Routing, DefaultMember: c.DefaultMember, ExtraAliases: c.ExtraAliases}
	if !c.NoThinking && c.Mode != "" {
		efforts := c.Efforts
		if efforts == nil {
			efforts = []string{}
		}
		family.Thinking = &Thinking{Mode: c.Mode, Efforts: efforts, DefaultLevel: c.DefaultLevel, RequiresEffort: c.RequiresEffort}
	}
	return family
}

func compiledTemplate(c CompiledFamily) Template {
	family := compiledFamily(c)
	alternatives := []string{}
	for _, id := range append(append([]string{family.ID}, family.Members...), family.ExtraAliases...) {
		parts := strings.Split(id, revisionPlaceholder)
		for i := range parts {
			parts[i] = regexp.QuoteMeta(parts[i])
		}
		alternatives = append(alternatives, strings.Join(parts, `(\d+(?:\.\d+){0,2})`))
	}
	template := Template{Family: family, pattern: regexp.MustCompile(`(?i)^(?:` + strings.Join(alternatives, "|") + `)$`)}
	if c.Revision != "" {
		if terms, ok := ParseRevisionConstraint(c.Revision); ok {
			template.Revision = terms
		}
	}
	return template
}

// ReviewedTable compiles provider's reviewed families (buildCompiledTables).
func ReviewedTable(provider string) *Table {
	table := &Table{}
	for _, compiled := range VariantFamilies(provider) {
		if strings.Contains(compiled.ID, revisionPlaceholder) {
			table.Templates = append(table.Templates, compiledTemplate(compiled))
		} else {
			table.Families = append(table.Families, compiledFamily(compiled))
		}
	}
	return table
}

func instantiateTemplate(template *Template, rev string) (Family, bool) {
	if template.Revision != nil {
		parsed, ok := ParseRevision(rev)
		if !ok || !parsed.Satisfies(template.Revision) {
			return Family{}, false
		}
	}
	fill := func(id string) string { return strings.ReplaceAll(id, revisionPlaceholder, rev) }
	source := template.Family
	family := Family{ID: fill(source.ID), Name: fill(source.Name), Thinking: source.Thinking.Clone(), Routing: map[string]string{}}
	for _, key := range RoutingKeys {
		if target, ok := source.Routing[key]; ok {
			family.Routing[key] = fill(target)
		}
	}
	for _, member := range source.Members {
		family.Members = append(family.Members, fill(member))
	}
	if source.DefaultMember != "" {
		family.DefaultMember = fill(source.DefaultMember)
	}
	for _, alias := range source.ExtraAliases {
		family.ExtraAliases = append(family.ExtraAliases, fill(alias))
	}
	return family, true
}

func templateFamilyFor(templates []Template, id string) (Family, bool) {
	for i := range templates {
		match := templates[i].pattern.FindStringSubmatch(id)
		if match == nil {
			continue
		}
		rev := ""
		for _, group := range match[1:] {
			if group != "" {
				rev = group
				break
			}
		}
		if rev == "" {
			continue
		}
		if family, ok := instantiateTemplate(&templates[i], rev); ok {
			return family, true
		}
	}
	return Family{}, false
}

func instantiateTemplates(table *Table, ids []string) []Family {
	if len(table.Templates) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, family := range table.Families {
		seen[family.ID] = true
	}
	var out []Family
	for _, id := range ids {
		family, ok := templateFamilyFor(table.Templates, id)
		if !ok || seen[family.ID] {
			continue
		}
		seen[family.ID] = true
		out = append(out, family)
	}
	return out
}

// aliasIndex is VariantAliasIndex over one table.
type aliasIndex struct {
	mu        sync.Mutex
	forward   map[string]string
	familyIDs map[string]bool
	templates []Template
}

func (x *aliasIndex) add(from, to string) {
	if from == to {
		return
	}
	if _, taken := x.forward[strings.ToLower(from)]; taken {
		return
	}
	x.forward[strings.ToLower(from)] = to
}

func (x *aliasIndex) addFamily(family Family) {
	x.familyIDs[family.ID] = true
	for _, member := range family.Members {
		x.add(member, family.ID)
	}
	for _, alias := range family.ExtraAliases {
		x.add(alias, family.ID)
	}
}

func (x *aliasIndex) learn(id string) {
	if len(x.templates) == 0 {
		return
	}
	if family, ok := templateFamilyFor(x.templates, id); ok && !x.familyIDs[family.ID] {
		x.addFamily(family)
	}
}

func (x *aliasIndex) resolve(id string) (string, bool) {
	x.mu.Lock()
	defer x.mu.Unlock()
	lower := strings.ToLower(id)
	x.learn(lower)
	target, ok := x.forward[lower]
	return target, ok
}

func (x *aliasIndex) isFamily(id string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.learn(id)
	return x.familyIDs[id]
}

func (t *Table) aliases() *aliasIndex {
	t.once.Do(func() {
		t.index = &aliasIndex{forward: map[string]string{}, familyIDs: map[string]bool{}, templates: t.Templates}
		for _, family := range t.Families {
			t.index.addFamily(family)
		}
	})
	return t.index
}

// FamilyID is reviewedVariantFamilyId: the logical id of the family claiming
// memberID as a member or extra alias.
func (t *Table) FamilyID(memberID string) (string, bool) {
	return t.aliases().resolve(strings.TrimSpace(memberID))
}

func isCollapsedSpec(s *Spec) bool {
	return s.RequestModelID != nil || s.Thinking != nil && s.Thinking.EffortRouting != nil
}

// reconcileDefaultMember re-points a collapsed snapshot's default wire id to
// the family's declared default member when that member is a live route.
// present nil means every member counts as present. Returns s when unchanged.
func reconcileDefaultMember(s *Spec, family *Family, present map[string]bool) *Spec {
	if family.DefaultMember == "" || family.DefaultMember == s.ID {
		return s
	}
	target := family.DefaultMember
	if present != nil && !present[target] {
		target = ""
		for _, id := range family.Members {
			if present[id] {
				target = id
				break
			}
		}
	}
	if target == "" || s.RequestModelID != nil && *s.RequestModelID == target {
		return s
	}
	if s.Thinking == nil || s.Thinking.EffortRouting == nil {
		return s
	}
	for _, key := range RoutingKeys {
		if routed, ok := s.Thinking.EffortRouting[key]; ok && routed == target {
			out := s.Clone()
			out.RequestModelID = Ptr(target)
			return out
		}
	}
	return s
}

// IsCursorMaxModeWireID is isCursorMaxModeWireId: whether a Cursor wire id
// names an extended tier upstream serves only in max mode.
func IsCursorMaxModeWireID(wireModelID string) bool {
	effort := CollapseVariantID("cursor", wireModelID).Effort
	return effort == "xhigh" || effort == "max"
}

func reconcileCursorMaxModeFromRouting(s *Spec) *Spec {
	if s.Provider != "cursor" || s.CursorMaxMode != nil && *s.CursorMaxMode || s.Thinking == nil || s.Thinking.EffortRouting == nil {
		return s
	}
	for _, target := range s.Thinking.EffortRouting {
		if IsCursorMaxModeWireID(target) {
			out := s.Clone()
			out.CursorMaxMode = Ptr(true)
			return out
		}
	}
	return s
}

func cursorMaxModeRoutesOf(provider string, members []*Spec) map[string]bool {
	if provider != "cursor" {
		return nil
	}
	var routes map[string]bool
	for _, member := range members {
		if member.CursorMaxMode == nil {
			continue
		}
		if routes == nil {
			routes = map[string]bool{}
		}
		routes[member.ID] = *member.CursorMaxMode
	}
	return routes
}

func reconcileCursorMaxMode(s *Spec, members []*Spec) *Spec {
	routes := cursorMaxModeRoutesOf(s.Provider, members)
	lifts := !(s.CursorMaxMode != nil && *s.CursorMaxMode) && slices.ContainsFunc(members, func(m *Spec) bool { return m.CursorMaxMode != nil && *m.CursorMaxMode })
	if routes == nil && !lifts {
		return s
	}
	out := s.Clone()
	if lifts {
		out.CursorMaxMode = Ptr(true)
	}
	if routes != nil {
		merged := maps.Clone(s.CursorMaxModeRoutes)
		if merged == nil {
			merged = map[string]bool{}
		}
		maps.Copy(merged, routes)
		out.CursorMaxModeRoutes = merged
	}
	return out
}

func maxOrNull(values []*int) *int {
	var best *int
	for _, value := range values {
		if value != nil && (best == nil || *value > *best) {
			best = Ptr(*value)
		}
	}
	return best
}

// CollapseWithTable is collapseWithTable: every family of table found in
// specs collapses into one logical spec at its first member's position.
// Non-member specs pass through by reference.
func CollapseWithTable(specs []*Spec, table *Table) []*Spec {
	byID := map[string]*Spec{}
	var order []string
	for _, spec := range specs {
		if _, ok := byID[spec.ID]; !ok {
			byID[spec.ID] = spec
			order = append(order, spec.ID)
		}
	}
	replacement := map[string]*Spec{}
	familyBySpec := map[string]string{}
	families := table.Families
	if instantiated := instantiateTemplates(table, order); len(instantiated) > 0 {
		families = append(slices.Clone(families), instantiated...)
	}
	for f := range families {
		family := &families[f]
		existing := byID[family.ID]
		existingCollapsed := existing != nil && isCollapsedSpec(existing)
		var rawPresent []string
		for _, id := range family.Members {
			if _, ok := byID[id]; ok && !(id == family.ID && existingCollapsed) {
				rawPresent = append(rawPresent, id)
			}
		}
		if len(rawPresent) == 0 {
			if existingCollapsed {
				refreshed := reconcileCursorMaxModeFromRouting(reconcileDefaultMember(existing, family, nil))
				if refreshed != existing {
					familyBySpec[family.ID] = family.ID
					replacement[family.ID] = refreshed
				}
			}
			continue
		}
		for _, id := range rawPresent {
			familyBySpec[id] = family.ID
		}
		if existing != nil {
			familyBySpec[family.ID] = family.ID
		}
		members := make([]*Spec, 0, len(rawPresent))
		present := map[string]bool{}
		for _, id := range rawPresent {
			members = append(members, byID[id])
			present[id] = true
		}
		if existingCollapsed {
			replacement[family.ID] = reconcileCursorMaxMode(reconcileDefaultMember(existing, family, present), members)
			continue
		}
		first := members[0]
		routing := map[string]string{}
		hasEffortRoute := false
		for _, key := range RoutingKeys {
			if target, ok := family.Routing[key]; ok && present[target] {
				routing[key] = target
				if key != "off" {
					hasEffortRoute = true
				}
			}
		}
		reasoning := hasEffortRoute
		textInput, imageInput := false, false
		contextWindows := make([]*int, 0, len(members))
		maxTokens := make([]*int, 0, len(members))
		providerDefault, maxMode := false, false
		for _, member := range members {
			reasoning = reasoning || member.Reasoning
			textInput = textInput || slices.Contains(member.Input, "text")
			imageInput = imageInput || slices.Contains(member.Input, "image")
			contextWindows = append(contextWindows, member.ContextWindow)
			maxTokens = append(maxTokens, member.MaxTokens)
			providerDefault = providerDefault || member.IsProviderDefault != nil && *member.IsProviderDefault
			maxMode = maxMode || member.CursorMaxMode != nil && *member.CursorMaxMode
		}
		collapsed := first.Clone()
		collapsed.ID, collapsed.Name, collapsed.Reasoning = family.ID, family.Name, reasoning
		collapsed.Input = []string{}
		if textInput {
			collapsed.Input = append(collapsed.Input, "text")
		}
		if imageInput {
			collapsed.Input = append(collapsed.Input, "image")
		}
		collapsed.ContextWindow, collapsed.MaxTokens = maxOrNull(contextWindows), maxOrNull(maxTokens)
		if maxMode {
			collapsed.CursorMaxMode = Ptr(true)
		}
		if routes := cursorMaxModeRoutesOf(first.Provider, members); routes != nil {
			collapsed.CursorMaxModeRoutes = routes
		}
		if providerDefault {
			collapsed.IsProviderDefault = Ptr(true)
		}
		defaultWire := rawPresent[0]
		if family.DefaultMember != "" && present[family.DefaultMember] {
			defaultWire = family.DefaultMember
		}
		if defaultWire == family.ID {
			collapsed.RequestModelID = nil
		} else {
			collapsed.RequestModelID = Ptr(defaultWire)
		}
		collapsed.Thinking = nil
		if reasoning && family.Thinking != nil {
			thinking := family.Thinking.Clone()
			if len(routing) > 0 {
				thinking.EffortRouting = routing
			}
			collapsed.Thinking = thinking
		}
		replacement[family.ID] = collapsed
	}
	if len(replacement) == 0 {
		return slices.Clone(specs)
	}
	emitted := map[string]bool{}
	out := make([]*Spec, 0, len(specs))
	for _, spec := range specs {
		familyID, ok := familyBySpec[spec.ID]
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

// tierFamily builds one effort-tier family from a tier→wire-id map.
func tierFamily(id, name string, routes map[string]string, efforts []string) Family {
	routing := map[string]string{}
	if off, ok := routes["off"]; ok {
		routing["off"] = off
	}
	for _, effort := range efforts {
		if target, ok := routes[effort]; ok {
			routing[effort] = target
		}
	}
	var members []string
	for _, key := range RoutingKeys {
		if target, ok := routes[key]; ok && !slices.Contains(members, target) {
			members = append(members, target)
		}
	}
	_, hasOff := routes["off"]
	return Family{ID: id, Name: name, Members: members, Routing: routing,
		Thinking: &Thinking{Mode: "effort", Efforts: slices.Clone(efforts), RequiresEffort: !hasOff}}
}

var (
	cursorTierIDPattern     = regexp.MustCompile(`^(.+?)-(extra-high|none|minimal|low|medium|high|xhigh|max)(-fast)?$`)
	cursorTierBasePattern   = regexp.MustCompile(`-(extra-high|none|minimal|low|medium|high|xhigh|max)$`)
	cursorThinkingToken     = regexp.MustCompile(`(^|-)thinking($|-)`)
	cursorTierNameSuffix    = regexp.MustCompile(`(?i)\s+(extra-high|none|minimal|low|medium|high|xhigh|max)(\s+fast)?$`)
	cursorTierByToken       = map[string]string{"none": "none", "minimal": "minimal", "low": "low", "medium": "medium", "high": "high", "extra-high": "xhigh", "xhigh": "xhigh", "max": "max"}
	defaultPairEffortLadder = []string{"minimal", "low", "medium", "high"}
)

type cursorTierMember struct {
	baseID string
	fast   bool
	spec   *Spec
	tier   string
}

func collapsedCursorLogicalMatches(s *Spec, members []cursorTierMember) bool {
	if s.Thinking == nil || s.Thinking.EffortRouting == nil {
		return false
	}
	for _, member := range members {
		if s.RequestModelID != nil && *s.RequestModelID == member.spec.ID {
			continue
		}
		matched := false
		for _, key := range RoutingKeys {
			if target, ok := s.Thinking.EffortRouting[key]; ok && target == member.spec.ID {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

// DeriveCursorEffortFamilies is deriveCursorEffortFamilies: safe per-effort
// families from live Cursor wire ids.
func DeriveCursorEffortFamilies(specs []*Spec) []Family {
	byID := map[string]*Spec{}
	groups := map[string][]cursorTierMember{}
	var groupOrder []string
	candidateBases := map[string]bool{}
	for _, spec := range specs {
		if _, ok := byID[spec.ID]; !ok {
			byID[spec.ID] = spec
		}
		match := cursorTierIDPattern.FindStringSubmatch(spec.ID)
		if match == nil {
			continue
		}
		baseID, tier, fast := match[1], cursorTierByToken[match[2]], match[3] != ""
		if baseID == "" || tier == "" {
			continue
		}
		key := baseID + "\x00standard"
		if fast {
			key = baseID + "\x00fast"
		}
		if _, ok := groups[key]; !ok {
			groupOrder = append(groupOrder, key)
		}
		groups[key] = append(groups[key], cursorTierMember{baseID: baseID, fast: fast, spec: spec, tier: tier})
		candidateBases[baseID] = true
	}
	unsafe := map[string]bool{}
	for _, key := range groupOrder {
		group := groups[key]
		first := group[0]
		baseID := first.baseID
		standardGroup := groups[baseID+"\x00standard"]
		laneID := baseID
		if first.fast {
			laneID += "-fast"
		}
		standardBase, laneBase := byID[baseID], byID[laneID]
		tiers := map[string]bool{}
		for _, member := range group {
			tiers[member.tier] = true
		}
		unsafeGroup := standardBase != nil && !collapsedCursorLogicalMatches(standardBase, standardGroup) ||
			laneBase != nil && !collapsedCursorLogicalMatches(laneBase, group) ||
			len(tiers) != len(group) ||
			cursorTierBasePattern.MatchString(baseID) ||
			cursorThinkingToken.MatchString(baseID) ||
			candidateBases[baseID+"-thinking"] ||
			byID[baseID+"-thinking"] != nil || byID[baseID+"-thinking-fast"] != nil || byID[baseID+"-fast-thinking"] != nil
		for _, member := range group {
			if member.spec.Thinking != nil || member.spec.RequestModelID != nil || candidateBases[baseID+"-"+member.tier] {
				unsafeGroup = true
			}
		}
		if unsafeGroup {
			unsafe[baseID] = true
		}
	}
	var families []Family
	for _, key := range groupOrder {
		group := groups[key]
		first := group[0]
		if len(group) < 2 || unsafe[first.baseID] {
			continue
		}
		homogeneous := true
		for _, member := range group {
			m, f := member.spec, first.spec
			if m.API != f.API || m.BaseURL != f.BaseURL || !reflect.DeepEqual(m.ContextWindow, f.ContextWindow) ||
				!reflect.DeepEqual(m.MaxTokens, f.MaxTokens) || !reflect.DeepEqual(m.CursorMaxMode, f.CursorMaxMode) ||
				!reflect.DeepEqual(m.Cost, f.Cost) {
				homogeneous = false
				break
			}
		}
		if !homogeneous {
			continue
		}
		routes := map[string]string{}
		for _, member := range group {
			if member.tier == "none" {
				routes["off"] = member.spec.ID
			} else {
				routes[member.tier] = member.spec.ID
			}
		}
		var efforts []string
		for _, effort := range ThinkingEfforts {
			if _, ok := routes[effort]; ok {
				efforts = append(efforts, effort)
			}
		}
		if len(efforts) == 0 {
			continue
		}
		stripped := strings.TrimSpace(cursorTierNameSuffix.ReplaceAllString(first.spec.Name, ""))
		baseName := stripped
		if stripped == first.spec.ID || stripped == "" {
			baseName = first.baseID
		}
		id, name := first.baseID, baseName
		if first.fast {
			id, name = id+"-fast", name+" Fast"
		}
		families = append(families, tierFamily(id, name, routes, efforts))
	}
	return families
}

// Policy supplies the thinking ladder NeoPi's rule engine derives for a spec
// (resolveModelPolicy({...spec, reasoning: true, thinking: undefined}).thinking).
type Policy interface {
	DerivedThinking(spec *Spec) *Thinking
}

func thinkingSurface(t *Thinking) *Thinking {
	surface := t.Clone()
	surface.EffortRouting, surface.SuppressWhenOff, surface.RequiresEffort = nil, false, false
	return surface
}

func derivePairThinkingSurface(thinkingSpec, baseSpec *Spec, policy Policy) *Thinking {
	baked := thinkingSpec.Thinking
	if baked == nil {
		baked = baseSpec.Thinking
	}
	if baked != nil && len(baked.Efforts) > 0 {
		return thinkingSurface(baked)
	}
	probe := thinkingSpec.Clone()
	probe.Reasoning, probe.Thinking = true, nil
	if derived := policy.DerivedThinking(probe); derived != nil && len(derived.Efforts) > 0 {
		return thinkingSurface(derived)
	}
	return &Thinking{Mode: "budget", Efforts: slices.Clone(defaultPairEffortLadder)}
}

// DeriveThinkingPairFamilies is deriveThinkingPairFamilies: an X / X-thinking
// family for every live pair the reviewed table does not claim.
func DeriveThinkingPairFamilies(specs []*Spec, table *Table, provider string, policy Policy) []Family {
	byID := map[string]*Spec{}
	for _, spec := range specs {
		if _, ok := byID[spec.ID]; !ok {
			byID[spec.ID] = spec
		}
	}
	var claimed *aliasIndex
	if table != nil {
		claimed = table.aliases()
	}
	var families []Family
	for _, spec := range specs {
		baseID := ""
		if collapsed := CollapseVariantID(provider, spec.ID); collapsed.ThinkingVariant {
			baseID = collapsed.LogicalID
		} else {
			baseID = StripThinkingVariantSuffix(spec.ID)
		}
		if baseID == "" || baseID == spec.ID {
			continue
		}
		base := byID[baseID]
		if base == nil {
			continue
		}
		if claimed != nil {
			_, specClaimed := claimed.resolve(spec.ID)
			_, baseClaimed := claimed.resolve(baseID)
			if specClaimed || baseClaimed || claimed.isFamily(spec.ID) || claimed.isFamily(baseID) {
				continue
			}
		}
		if spec.API != base.API {
			continue
		}
		specPriced := spec.Cost.Input != 0 || spec.Cost.Output != 0
		basePriced := base.Cost.Input != 0 || base.Cost.Output != 0
		if specPriced && basePriced && (spec.Cost.Input != base.Cost.Input || spec.Cost.Output != base.Cost.Output ||
			spec.Cost.CacheRead != base.Cost.CacheRead || spec.Cost.CacheWrite != base.Cost.CacheWrite) {
			continue
		}
		surface := derivePairThinkingSurface(spec, base, policy)
		routing := map[string]string{"off": base.ID}
		for _, effort := range surface.Efforts {
			routing[effort] = spec.ID
		}
		families = append(families, Family{ID: base.ID, Name: base.Name, Members: []string{base.ID, spec.ID}, Routing: routing, Thinking: surface})
	}
	return families
}

// CollapseVariants is collapseVariants without a table override, for one
// provider's specs: the reviewed table, then (Cursor) derived per-effort
// families, then derived thinking pairs.
func CollapseVariants(provider string, specs []*Spec, table *Table, policy Policy) []*Spec {
	result := specs
	if table != nil {
		result = CollapseWithTable(result, table)
	}
	if provider == "cursor" {
		if derived := DeriveCursorEffortFamilies(result); len(derived) > 0 {
			result = CollapseWithTable(result, &Table{Families: derived})
		}
	}
	if derived := DeriveThinkingPairFamilies(result, table, provider, policy); len(derived) > 0 {
		result = CollapseWithTable(result, &Table{Families: derived})
	}
	return result
}
