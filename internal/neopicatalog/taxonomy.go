// Package neopicatalog ports the provider-independent parts of NeoPi's model
// catalog (pi-catalog): the identity taxonomy (compat/taxonomy.ts), revision
// parsing (compat/revision.ts) and effort-variant collapsing
// (compat/collapse.ts). Rule data comes from NeoPi's compiled rules.json,
// exported as taxonomy.json by
// internal/registry/testdata/capture_cursor_npi_catalog.ts.
package neopicatalog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

//go:embed taxonomy.json
var taxonomyJSON []byte

type matcher struct {
	Kind    string `json:"kind"`
	Token   string `json:"token"`
	Bounded bool   `json:"bounded"`
}

type classFamily struct {
	ID       string `json:"id"`
	Glob     string `json:"glob"`
	Priority int    `json:"priority"`
}

type revisionPrefix struct {
	Prefix   string `json:"prefix"`
	Anywhere bool   `json:"anywhere"`
}

type identityOverride struct {
	ID              string  `json:"id"`
	Model           *string `json:"model"`
	Glob            *string `json:"glob"`
	Provider        *string `json:"provider"`
	Logical         *string `json:"logical"`
	Class           *string `json:"class"`
	Family          *string `json:"family"`
	Revision        *string `json:"revision"`
	Effort          *string `json:"effort"`
	ThinkingVariant bool    `json:"thinkingVariant"`
	ExpiresAtMs     *int64  `json:"expiresAtMs"`
}

type identityClass struct {
	ID               string             `json:"id"`
	Matchers         []matcher          `json:"matchers"`
	Families         []classFamily      `json:"families"`
	RevisionPrefixes []revisionPrefix   `json:"revisionPrefixes"`
	SkipBare         []string           `json:"skipBare"`
	Overrides        []identityOverride `json:"overrides"`
}

type suffixRule struct {
	Suffix           string  `json:"suffix"`
	Thinking         bool    `json:"thinking"`
	Effort           *string `json:"effort"`
	ExceptBarePrefix *string `json:"exceptBarePrefix"`
}

type laneRule struct {
	Suffix     string   `json:"suffix"`
	Providers  []string `json:"providers"`
	BarePrefix *string  `json:"barePrefix"`
}

type effortFamilyRule struct {
	Provider string   `json:"provider"`
	Logical  string   `json:"logical"`
	Aliases  []string `json:"aliases"`
}

// CompiledFamily is one reviewed variant-family as rules.json compiles it.
type CompiledFamily struct {
	Provider       string            `json:"provider"`
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Members        []string          `json:"members"`
	Routing        map[string]string `json:"routing"`
	Revision       string            `json:"revision,omitempty"`
	Mode           string            `json:"mode,omitempty"`
	Efforts        []string          `json:"efforts,omitempty"`
	RequiresEffort bool              `json:"requiresEffort,omitempty"`
	DefaultLevel   string            `json:"defaultLevel,omitempty"`
	DefaultMember  string            `json:"defaultMember,omitempty"`
	ExtraAliases   []string          `json:"extraAliases,omitempty"`
	NoThinking     bool              `json:"noThinking,omitempty"`
}

type taxonomyData struct {
	Classes  []identityClass `json:"classes"`
	Collapse struct {
		Suffixes        []suffixRule       `json:"suffixes"`
		Lanes           []laneRule         `json:"lanes"`
		PairTokens      []string           `json:"pairTokens"`
		EffortFamilies  []effortFamilyRule `json:"effortFamilies"`
		VariantFamilies []CompiledFamily   `json:"variantFamilies"`
	} `json:"collapse"`
	CursorParameters []struct {
		Model string `json:"model"`
		ModelParameter
	} `json:"cursorParameters"`
}

var taxonomy = sync.OnceValue(func() *taxonomyData {
	out := &taxonomyData{}
	if err := json.Unmarshal(taxonomyJSON, out); err != nil {
		panic(fmt.Sprintf("neopicatalog: taxonomy.json: %v", err))
	}
	return out
})

// CursorModelParameters is cursorModelParameters (compat/behavior.ts): the
// fixed requestedModel parameters declared for an exact Cursor wire model.
func CursorModelParameters(model string) []ModelParameter {
	var out []ModelParameter
	for _, parameter := range taxonomy().CursorParameters {
		if parameter.Model == model {
			out = append(out, parameter.ModelParameter)
		}
	}
	return out
}

// Identity is ModelIdentity: class, family, revision and the collapsed variant.
type Identity struct {
	Class           string `json:"class"`
	Family          string `json:"family,omitempty"`
	Revision        string `json:"revision,omitempty"`
	Effort          string `json:"effort,omitempty"`
	ThinkingVariant bool   `json:"thinkingVariant,omitempty"`
	LogicalID       string `json:"logicalId,omitempty"`
}

var matcherRank = map[string]int{"exact": 4, "bounded": 3, "namespace": 2, "prefix": 1, "glob": 0}

func bareOf(id string) string {
	if slash := strings.LastIndexByte(id, '/'); slash >= 0 {
		return id[slash+1:]
	}
	return id
}

func boundedMatch(value, token string) bool {
	if value == token {
		return true
	}
	if !strings.HasPrefix(value, token) || len(value) == len(token) {
		return false
	}
	next := value[len(token)]
	return next == '-' || next == '_' || next == '.' || next == ':' || (next >= '0' && next <= '9')
}

func (m matcher) matches(lower, bare string) bool {
	switch m.Kind {
	case "exact":
		return bare == m.Token
	case "bounded":
		return boundedMatch(bare, m.Token)
	case "namespace":
		var parts []string
		if m.Bounded {
			parts = strings.FieldsFunc(lower, func(r rune) bool { return r == '/' || r == '.' || r == ':' })
		} else {
			parts = strings.Split(lower, "/")
		}
		for _, part := range parts {
			if part == "" {
				continue
			}
			if m.Bounded && boundedMatch(part, m.Token) || !m.Bounded && part == m.Token {
				return true
			}
		}
		return false
	case "prefix":
		return strings.HasPrefix(bare, m.Token)
	case "glob":
		return GlobMatch(m.Token, bare)
	}
	return false
}

// GlobMatch is cascade.ts globMatch: an anchored `*` wildcard match.
func GlobMatch(pattern, value string) bool {
	segments := strings.Split(pattern, "*")
	if len(segments) == 1 {
		return value == pattern
	}
	if !strings.HasPrefix(value, segments[0]) {
		return false
	}
	remainder := value[len(segments[0]):]
	for _, segment := range segments[1 : len(segments)-1] {
		if segment == "" {
			continue
		}
		found := strings.Index(remainder, segment)
		if found < 0 {
			return false
		}
		remainder = remainder[found+len(segment):]
	}
	last := segments[len(segments)-1]
	return last == "" || strings.HasSuffix(remainder, last)
}

func nonWildcardBytes(glob string) int { return len(glob) - strings.Count(glob, "*") }

type rankPair [2]int

func (a rankPair) less(b rankPair) bool { return a[0] < b[0] || a[0] == b[0] && a[1] < b[1] }

func rankFamilies(cls *identityClass, subject string) (winner string, tied bool) {
	var best rankPair
	found := false
	for _, family := range cls.Families {
		if !GlobMatch(family.Glob, subject) {
			continue
		}
		rank := rankPair{family.Priority, nonWildcardBytes(family.Glob)}
		switch {
		case found && rank == best && winner != family.ID:
			tied = true
		case !found || best.less(rank):
			best, winner, found, tied = rank, family.ID, true, false
		}
	}
	return winner, tied
}

func stripClassNamespace(cls *identityClass, bare string) (string, bool) {
	separator := strings.IndexAny(bare, ".:")
	if separator <= 0 || separator == len(bare)-1 {
		return "", false
	}
	namespace := bare[:separator]
	for _, m := range cls.Matchers {
		if m.Token == namespace {
			return bare[separator+1:], true
		}
	}
	return "", false
}

// classifyFamily is lenient: ambiguity resolves to no family.
func classifyFamily(cls *identityClass, bare string) string {
	winner, tied := rankFamilies(cls, bare)
	if !tied {
		return winner
	}
	if scoped, ok := stripClassNamespace(cls, bare); ok {
		if rescored, rescoredTied := rankFamilies(cls, scoped); rescored != "" && !rescoredTied {
			return rescored
		}
	}
	return ""
}

func extractRevision(cls *identityClass, bare string) string {
	for _, skip := range cls.SkipBare {
		if skip == bare {
			return ""
		}
	}
	for _, rule := range cls.RevisionPrefixes {
		tail, ok := "", false
		if rule.Anywhere {
			if start := strings.Index(bare, rule.Prefix); start >= 0 {
				tail, ok = bare[start+len(rule.Prefix):], true
			}
		} else if strings.HasPrefix(bare, rule.Prefix) {
			tail, ok = bare[len(rule.Prefix):], true
		}
		if !ok {
			continue
		}
		digit := strings.IndexAny(tail, "0123456789")
		if digit < 0 {
			return ""
		}
		if revision, okRevision := ParseRevisionPrefix(tail[digit:]); okRevision {
			return revision.String()
		}
		return ""
	}
	return ""
}

func classifyRanks(model string) Identity {
	lower := strings.ToLower(strings.TrimSpace(model))
	bare := bareOf(lower)
	var winner *identityClass
	var best rankPair
	tied := false
	classes := taxonomy().Classes
	for i := range classes {
		cls := &classes[i]
		for _, m := range cls.Matchers {
			if !m.matches(lower, bare) {
				continue
			}
			rank := rankPair{matcherRank[m.Kind], len(m.Token)}
			switch {
			case winner != nil && rank == best && winner.ID != cls.ID:
				tied = true
			case winner == nil || best.less(rank):
				winner, best, tied = cls, rank, false
			}
		}
	}
	if tied || winner == nil {
		return Identity{Class: "unknown"}
	}
	return Identity{Class: winner.ID, Family: classifyFamily(winner, bare), Revision: extractRevision(winner, bare)}
}

func ranksInClass(classID, model string) Identity {
	classes := taxonomy().Classes
	for i := range classes {
		if classes[i].ID != classID {
			continue
		}
		bare := bareOf(strings.ToLower(strings.TrimSpace(model)))
		return Identity{Family: classifyFamily(&classes[i], bare), Revision: extractRevision(&classes[i], bare)}
	}
	return Identity{}
}

// findOverride is findIdentityOverride without expiry (no observation time)
// and lenient on pattern ties.
func findOverride(provider, bareModel string) *identityOverride {
	lowerProvider, lowerBare := strings.ToLower(provider), strings.ToLower(bareModel)
	var agnostic *identityOverride
	classes := taxonomy().Classes
	for i := range classes {
		for j := range classes[i].Overrides {
			o := &classes[i].Overrides[j]
			if o.Model == nil || strings.ToLower(*o.Model) != lowerBare {
				continue
			}
			if o.Provider != nil {
				if strings.ToLower(*o.Provider) == lowerProvider {
					return o
				}
			} else if agnostic == nil {
				agnostic = o
			}
		}
	}
	if agnostic != nil {
		return agnostic
	}
	if winner, tied, any := rankPatternOverrides(lowerProvider, lowerBare); any {
		if tied {
			return nil
		}
		return winner
	}
	winner, tied, _ := rankPatternOverrides("", lowerBare)
	if tied {
		return nil
	}
	return winner
}

func rankPatternOverrides(provider, bareModel string) (winner *identityOverride, tied, any bool) {
	best := -1
	classes := taxonomy().Classes
	for i := range classes {
		for j := range classes[i].Overrides {
			o := &classes[i].Overrides[j]
			if o.Model != nil || o.Glob == nil {
				continue
			}
			scope := ""
			if o.Provider != nil {
				scope = strings.ToLower(*o.Provider)
			}
			if scope != provider || !GlobMatch(*o.Glob, bareModel) {
				continue
			}
			any = true
			specificity := nonWildcardBytes(*o.Glob)
			switch {
			case specificity == best:
				tied = true
			case specificity > best:
				winner, best, tied = o, specificity, false
			}
		}
	}
	return winner, tied, any
}

// Classify is classifyModel(provider, id, { lenient: true }): a reviewed
// override first, then suffix collapse, then class/family/revision ranks.
func Classify(provider, modelID string) Identity {
	trimmed := strings.TrimSpace(modelID)
	if override := findOverride(provider, bareOf(trimmed)); override != nil {
		logical := trimmed
		if override.Logical != nil {
			logical = *override.Logical
		}
		identity := Identity{}
		if override.Class != nil {
			identity.Class = *override.Class
		} else {
			identity.Class = classifyRanks(logical).Class
		}
		inferred := ranksInClass(identity.Class, logical)
		identity.Family, identity.Revision = inferred.Family, inferred.Revision
		if override.Family != nil {
			identity.Family = *override.Family
		}
		if override.Revision != nil {
			identity.Revision = *override.Revision
		}
		if override.Effort != nil {
			identity.Effort = *override.Effort
		}
		identity.ThinkingVariant = override.ThinkingVariant
		if logical != trimmed {
			identity.LogicalID = logical
		}
		return identity
	}
	collapsed := CollapsedVariant{LogicalID: trimmed}
	if len(trimmed) == len(modelID) {
		collapsed = CollapseVariantID(provider, trimmed)
	}
	identity := classifyRanks(collapsed.LogicalID)
	identity.Effort, identity.ThinkingVariant = collapsed.Effort, collapsed.ThinkingVariant
	if collapsed.LogicalID != trimmed {
		identity.LogicalID = collapsed.LogicalID
	}
	return identity
}

// CollapsedVariant is the result of collapsing a wire id's suffix.
type CollapsedVariant struct {
	LogicalID       string
	Effort          string // "" when the id was no effort variant
	ThinkingVariant bool
}

// CollapseVariantID is collapseVariantId.
func CollapseVariantID(provider, model string) CollapsedVariant {
	collapse := &taxonomy().Collapse
	lowerProvider := strings.ToLower(provider)
	lower := strings.ToLower(model)
	for _, family := range collapse.EffortFamilies {
		if family.Provider != lowerProvider {
			continue
		}
		for _, alias := range family.Aliases {
			if alias == lower {
				return CollapsedVariant{LogicalID: family.Logical}
			}
		}
	}
	bare := bareOf(lower)
	var winner *suffixRule
	for i := range collapse.Suffixes {
		rule := &collapse.Suffixes[i]
		if !strings.HasSuffix(lower, rule.Suffix) || rule.ExceptBarePrefix != nil && strings.HasPrefix(bare, *rule.ExceptBarePrefix) {
			continue
		}
		if winner == nil || len(rule.Suffix) > len(winner.Suffix) {
			winner = rule
		}
	}
	if winner != nil {
		out := CollapsedVariant{LogicalID: model[:len(model)-len(winner.Suffix)], ThinkingVariant: winner.Thinking}
		if winner.Effort != nil {
			out.Effort = *winner.Effort
		}
		return out
	}
	for _, lane := range collapse.Lanes {
		if !containsString(lane.Providers, lowerProvider) || !strings.HasSuffix(lower, lane.Suffix) {
			continue
		}
		trimmed := lower[:len(lower)-len(lane.Suffix)]
		trimmedBare := bareOf(trimmed)
		if lane.BarePrefix != nil && !strings.HasPrefix(trimmedBare, *lane.BarePrefix) {
			continue
		}
		var effortRule *suffixRule
		for i := range collapse.Suffixes {
			rule := &collapse.Suffixes[i]
			if rule.Effort == nil || !strings.HasSuffix(trimmed, rule.Suffix) || rule.ExceptBarePrefix != nil && strings.HasPrefix(trimmedBare, *rule.ExceptBarePrefix) {
				continue
			}
			if effortRule == nil || len(rule.Suffix) > len(effortRule.Suffix) {
				effortRule = rule
			}
		}
		if effortRule == nil {
			continue
		}
		base := model[:len(trimmed)-len(effortRule.Suffix)]
		if base == "" || strings.HasSuffix(base, "/") {
			continue
		}
		return CollapsedVariant{LogicalID: base + model[len(trimmed):], Effort: *effortRule.Effort}
	}
	return CollapsedVariant{LogicalID: model}
}

func isTokenChar(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' }

// StripThinkingVariantSuffix is stripThinkingVariantSuffix: the id without
// its first "-<pair token>" that is neither followed by a token character nor
// negated by "no"/"non"; "" when there is none.
func StripThinkingVariantSuffix(model string) string {
	lower := strings.ToLower(model)
	for _, token := range taxonomy().Collapse.PairTokens {
		needle := "-" + token
		for from := 0; from < len(lower); {
			index := strings.Index(lower[from:], needle)
			if index < 0 {
				break
			}
			index += from
			end := index + len(needle)
			followed := end < len(lower) && isTokenChar(lower[end])
			start := index
			for start > 0 && isTokenChar(lower[start-1]) {
				start--
			}
			preceding := lower[start:index]
			if !followed && preceding != "non" && preceding != "no" {
				return model[:index] + model[end:]
			}
			from = index + 1
		}
	}
	return ""
}

// VariantFamilies returns the reviewed variant families compiled for provider.
func VariantFamilies(provider string) []CompiledFamily {
	var out []CompiledFamily
	for _, family := range taxonomy().Collapse.VariantFamilies {
		if family.Provider == strings.ToLower(provider) {
			out = append(out, family)
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// Revision is a parsed major.minor.patch triple.
type Revision [3]int

func (r Revision) String() string {
	return strconv.Itoa(r[0]) + "." + strconv.Itoa(r[1]) + "." + strconv.Itoa(r[2])
}

// Compare orders revisions lexicographically.
func (r Revision) Compare(o Revision) int {
	for i := range r {
		if r[i] != o[i] {
			return r[i] - o[i]
		}
	}
	return 0
}

func parseComponent(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	out := 0
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
		if out = out*10 + int(value[i]-'0'); out > 255 {
			return 0, false
		}
	}
	return out, true
}

// ParseRevision is parseRevision: one to three components split on . or -.
func ParseRevision(value string) (Revision, bool) {
	var out Revision
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '.' || r == '-' })
	if strings.Count(value, ".")+strings.Count(value, "-")+1 != len(parts) || len(parts) > 3 || len(parts) == 0 {
		return out, false
	}
	for i, part := range parts {
		component, ok := parseComponent(part)
		if !ok {
			return out, false
		}
		out[i] = component
	}
	return out, true
}

// ParseRevisionPrefix is parseRevisionPrefix: the leading revision of a tail
// that starts with a digit.
func ParseRevisionPrefix(value string) (Revision, bool) {
	var out Revision
	count, index := 0, 0
	for count < 3 {
		start := index
		for index < len(value) && value[index] >= '0' && value[index] <= '9' {
			index++
		}
		sizeToken := index < len(value) && (value[index] >= 'a' && value[index] <= 'z' || value[index] >= 'A' && value[index] <= 'Z')
		component, ok := parseComponent(value[start:index])
		if sizeToken || !ok {
			return out, count > 0
		}
		out[count] = component
		count++
		if index >= len(value) {
			break
		}
		separator := value[index]
		if separator != '.' && separator != '-' || index+1 >= len(value) || value[index+1] < '0' || value[index+1] > '9' {
			break
		}
		index++
	}
	return out, true
}

// RevisionTerm is one comparison of a revision constraint.
type RevisionTerm struct {
	Op       string
	Revision Revision
}

// ParseRevisionConstraint is parseRevisionConstraint (">=4.7 <5").
func ParseRevisionConstraint(expression string) ([]RevisionTerm, bool) {
	var terms []RevisionTerm
	for _, raw := range strings.Fields(expression) {
		var op, operand string
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(raw, candidate) {
				op, operand = candidate, raw[len(candidate):]
				break
			}
		}
		if op == "" || strings.Contains(operand, "-") {
			return nil, false
		}
		revision, ok := ParseRevision(operand)
		if !ok {
			return nil, false
		}
		terms = append(terms, RevisionTerm{Op: op, Revision: revision})
	}
	return terms, len(terms) > 0
}

// Satisfies reports whether r meets every term.
func (r Revision) Satisfies(terms []RevisionTerm) bool {
	for _, term := range terms {
		cmp := r.Compare(term.Revision)
		switch term.Op {
		case ">=":
			if cmp < 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		case "<":
			if cmp >= 0 {
				return false
			}
		case "=":
			if cmp != 0 {
				return false
			}
		}
	}
	return true
}
