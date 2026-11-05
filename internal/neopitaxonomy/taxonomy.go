// Package neopitaxonomy is a port of NeoPi's model-identity taxonomy
// (catalog compat/taxonomy.ts): it classifies a wire model id into its
// (class, family, revision) ranks after NeoPi's reviewed identity overrides
// and variant-suffix collapse. The rules are NeoPi's compiled taxonomy,
// exported verbatim by testdata/export_taxonomy.ts.
//
// Only lenient classification is ported: ambiguities resolve to the
// "unknown" class or no family instead of failing, which is the mode NeoPi's
// discovery mappers use.
package neopitaxonomy

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed taxonomy.json
var taxonomyJSON []byte

// Revision is a parsed major.minor.patch revision.
type Revision [3]int

// String formats the revision the way NeoPi does ("5.2.0").
func (r Revision) String() string { return fmt.Sprintf("%d.%d.%d", r[0], r[1], r[2]) }

// Compare orders two revisions component by component.
func (r Revision) Compare(other Revision) int {
	for i := range r {
		if r[i] != other[i] {
			if r[i] < other[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Identity is NeoPi's ModelIdentity.
type Identity struct {
	Class  string
	Family string
	// Revision is valid when HasRevision.
	Revision        Revision
	HasRevision     bool
	Effort          string
	ThinkingVariant bool
	// LogicalID is set when the classified id differs from the wire id.
	LogicalID string
}

// AtLeast is NeoPi's revisionAtLeast(identity.revision, floor): false when
// the identity has no revision.
func (id Identity) AtLeast(floor string) bool {
	if !id.HasRevision {
		return false
	}
	parsed, ok := ParseRevisionPrefix(floor)
	if !ok {
		return false
	}
	return id.Revision.Compare(parsed) >= 0
}

type matcher struct {
	Kind    string `json:"kind"`
	Token   string `json:"token"`
	Bounded bool   `json:"bounded"`
}

type family struct {
	ID       string `json:"id"`
	Glob     string `json:"glob"`
	Priority int    `json:"priority"`
}

type revisionPrefix struct {
	Prefix   string `json:"prefix"`
	Anywhere bool   `json:"anywhere"`
}

type override struct {
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

type class struct {
	ID               string           `json:"id"`
	Matchers         []matcher        `json:"matchers"`
	Families         []family         `json:"families"`
	RevisionPrefixes []revisionPrefix `json:"revisionPrefixes"`
	SkipBare         []string         `json:"skipBare"`
	Overrides        []override       `json:"overrides"`
}

type collapseSuffix struct {
	Suffix           string  `json:"suffix"`
	Effort           *string `json:"effort"`
	Thinking         bool    `json:"thinking"`
	ExceptBarePrefix *string `json:"exceptBarePrefix"`
}

type effortLane struct {
	Suffix     string   `json:"suffix"`
	Providers  []string `json:"providers"`
	BarePrefix *string  `json:"barePrefix"`
}

type effortFamily struct {
	Provider string   `json:"provider"`
	Logical  string   `json:"logical"`
	Aliases  []string `json:"aliases"`
}

type rules struct {
	Classes  []class `json:"classes"`
	Collapse struct {
		Suffixes       []collapseSuffix `json:"suffixes"`
		Lanes          []effortLane     `json:"lanes"`
		EffortFamilies []effortFamily   `json:"effortFamilies"`
		PairTokens     []string         `json:"pairTokens"`
	} `json:"collapse"`

	// exact indexes model overrides by lowercase bare model: the first
	// provider-scoped override per provider and the first agnostic one.
	exact    map[string]*overrideBucket
	patterns []patternOverride
}

type overrideBucket struct {
	byProvider map[string]*override
	agnostic   *override
}

type patternOverride struct {
	override *override
	glob     string
	// provider is the lowercase provider; empty for agnostic patterns.
	provider    string
	scoped      bool
	specificity int
}

var (
	loadOnce sync.Once
	loaded   *rules
)

func taxonomy() *rules {
	loadOnce.Do(func() {
		r := &rules{exact: map[string]*overrideBucket{}}
		if err := json.Unmarshal(taxonomyJSON, r); err != nil {
			panic("neopitaxonomy: embedded taxonomy.json is invalid: " + err.Error())
		}
		for ci := range r.Classes {
			for oi := range r.Classes[ci].Overrides {
				o := &r.Classes[ci].Overrides[oi]
				if o.Model == nil {
					if o.Glob != nil {
						p := patternOverride{override: o, glob: *o.Glob, specificity: nonWildcardBytes(*o.Glob)}
						if o.Provider != nil {
							p.provider, p.scoped = jsLower(*o.Provider), true
						}
						r.patterns = append(r.patterns, p)
					}
					continue
				}
				key := jsLower(*o.Model)
				bucket := r.exact[key]
				if bucket == nil {
					bucket = &overrideBucket{byProvider: map[string]*override{}}
					r.exact[key] = bucket
				}
				if o.Provider != nil {
					providerKey := jsLower(*o.Provider)
					if _, ok := bucket.byProvider[providerKey]; !ok {
						bucket.byProvider[providerKey] = o
					}
				} else if bucket.agnostic == nil {
					bucket.agnostic = o
				}
			}
		}
		loaded = r
	})
	return loaded
}

var matcherRank = map[string]int{"exact": 4, "bounded": 3, "namespace": 2, "prefix": 1, "glob": 0}

func bareOf(id string) string {
	if slash := strings.LastIndex(id, "/"); slash >= 0 {
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
	switch next := value[len(token)]; {
	case next == '-', next == '_', next == '.', next == ':':
		return true
	default:
		return next >= '0' && next <= '9'
	}
}

func matcherMatches(m matcher, lower, bare string) bool {
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

func nonWildcardBytes(glob string) int {
	return len(glob) - strings.Count(glob, "*")
}

// GlobMatch is NeoPi's cascade globMatch: "*" matches any run of bytes and
// a pattern without "*" matches only itself.
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

type rank [2]int

func (r rank) less(other rank) bool {
	return r[0] < other[0] || r[0] == other[0] && r[1] < other[1]
}

// rankFamilies returns the winning family of subject and whether another
// family ties it.
func rankFamilies(cls *class, subject string) (winner string, tied bool) {
	var best rank
	found := false
	for _, f := range cls.Families {
		if !GlobMatch(f.Glob, subject) {
			continue
		}
		r := rank{f.Priority, nonWildcardBytes(f.Glob)}
		switch {
		case found && best == r && winner != f.ID:
			tied = true
		case !found || best.less(r):
			best, winner, found, tied = r, f.ID, true, false
		}
	}
	return winner, tied
}

func stripClassNamespace(cls *class, bare string) (string, bool) {
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

func classifyFamily(cls *class, bare string) string {
	winner, tied := rankFamilies(cls, bare)
	if !tied {
		return winner
	}
	// A Bedrock-style vendor.model id names the product after the class
	// namespace; only ambiguities are rescored.
	if scoped, ok := stripClassNamespace(cls, bare); ok {
		if rescored, rescoredTied := rankFamilies(cls, scoped); rescored != "" && !rescoredTied {
			return rescored
		}
	}
	return ""
}

func extractRevision(cls *class, bare string) (Revision, bool) {
	for _, skip := range cls.SkipBare {
		if skip == bare {
			return Revision{}, false
		}
	}
	for _, rule := range cls.RevisionPrefixes {
		var tail string
		matched := false
		if rule.Anywhere {
			if start := strings.Index(bare, rule.Prefix); start >= 0 {
				tail, matched = bare[start+len(rule.Prefix):], true
			}
		} else if strings.HasPrefix(bare, rule.Prefix) {
			tail, matched = bare[len(rule.Prefix):], true
		}
		if !matched {
			continue
		}
		digit := strings.IndexAny(tail, "0123456789")
		if digit < 0 {
			return Revision{}, false
		}
		return ParseRevisionPrefix(tail[digit:])
	}
	return Revision{}, false
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// ParseRevisionPrefix is NeoPi's parseRevisionPrefix: up to three numeric
// components (each at most 255) joined by "." or "-", stopping at the first
// component glued to a letter (a size token such as "32b").
func ParseRevisionPrefix(value string) (Revision, bool) {
	var out Revision
	count, index := 0, 0
	for count < 3 {
		start := index
		for index < len(value) && isDigit(value[index]) {
			index++
		}
		sizeToken := index < len(value) && isLetter(value[index])
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
		if separator != '.' && separator != '-' || index+1 >= len(value) || !isDigit(value[index+1]) {
			break
		}
		index++
	}
	return out, true
}

func parseComponent(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	out := 0
	for i := 0; i < len(value); i++ {
		if !isDigit(value[i]) {
			return 0, false
		}
		out = out*10 + int(value[i]-'0')
		if out > 255 {
			return 0, false
		}
	}
	return out, true
}

type ranks struct {
	family      string
	revision    Revision
	hasRevision bool
}

func classifyRanks(r *rules, model string) (string, ranks) {
	lower := jsLower(jsTrim(model))
	bare := bareOf(lower)
	var winner *class
	var best rank
	tied := false
	for ci := range r.Classes {
		cls := &r.Classes[ci]
		for _, m := range cls.Matchers {
			if !matcherMatches(m, lower, bare) {
				continue
			}
			rk := rank{matcherRank[m.Kind], len(m.Token)}
			switch {
			case winner != nil && best == rk && winner.ID != cls.ID:
				tied = true
			case winner == nil || best.less(rk):
				winner, best, tied = cls, rk, false
			}
		}
	}
	if tied || winner == nil {
		return "unknown", ranks{}
	}
	return winner.ID, ranksOf(winner, bare)
}

func ranksOf(cls *class, bare string) ranks {
	out := ranks{family: classifyFamily(cls, bare)}
	out.revision, out.hasRevision = extractRevision(cls, bare)
	return out
}

func ranksInClass(r *rules, classID, model string) ranks {
	for ci := range r.Classes {
		if r.Classes[ci].ID == classID {
			return ranksOf(&r.Classes[ci], bareOf(jsLower(jsTrim(model))))
		}
	}
	return ranks{}
}

// findIdentityOverride: overrides never expire here because NeoPi's
// discovery classifies without an observation time.
func findIdentityOverride(r *rules, provider, bareModel string) *override {
	lowerProvider := jsLower(provider)
	lowerBare := jsLower(bareModel)
	if bucket := r.exact[lowerBare]; bucket != nil {
		if scoped := bucket.byProvider[lowerProvider]; scoped != nil {
			return scoped
		}
		if bucket.agnostic != nil {
			return bucket.agnostic
		}
	}
	winner, tied := rankPatternOverrides(r.patterns, lowerProvider, true, lowerBare)
	if winner == nil && !tied {
		winner, tied = rankPatternOverrides(r.patterns, "", false, lowerBare)
	}
	if tied {
		return nil
	}
	return winner
}

func rankPatternOverrides(patterns []patternOverride, provider string, scoped bool, bare string) (*override, bool) {
	var winner *override
	specificity, tied := 0, false
	for _, p := range patterns {
		if p.scoped != scoped || p.provider != provider || !GlobMatch(p.glob, bare) {
			continue
		}
		switch {
		case winner != nil && specificity == p.specificity:
			tied = true
		case winner == nil || specificity < p.specificity:
			winner, specificity, tied = p.override, p.specificity, false
		}
	}
	return winner, tied
}

type collapsed struct {
	logicalID       string
	effort          string
	thinkingVariant bool
}

// CollapseVariant is NeoPi's collapseVariantId(provider, model): the logical
// id after a declared thinking or effort suffix is collapsed, and whether
// the id was a thinking variant.
func CollapseVariant(provider, model string) (logicalID string, thinkingVariant bool) {
	c := collapseVariantID(taxonomy(), provider, model)
	return c.logicalID, c.thinkingVariant
}

func isTokenByte(c byte) bool { return isDigit(c) || c >= 'a' && c <= 'z' }

// StripThinkingVariantSuffix is NeoPi's stripThinkingVariantSuffix: the id
// without its first "-<pair token>" (thinking, reasoning, reasoner) that
// ends a word and is not negated ("non-thinking", "no-thinking"); ok is
// false when there is none or nothing would remain.
func StripThinkingVariantSuffix(model string) (string, bool) {
	lower := jsLower(model)
	for _, token := range taxonomy().Collapse.PairTokens {
		needle := "-" + token
		for searchFrom := 0; searchFrom < len(lower); {
			found := strings.Index(lower[searchFrom:], needle)
			if found < 0 {
				break
			}
			index := searchFrom + found
			end := index + len(needle)
			followed := end < len(lower) && isTokenByte(lower[end])
			wordStart := index
			for wordStart > 0 && isTokenByte(lower[wordStart-1]) {
				wordStart--
			}
			if preceding := lower[wordStart:index]; !followed && preceding != "non" && preceding != "no" {
				stripped := model[:index] + model[end:]
				return stripped, stripped != ""
			}
			searchFrom = index + 1
		}
	}
	return "", false
}

func collapseVariantID(r *rules, provider, model string) collapsed {
	lower := jsLower(model)
	lowerProvider := jsLower(provider)
	for _, f := range r.Collapse.EffortFamilies {
		if f.Provider != lowerProvider {
			continue
		}
		for _, alias := range f.Aliases {
			if alias == lower {
				return collapsed{logicalID: f.Logical}
			}
		}
	}
	bare := bareOf(lower)
	var winner *collapseSuffix
	for i := range r.Collapse.Suffixes {
		rule := &r.Collapse.Suffixes[i]
		if !strings.HasSuffix(lower, rule.Suffix) {
			continue
		}
		if rule.ExceptBarePrefix != nil && strings.HasPrefix(bare, *rule.ExceptBarePrefix) {
			continue
		}
		if winner == nil || len(rule.Suffix) > len(winner.Suffix) {
			winner = rule
		}
	}
	if winner != nil {
		out := collapsed{logicalID: model[:len(model)-len(winner.Suffix)], thinkingVariant: winner.Thinking}
		if winner.Effort != nil {
			out.effort = *winner.Effort
		}
		return out
	}
	for _, lane := range r.Collapse.Lanes {
		served := false
		for _, candidate := range lane.Providers {
			served = served || candidate == lowerProvider
		}
		if !served || !strings.HasSuffix(lower, lane.Suffix) {
			continue
		}
		trimmed := lower[:len(lower)-len(lane.Suffix)]
		trimmedBare := bareOf(trimmed)
		if lane.BarePrefix != nil && !strings.HasPrefix(trimmedBare, *lane.BarePrefix) {
			continue
		}
		var effortRule *collapseSuffix
		for i := range r.Collapse.Suffixes {
			rule := &r.Collapse.Suffixes[i]
			if rule.Effort == nil || !strings.HasSuffix(trimmed, rule.Suffix) {
				continue
			}
			if rule.ExceptBarePrefix != nil && strings.HasPrefix(trimmedBare, *rule.ExceptBarePrefix) {
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
		return collapsed{logicalID: base + model[len(trimmed):], effort: *effortRule.Effort}
	}
	return collapsed{logicalID: model}
}

// Classify is NeoPi's classifyModel(provider, modelID, { lenient: true }):
// a reviewed override first, then suffix collapse, then the class, family
// and revision ranks of the logical id.
func Classify(provider, modelID string) Identity {
	r := taxonomy()
	trimmed := jsTrim(modelID)
	if o := findIdentityOverride(r, provider, bareOf(trimmed)); o != nil {
		logical := trimmed
		if o.Logical != nil {
			logical = *o.Logical
		}
		var classID string
		var inferred ranks
		if o.Class != nil {
			classID = *o.Class
		} else {
			classID, _ = classifyRanks(r, logical)
		}
		inferred = ranksInClass(r, classID, logical)
		identity := Identity{Class: classID, Family: inferred.family, Revision: inferred.revision, HasRevision: inferred.hasRevision}
		if o.Family != nil {
			identity.Family = *o.Family
		}
		if o.Revision != nil {
			if revision, ok := ParseRevisionPrefix(*o.Revision); ok {
				identity.Revision, identity.HasRevision = revision, true
			}
		}
		if o.Effort != nil {
			identity.Effort = *o.Effort
		}
		identity.ThinkingVariant = o.ThinkingVariant
		if logical != trimmed {
			identity.LogicalID = logical
		}
		return identity
	}
	c := collapsed{logicalID: trimmed}
	if len(trimmed) == len(modelID) {
		c = collapseVariantID(r, provider, trimmed)
	}
	classID, rk := classifyRanks(r, c.logicalID)
	identity := Identity{Class: classID, Family: rk.family, Revision: rk.revision, HasRevision: rk.hasRevision,
		Effort: c.effort, ThinkingVariant: c.thinkingVariant}
	if c.logicalID != trimmed {
		identity.LogicalID = c.logicalID
	}
	return identity
}

// isJSWhitespace reports whether r is trimmed by ECMAScript
// String.prototype.trim (WhiteSpace plus LineTerminator): U+FEFF is
// whitespace in JavaScript while U+0085 is not.
func isJSWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// jsTrim mirrors String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSWhitespace) }

// jsLower mirrors String.prototype.toLowerCase where Go differs: Go maps
// U+0130 to a plain "i" while JavaScript yields "i" + U+0307.
func jsLower(s string) string { return strings.ToLower(strings.ReplaceAll(s, "\u0130", "i\u0307")) }
