package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicatalog"
)

// cursorNeoPiJSON is NeoPi's bundled cursor catalog and the per-id policy
// its rule engine resolves, exported by testdata/capture_cursor_npi_catalog.ts.
//
//go:embed models/cursor_neopi.json
var cursorNeoPiJSON []byte

// cursorPolicyEntry is what resolveModelPolicy yields for one model id:
// the ladder it derives without explicit thinking, the fields it fills into
// explicit thinking, and the catalog fields buildModel applies.
type cursorPolicyEntry struct {
	Catalog struct {
		ContextWindowFloor                 *int     `json:"contextWindowFloor"`
		InputModalities                    []string `json:"inputModalities"`
		RequiresCursorToolSchemaProjection bool     `json:"requiresCursorToolSchemaProjection"`
		MaxTokensFallback                  *int     `json:"maxTokensFallback"`
		MaxTokensCeiling                   *int     `json:"maxTokensCeiling"`
		LimitsPatch                        *struct {
			ContextWindow *float64 `json:"contextWindow"`
			MaxTokens     *float64 `json:"maxTokens"`
		} `json:"limitsPatch"`
		CostPatch    *cursorCostCard `json:"costPatch"`
		CostFallback *cursorCostCard `json:"costFallback"`
		LongContext  *cursorLongRule `json:"longContext"`
	} `json:"catalog"`
	Derived *neopicatalog.Thinking `json:"derived"`
	Fill    struct {
		RequiresEffort bool   `json:"requiresEffort"`
		DefaultLevel   string `json:"defaultLevel"`
		PrefixBinding  bool   `json:"prefixBinding"`
	} `json:"fill"`

	patchCards, fallbackCards []datedCostCard
}

// cursorCostCard is a cost-patch/cost-fallback payload with its dated cards.
type cursorCostCard struct {
	Input          *float64                   `json:"input"`
	Output         *float64                   `json:"output"`
	CacheRead      *float64                   `json:"cacheRead"`
	CacheWrite     *float64                   `json:"cacheWrite"`
	EffectiveRates map[string]json.RawMessage `json:"effectiveRates"`
}

// cursorLongRule is a long-context payload, in explicit or multiplier form.
type cursorLongRule struct {
	InputThreshold          *float64 `json:"inputThreshold"`
	InputThresholdInclusive bool     `json:"inputThresholdInclusive"`
	Multiplier              *float64 `json:"multiplier"`
	OutputMultiplier        *float64 `json:"outputMultiplier"`
	Input                   *float64 `json:"input"`
	Output                  *float64 `json:"output"`
	CacheRead               *float64 `json:"cacheRead"`
	CacheWrite              *float64 `json:"cacheWrite"`
}

// datedCostCard is DatedCostCard: the legs a card overwrites from EffectiveFrom.
type datedCostCard struct {
	EffectiveFrom                        int64
	Input, Output, CacheRead, CacheWrite *float64
}

// earliestEffectiveFrom is EARLIEST_EFFECTIVE_FROM (the Date range minimum, ms).
const earliestEffectiveFrom int64 = -8_640_000_000_000_000

var utcInstant = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{3})?Z$`)

// parseDatedCostCards is parseDatedCostCards: effective-rate cards ascending by date.
func parseDatedCostCards(card *cursorCostCard) ([]datedCostCard, error) {
	if card == nil || card.EffectiveRates == nil {
		return nil, nil
	}
	var cards []datedCostCard
	for name, raw := range card.EffectiveRates {
		var entry struct {
			EffectiveFrom                        string
			Input, Output, CacheRead, CacheWrite *float64
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("effective-rate %s: %w", name, err)
		}
		if !utcInstant.MatchString(entry.EffectiveFrom) {
			return nil, fmt.Errorf("effective-rate %s: expected an ISO UTC timestamp", name)
		}
		at, err := time.Parse(time.RFC3339Nano, entry.EffectiveFrom)
		if err != nil {
			return nil, fmt.Errorf("effective-rate %s: %w", name, err)
		}
		cards = append(cards, datedCostCard{EffectiveFrom: at.UnixMilli(), Input: entry.Input, Output: entry.Output, CacheRead: entry.CacheRead, CacheWrite: entry.CacheWrite})
	}
	sort.SliceStable(cards, func(i, j int) bool { return cards[i].EffectiveFrom < cards[j].EffectiveFrom })
	return cards, nil
}

var cursorCatalog = func() struct {
	bundled []*neopicatalog.Spec
	policy  map[string]*cursorPolicyEntry
	table   *neopicatalog.Table
} {
	var data struct {
		Bundled []*neopicatalog.Spec          `json:"bundled"`
		Policy  map[string]*cursorPolicyEntry `json:"policy"`
	}
	if err := json.Unmarshal(cursorNeoPiJSON, &data); err != nil {
		panic(fmt.Sprintf("registry: cursor_neopi.json: %v", err))
	}
	for id, entry := range data.Policy {
		var err error
		if entry.patchCards, err = parseDatedCostCards(entry.Catalog.CostPatch); err != nil {
			panic(fmt.Sprintf("registry: cursor policy %s cost-patch: %v", id, err))
		}
		if entry.fallbackCards, err = parseDatedCostCards(entry.Catalog.CostFallback); err != nil {
			panic(fmt.Sprintf("registry: cursor policy %s cost-fallback: %v", id, err))
		}
	}
	return struct {
		bundled []*neopicatalog.Spec
		policy  map[string]*cursorPolicyEntry
		table   *neopicatalog.Table
	}{data.Bundled, data.Policy, neopicatalog.ReviewedTable("cursor")}
}()

// cursorPolicy is NeoPi's rule engine for cursor rows, read from the table.
type cursorPolicy struct{ now int64 }

// entry is the policy of the id the spec resolves rules under (policyModelId).
func (cursorPolicy) entry(spec *neopicatalog.Spec) *cursorPolicyEntry {
	id := spec.ID
	if spec.RequestModelID != nil && *spec.RequestModelID != spec.ID &&
		neopicatalog.Classify(spec.Provider, spec.ID).Class == "unknown" &&
		neopicatalog.Classify(spec.Provider, *spec.RequestModelID).Class != "unknown" {
		id = *spec.RequestModelID
	}
	return cursorCatalog.policy[id]
}

// DerivedThinking implements neopicatalog.Policy.
func (p cursorPolicy) DerivedThinking(spec *neopicatalog.Spec) *neopicatalog.Thinking {
	if !spec.Reasoning {
		return nil
	}
	if entry := p.entry(spec); entry != nil {
		return entry.Derived.Clone()
	}
	return nil
}

// thinking is resolveThinkingPolicy for cursor rows: no reasoning yields no
// ladder; explicit thinking keeps its fields and gains the rule fills; else
// the derived ladder.
func (p cursorPolicy) thinking(spec *neopicatalog.Spec) *neopicatalog.Thinking {
	if !spec.Reasoning {
		return nil
	}
	if spec.Thinking != nil && len(spec.Thinking.Efforts) > 0 {
		thinking := spec.Thinking.Clone()
		if entry := p.entry(spec); entry != nil {
			if !thinking.RequiresEffort && entry.Fill.RequiresEffort {
				thinking.RequiresEffort = true
			}
			if thinking.DefaultLevel == "" && entry.Fill.DefaultLevel != "" {
				thinking.DefaultLevel = entry.Fill.DefaultLevel
			}
			if !thinking.PrefixBinding && entry.Fill.PrefixBinding {
				thinking.PrefixBinding = true
			}
		}
		return thinking
	}
	return p.DerivedThinking(spec)
}

var (
	cursorAuthorPrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 .+&'-]{0,23}: `)
	cursorNoiseTags    = regexp.MustCompile(`\s*\((?:latest|Antigravity|\$+|>?\d+% off|retires [^)]*)\)`)
	cursorDoubleSpaces = regexp.MustCompile(` {2,}`)
)

// cleanModelName is pi-catalog cleanModelName.
func cleanModelName(name string) string {
	cleaned := strings.TrimSpace(cursorDoubleSpaces.ReplaceAllString(cursorNoiseTags.ReplaceAllString(cursorAuthorPrefix.ReplaceAllString(name, ""), ""), " "))
	if cleaned == "" {
		return name
	}
	return cleaned
}

// build is buildModel over the fields the pipeline reads. An id the exported
// policy does not know builds under empty rules: no derived ladder, no
// catalog corrections.
func (p cursorPolicy) build(spec *neopicatalog.Spec) *neopicatalog.Spec {
	entry := p.entry(spec)
	if entry == nil {
		entry = &cursorPolicyEntry{}
	}
	model := spec.Clone()
	model.Thinking = p.thinking(spec)
	model.Reasoning = spec.Reasoning || model.Thinking != nil
	model.Name = cleanModelName(spec.Name)
	model.RequiresCursorToolSchemaProjection = nil
	catalog := &entry.Catalog
	if catalog.RequiresCursorToolSchemaProjection {
		model.RequiresCursorToolSchemaProjection = neopicatalog.Ptr(true)
	}
	upstream := spec.Cost
	model.Cost = p.correctedCost(upstream, entry, p.now)
	if dated := p.scheduledRates(upstream, entry); dated != nil {
		model.Cost.TimeBased = &neopicatalog.TimeBasedCost{OffPeakMultiplier: 1, EffectiveRates: dated}
	}
	if patch := catalog.LimitsPatch; patch != nil {
		if patch.ContextWindow != nil {
			model.ContextWindow = neopicatalog.Ptr(int(*patch.ContextWindow))
		}
		if patch.MaxTokens != nil {
			model.MaxTokens = neopicatalog.Ptr(int(*patch.MaxTokens))
		}
	}
	if catalog.MaxTokensFallback != nil && model.MaxTokens == nil {
		model.MaxTokens = neopicatalog.Ptr(*catalog.MaxTokensFallback)
	}
	if catalog.MaxTokensCeiling != nil && model.MaxTokens != nil && *model.MaxTokens > *catalog.MaxTokensCeiling {
		model.MaxTokens = neopicatalog.Ptr(*catalog.MaxTokensCeiling)
	}
	if catalog.ContextWindowFloor != nil {
		floor := *catalog.ContextWindowFloor
		if model.ContextWindow != nil && *model.ContextWindow > floor {
			floor = *model.ContextWindow
		}
		model.ContextWindow = neopicatalog.Ptr(floor)
	}
	if catalog.InputModalities != nil {
		model.Input = slices.Clone(catalog.InputModalities)
	}
	return model
}

func hasTokenPrice(c *neopicatalog.Cost) bool {
	return c.Input != 0 || c.Output != 0 || c.CacheRead != 0 || c.CacheWrite != 0
}

func applyLegs(c *neopicatalog.Cost, input, output, cacheRead, cacheWrite *float64) {
	if input != nil {
		c.Input = *input
	}
	if output != nil {
		c.Output = *output
	}
	if cacheRead != nil {
		c.CacheRead = *cacheRead
	}
	if cacheWrite != nil {
		c.CacheWrite = *cacheWrite
	}
}

// applyDueDatedCostCard overwrites legs with the latest card due at now.
func applyDueDatedCostCard(c *neopicatalog.Cost, cards []datedCostCard, now int64) {
	var due *datedCostCard
	for i := range cards {
		if cards[i].EffectiveFrom <= now {
			due = &cards[i]
		}
	}
	if due != nil {
		applyLegs(c, due.Input, due.Output, due.CacheRead, due.CacheWrite)
	}
}

// dueEffectiveRate is dueEffectiveRate over a materialized schedule.
func dueEffectiveRate(schedule *neopicatalog.TimeBasedCost, now int64) *neopicatalog.EffectiveRate {
	if schedule == nil {
		return nil
	}
	var due *neopicatalog.EffectiveRate
	for i := range schedule.EffectiveRates {
		candidate := &schedule.EffectiveRates[i]
		if candidate.EffectiveFrom <= now && (due == nil || candidate.EffectiveFrom > due.EffectiveFrom) {
			due = candidate
		}
	}
	return due
}

// correctedCost is build.ts correctedCost.
func (cursorPolicy) correctedCost(upstream neopicatalog.Cost, entry *cursorPolicyEntry, now int64) neopicatalog.Cost {
	cost := upstream
	cost.LongContext = clonePointer(upstream.LongContext)
	cost.TimeBased = upstream.TimeBased
	if upstream.TimeBased != nil && len(upstream.TimeBased.EffectiveRates) > 0 && upstream.TimeBased.EffectiveRates[0].EffectiveFrom == earliestEffectiveFrom {
		if card := dueEffectiveRate(upstream.TimeBased, now); card != nil {
			cost.Input, cost.Output, cost.CacheRead, cost.CacheWrite = card.Input, card.Output, card.CacheRead, card.CacheWrite
			cost.LongContext = clonePointer(card.LongContext)
		}
	}
	catalog := &entry.Catalog
	if patch := catalog.CostPatch; patch != nil {
		applyLegs(&cost, patch.Input, patch.Output, patch.CacheRead, patch.CacheWrite)
		applyDueDatedCostCard(&cost, entry.patchCards, now)
	}
	if fallback := catalog.CostFallback; fallback != nil && !hasTokenPrice(&cost) {
		applyLegs(&cost, fallback.Input, fallback.Output, fallback.CacheRead, fallback.CacheWrite)
		applyDueDatedCostCard(&cost, entry.fallbackCards, now)
	}
	if rule := catalog.LongContext; rule != nil && rule.InputThreshold != nil {
		switch {
		case rule.Multiplier != nil && hasTokenPrice(&cost):
			outputMultiplier := *rule.Multiplier
			if rule.OutputMultiplier != nil {
				outputMultiplier = *rule.OutputMultiplier
			}
			cost.LongContext = &neopicatalog.LongContextCost{InputThreshold: *rule.InputThreshold, InputThresholdInclusive: rule.InputThresholdInclusive,
				Input: cost.Input * *rule.Multiplier, Output: cost.Output * outputMultiplier, CacheRead: cost.CacheRead * *rule.Multiplier, CacheWrite: cost.CacheWrite * *rule.Multiplier}
		case rule.Input != nil && rule.Output != nil && rule.CacheRead != nil && rule.CacheWrite != nil:
			cost.LongContext = &neopicatalog.LongContextCost{InputThreshold: *rule.InputThreshold, Input: *rule.Input, Output: *rule.Output, CacheRead: *rule.CacheRead, CacheWrite: *rule.CacheWrite}
		}
	}
	return cost
}

// scheduledRates is build.ts scheduledRates: every price period the dated
// cards define, or nil when no card changes the price.
func (p cursorPolicy) scheduledRates(upstream neopicatalog.Cost, entry *cursorPolicyEntry) []neopicatalog.EffectiveRate {
	var dates []int64
	for _, card := range entry.patchCards {
		dates = append(dates, card.EffectiveFrom)
	}
	for _, card := range entry.fallbackCards {
		dates = append(dates, card.EffectiveFrom)
	}
	if len(dates) == 0 {
		return nil
	}
	slices.Sort(dates)
	var periods []neopicatalog.EffectiveRate
	previous := ""
	for _, from := range append([]int64{earliestEffectiveFrom}, dates...) {
		cost := p.correctedCost(upstream, entry, from)
		rate := neopicatalog.EffectiveRate{EffectiveFrom: from, Input: cost.Input, Output: cost.Output, CacheRead: cost.CacheRead, CacheWrite: cost.CacheWrite, LongContext: cost.LongContext}
		key, _ := json.Marshal(struct {
			I, O, R, W float64
			L          *neopicatalog.LongContextCost
		}{rate.Input, rate.Output, rate.CacheRead, rate.CacheWrite, rate.LongContext})
		if string(key) == previous {
			continue
		}
		previous = string(key)
		periods = append(periods, rate)
	}
	if len(periods) <= 1 {
		return nil
	}
	return periods
}

// datedCost is pricing.ts datedCost: a flat card plus its dated overrides.
func datedCost(base neopicatalog.Cost, cards []datedCostCard, now int64) neopicatalog.Cost {
	if len(cards) == 0 {
		return base
	}
	rates := []neopicatalog.EffectiveRate{{EffectiveFrom: earliestEffectiveFrom, Input: base.Input, Output: base.Output, CacheRead: base.CacheRead, CacheWrite: base.CacheWrite}}
	for _, card := range cards {
		next := rates[len(rates)-1]
		next.EffectiveFrom = card.EffectiveFrom
		applyRateLegs(&next, card)
		rates = append(rates, next)
	}
	schedule := &neopicatalog.TimeBasedCost{OffPeakMultiplier: 1, EffectiveRates: rates}
	due := dueEffectiveRate(schedule, now)
	return neopicatalog.Cost{Input: due.Input, Output: due.Output, CacheRead: due.CacheRead, CacheWrite: due.CacheWrite, LongContext: clonePointer(due.LongContext), TimeBased: schedule}
}

func applyRateLegs(rate *neopicatalog.EffectiveRate, card datedCostCard) {
	if card.Input != nil {
		rate.Input = *card.Input
	}
	if card.Output != nil {
		rate.Output = *card.Output
	}
	if card.CacheRead != nil {
		rate.CacheRead = *card.CacheRead
	}
	if card.CacheWrite != nil {
		rate.CacheWrite = *card.CacheWrite
	}
}

func clonePointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
