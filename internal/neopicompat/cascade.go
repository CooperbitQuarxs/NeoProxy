// Package neopicompat is a port of NeoPi's catalog compat engine (pi-catalog
// compat/cascade.ts and compat/resolve.ts): the model policy NeoPi resolves
// for a model spec from its compiled rules, its known-host vocabulary and the
// model's identity. NeoPi's bundled catalog carries this policy precomputed;
// the engine is what derives it for an id the bundle lacks.
//
// The rules, host vocabulary and axis keys are NeoPi's own, exported verbatim
// by testdata/export_compat_rules.ts.
package neopicompat

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
)

//go:embed compat_rules.json.gz
var compatRulesGz []byte

type selector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type revisionTerm struct {
	Op       string `json:"op"`
	Revision string `json:"revision"`
}

// rule is one compiled cascade rule: a conjunction of selectors and the axis
// values it assigns.
type rule struct {
	Source    string           `json:"source"`
	Class     *string          `json:"class"`
	Providers []string         `json:"providers"`
	APIs      []string         `json:"apis"`
	Upstreams []string         `json:"upstreams"`
	Family    *string          `json:"family"`
	Revision  []revisionTerm   `json:"revision"`
	Models    []selector       `json:"models"`
	Priority  int              `json:"priority"`
	Wire      map[string]any   `json:"wire"`
	Thinking  map[string]any   `json:"thinking"`
	Catalog   map[string]any   `json:"catalog"`
	revision  []parsedRevision // parsed Revision terms
	dimension int
	order     int
	exactEff  bool
}

type parsedRevision struct {
	op       string
	revision neopitaxonomy.Revision
}

type hostSpec struct {
	Providers        []string `json:"providers"`
	ProviderPrefixes []string `json:"providerPrefixes"`
	URLMarkers       []string `json:"urlMarkers"`
}

type compiledData struct {
	Cascade             []*rule             `json:"cascade"`
	Hosts               map[string]hostSpec `json:"hosts"`
	CompletionsWireKeys []string            `json:"completionsWireKeys"`
}

type ruleIndex struct {
	globals         []*rule
	byClass         map[string][]*rule
	byProvider      map[string][]*rule
	byClassProvider map[string]map[string][]*rule
}

var (
	dataOnce  sync.Once
	data      *compiledData
	index     *ruleIndex
	wireKeys  map[string]bool
	dataError error
)

func load() error {
	dataOnce.Do(func() {
		reader, err := gzip.NewReader(bytes.NewReader(compatRulesGz))
		if err != nil {
			dataError = err
			return
		}
		defer func() { _ = reader.Close() }()
		raw, err := io.ReadAll(reader)
		if err != nil {
			dataError = err
			return
		}
		data = &compiledData{}
		if err = json.Unmarshal(raw, data); err != nil {
			dataError = err
			return
		}
		wireKeys = make(map[string]bool, len(data.CompletionsWireKeys))
		for _, key := range data.CompletionsWireKeys {
			wireKeys[key] = true
		}
		index, dataError = buildIndex(data.Cascade)
	})
	return dataError
}

func buildIndex(rules []*rule) (*ruleIndex, error) {
	idx := &ruleIndex{byClass: map[string][]*rule{}, byProvider: map[string][]*rule{}, byClassProvider: map[string]map[string][]*rule{}}
	for order, r := range rules {
		r.order = order
		for _, term := range r.Revision {
			parsed, ok := parseRevision(term.Revision)
			if !ok {
				return nil, fmt.Errorf("neopicompat: invalid compiled revision term in %s", r.Source)
			}
			r.revision = append(r.revision, parsedRevision{op: term.Op, revision: parsed})
		}
		count := func(present bool) int {
			if present {
				return 1
			}
			return 0
		}
		r.dimension = count(r.Class != nil) + count(r.Providers != nil) + count(r.APIs != nil) + count(r.Upstreams != nil) +
			count(r.Family != nil) + count(r.Revision != nil) + count(r.Models != nil)
		_, r.exactEff = r.Thinking["efforts"]
		switch {
		case r.Class == nil && r.Providers == nil:
			idx.globals = append(idx.globals, r)
		case r.Class == nil:
			for _, provider := range unique(r.Providers) {
				idx.byProvider[provider] = append(idx.byProvider[provider], r)
			}
		case r.Providers == nil:
			idx.byClass[*r.Class] = append(idx.byClass[*r.Class], r)
		default:
			providers := idx.byClassProvider[*r.Class]
			if providers == nil {
				providers = map[string][]*rule{}
				idx.byClassProvider[*r.Class] = providers
			}
			for _, provider := range unique(r.Providers) {
				providers[provider] = append(providers[provider], r)
			}
		}
	}
	return idx, nil
}

func unique(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

// parseRevision is revision.ts parseRevision: one to three components of at
// most 255, separated by '.' or '-'.
func parseRevision(value string) (neopitaxonomy.Revision, bool) {
	var out neopitaxonomy.Revision
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '.' || r == '-' })
	if len(parts) != strings.Count(value, ".")+strings.Count(value, "-")+1 || len(parts) == 0 || len(parts) > 3 {
		return out, false
	}
	for i, part := range parts {
		n := 0
		for _, c := range part {
			if c < '0' || c > '9' {
				return out, false
			}
			n = n*10 + int(c-'0')
			if n > 255 {
				return out, false
			}
		}
		out[i] = n
	}
	return out, true
}

func revisionSatisfies(revision neopitaxonomy.Revision, terms []parsedRevision) bool {
	for _, term := range terms {
		cmp := revision.Compare(term.revision)
		switch term.op {
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

// target is ResolveTarget: the structured identity rules select on.
type target struct {
	provider    string
	api         string
	upstream    string
	class       string
	family      string
	revision    neopitaxonomy.Revision
	hasRevision bool
	model       string
	reasoning   bool
}

type preparedTarget struct {
	target
	modelLower  string
	modelTokens []string
}

var tokenSeparator = regexp.MustCompile(`[^a-z0-9]+`)

func prepare(t target) preparedTarget {
	lower := strings.ToLower(t.model)
	return preparedTarget{target: t, modelLower: lower, modelTokens: tokenSeparator.Split(lower, -1)}
}

// globMatch is cascade.ts globMatch: an anchored '*' wildcard match over
// pre-lowercased text.
func globMatch(pattern, value string) bool {
	segments := strings.Split(pattern, "*")
	if len(segments) == 1 {
		return value == pattern
	}
	head := segments[0]
	if !strings.HasPrefix(value, head) {
		return false
	}
	remainder := value[len(head):]
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

type rank [3]int

func (a rank) compare(b rank) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

type rankedRule struct {
	rule *rule
	rank rank
}

func rankRule(r *rule, t preparedTarget) (rank, bool) {
	if r.APIs != nil && !slices.Contains(r.APIs, t.api) {
		return rank{}, false
	}
	if r.Upstreams != nil && (t.upstream == "" || !slices.Contains(r.Upstreams, t.upstream)) {
		return rank{}, false
	}
	if r.Family != nil && (t.family == "" || *r.Family != t.family) {
		return rank{}, false
	}
	if r.revision != nil && (!t.hasRevision || !revisionSatisfies(t.revision, r.revision)) {
		return rank{}, false
	}
	exactness := 0
	if r.Models != nil {
		best := -1
		for _, sel := range r.Models {
			matched := false
			switch sel.Kind {
			case "exact":
				matched = sel.Value == t.model
			case "glob":
				matched = globMatch(sel.Value, t.modelLower)
			case "token":
				matched = slices.Contains(t.modelTokens, sel.Value)
			}
			if !matched {
				continue
			}
			value := 1
			if sel.Kind == "exact" {
				value = 2
			}
			best = max(best, value)
		}
		if best < 0 {
			return rank{}, false
		}
		exactness = best
	}
	return rank{exactness, r.dimension, r.Priority}, true
}

// relevant merges the four candidate buckets in compiled-rule order.
func relevant(t preparedTarget) []rankedRule {
	buckets := [][]*rule{index.globals, index.byClass[t.class], index.byProvider[t.provider], index.byClassProvider[t.class][t.provider]}
	positions := make([]int, len(buckets))
	var out []rankedRule
	for {
		next, order := -1, int(^uint(0)>>1)
		for b, bucket := range buckets {
			if positions[b] < len(bucket) && bucket[positions[b]].order < order {
				next, order = b, bucket[positions[b]].order
			}
		}
		if next < 0 {
			return out
		}
		r := buckets[next][positions[next]]
		positions[next]++
		if rk, ok := rankRule(r, t); ok {
			out = append(out, rankedRule{rule: r, rank: rk})
		}
	}
}

type winner struct {
	rank rank
	rule *rule
}

// ErrAmbiguousOverlap is AmbiguousOverlapError: two equal-rank rules
// contest one axis.
type ErrAmbiguousOverlap struct {
	Provider, Model, Axis, First, Second string
}

func (e *ErrAmbiguousOverlap) Error() string {
	return fmt.Sprintf("ambiguous overlap for `%s/%s` on axis `%s`: rules `%s` and `%s` tie; add an explicit priority", e.Provider, e.Model, e.Axis, e.First, e.Second)
}

func contestAxis(winners map[string]winner, axis string, rk rank, r *rule, t target) error {
	if held, ok := winners[axis]; ok {
		order := held.rank.compare(rk)
		if order == 0 {
			return &ErrAmbiguousOverlap{Provider: t.provider, Model: t.model, Axis: axis, First: held.rule.Source, Second: r.Source}
		}
		if order > 0 {
			return nil
		}
	}
	winners[axis] = winner{rank: rk, rule: r}
	return nil
}

func contest(winners map[string]winner, axes map[string]any, rk rank, r *rule, t target) error {
	for _, axis := range sortedKeys(axes) {
		if err := contestAxis(winners, axis, rk, r, t); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// axes is ResolvedAxes.
type axes struct {
	wire      map[string]any
	thinking  map[string]any
	catalog   map[string]any
	reasoning bool
}

func collect(winners map[string]winner, pick func(*rule) map[string]any) map[string]any {
	out := make(map[string]any, len(winners))
	for axis, w := range winners {
		out[axis] = cloneValue(pick(w.rule)[axis])
	}
	return out
}

func cloneValue(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = cloneValue(v[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = cloneValue(item)
		}
		return out
	}
	return value
}

func identityScoped(r *rule) bool {
	return r.Class != nil && *r.Class != "unknown" || r.Family != nil || r.Revision != nil || r.Models != nil
}

// resolveCascade is resolveCascade: per axis, the matching rule with the
// greatest (model-selector exactness, constrained dimensions, priority) wins.
func resolveCascade(t target) (axes, error) {
	ranked := relevant(prepare(t))
	reasoning := t.reasoning
	if !reasoning {
		upgrade := map[string]winner{}
		hasEfforts := false
		for _, rr := range ranked {
			thinking := rr.rule.Thinking
			if thinking == nil {
				continue
			}
			if _, ok := thinking["upgradeNeutral"]; ok {
				if err := contestAxis(upgrade, "upgradeNeutral", rr.rank, rr.rule, t); err != nil {
					return axes{}, err
				}
			}
			if _, ok := thinking["efforts"]; ok {
				hasEfforts = true
			}
			if rr.rule.exactEff && rr.rank[0] == 2 {
				reasoning = true
			}
		}
		if held, ok := upgrade["upgradeNeutral"]; ok && identityScoped(held.rule) && held.rule.Thinking["upgradeNeutral"] == true && hasEfforts {
			reasoning = true
		}
	}
	offersThinking := reasoning || t.class == "unknown"
	wire, thinking, catalog := map[string]winner{}, map[string]winner{}, map[string]winner{}
	for _, rr := range ranked {
		if err := contest(wire, rr.rule.Wire, rr.rank, rr.rule, t); err != nil {
			return axes{}, err
		}
		if err := contest(catalog, rr.rule.Catalog, rr.rank, rr.rule, t); err != nil {
			return axes{}, err
		}
		if offersThinking {
			if err := contest(thinking, rr.rule.Thinking, rr.rank, rr.rule, t); err != nil {
				return axes{}, err
			}
		}
	}
	return axes{
		wire:      collect(wire, func(r *rule) map[string]any { return r.Wire }),
		thinking:  collect(thinking, func(r *rule) map[string]any { return r.Thinking }),
		catalog:   collect(catalog, func(r *rule) map[string]any { return r.Catalog }),
		reasoning: reasoning,
	}, nil
}
