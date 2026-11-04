package neoparams

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// providerCodex is the registry provider of ChatGPT-subscription Codex models,
// the only provider the Codex client catalogue describes.
const providerCodex = "codex"

// codexEntry is the subset of a Codex client catalogue model the catalogue reads.
type codexEntry struct {
	Slug                    string `json:"slug"`
	ContextWindow           int    `json:"context_window"`
	MaxContextWindow        int    `json:"max_context_window"`
	EffectiveContextPercent int    `json:"effective_context_window_percent"`
	DefaultReasoningLevel   string `json:"default_reasoning_level"`
	DefaultReasoningSummary string `json:"default_reasoning_summary"`
	SupportVerbosity        bool   `json:"support_verbosity"`
	DefaultVerbosity        string `json:"default_verbosity"`
	ServiceTiers            []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"service_tiers"`
	SupportedReasoningLevels []struct {
		Effort      string `json:"effort"`
		Description string `json:"description"`
	} `json:"supported_reasoning_levels"`
}

var codexCatalogue struct {
	mu       sync.Mutex
	revision uint64
	loaded   bool
	models   map[string]codexEntry
}

// codexClientEntry returns the Codex client catalogue entry for a slug.
func codexClientEntry(slug string) (codexEntry, bool) {
	revision := registry.GetCodexClientModelsRevision()
	codexCatalogue.mu.Lock()
	defer codexCatalogue.mu.Unlock()
	if !codexCatalogue.loaded || codexCatalogue.revision != revision {
		var payload struct {
			Models []codexEntry `json:"models"`
		}
		models := make(map[string]codexEntry)
		if json.Unmarshal(registry.GetCodexClientModelsJSON(), &payload) == nil {
			for _, m := range payload.Models {
				models[m.Slug] = m
			}
		}
		codexCatalogue.models, codexCatalogue.revision, codexCatalogue.loaded = models, revision, true
	}
	m, ok := codexCatalogue.models[slug]
	return m, ok
}

// tierFact is a speed tier a model offers. Wire is the service tier the
// provider accepts; Value is the stable option value.
type tierFact struct {
	Value string
	Wire  string
	Label string
	Note  string
	// Multiplier is the published usage multiplier, 0 when unknown.
	Multiplier float64
}

// discovered reports whether the model carries Codex's live, account-scoped
// discovery record; its tiers, windows and sampling support are then authoritative.
func discovered(info *registry.ModelInfo) bool { return info != nil && info.Codex != nil }

// providerPrice is what is published about a provider's price steps. Entries
// exist only for known facts; an absent entry means no band and no multiplier.
type providerPrice struct {
	provider string
	models   func(name string) bool
	// tierMultiplier is the usage multiplier per wire service tier.
	tierMultiplier map[string]float64
	// fastSibling is the multiplier of the "-fast" sibling model.
	fastSibling float64
	// longFrom is where the long-context price starts (0: the standard window)
	// and longMultiplier the factor above it.
	longFrom       int
	longMultiplier float64
	longNote       string
}

var priceFacts = []providerPrice{
	{
		provider:       providerCodex,
		models:         func(string) bool { return true },
		tierMultiplier: map[string]float64{"priority": 2.5, "ultrafast": 8},
		longMultiplier: 2,
		longNote:       "Billed at twice the usage above the standard window",
	},
	{
		provider:       "xai",
		models:         func(name string) bool { return strings.HasPrefix(name, "grok-4.7") },
		fastSibling:    2,
		longFrom:       256000,
		longMultiplier: 2,
		longNote:       "Billed at twice the price above 256k tokens",
	},
}

func priceFor(provider, name string) (providerPrice, bool) {
	for _, p := range priceFacts {
		if p.provider == provider && p.models(name) {
			return p, true
		}
	}
	return providerPrice{}, false
}

// serviceTiers lists the speed tiers the model offers: the discovered service
// tiers when Codex discovery describes the model, else the Codex client
// catalogue's. Tiers NeoProxy cannot describe are left out.
func serviceTiers(provider, name string, info *registry.ModelInfo, entry codexEntry, hasEntry bool) []tierFact {
	if provider != providerCodex {
		return nil
	}
	var ids []string
	switch {
	case discovered(info):
		ids = info.ServiceTiers
	case hasEntry:
		for _, t := range entry.ServiceTiers {
			ids = append(ids, t.ID)
		}
	}
	describe := func(id string) string {
		for _, t := range entry.ServiceTiers {
			if t.ID == id {
				return t.Description
			}
		}
		return ""
	}
	price, _ := priceFor(provider, name)
	var out []tierFact
	for _, id := range ids {
		switch id {
		case "priority":
			out = append(out, tierFact{Value: "fast", Wire: "priority", Label: "Fast", Note: usageNote(describe(id), price.tierMultiplier[id]), Multiplier: price.tierMultiplier[id]})
		case "ultrafast":
			out = append(out, tierFact{Value: "ultrafast", Wire: "ultrafast", Label: "Ultrafast", Note: usageNote("Needs a plan that includes Ultrafast", price.tierMultiplier[id]), Multiplier: price.tierMultiplier[id]})
		}
	}
	return out
}

var timesSuffix = regexp.MustCompile(`(\d(?:\.\d+)?)x\b`)

// usageNote joins the provider's speed description ("1.5x speed") with the usage
// multiplier, writing multipliers one way ("1.5×") and dropping the provider's
// vaguer ", increased usage" when the number says it.
func usageNote(speed string, multiplier float64) string {
	note := timesSuffix.ReplaceAllString(strings.TrimSpace(speed), "$1×")
	if multiplier > 0 {
		note = strings.TrimSuffix(strings.TrimSuffix(note, ", increased usage"), " increased usage")
		if note != "" {
			note += " · "
		}
		note += formatMultiplier(multiplier) + " usage"
	}
	return note
}

func formatMultiplier(m float64) string { return strconv.FormatFloat(m, 'f', -1, 64) + "×" }

// compactTokens writes a token count the way people say it: 872k, 1M, 1.05M tokens.
func compactTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', -1, 64) + "M tokens"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1_000, 'f', -1, 64) + "k tokens"
	}
	return strconv.Itoa(n) + " tokens"
}

// effortLevels are the reasoning levels the model accepts, lowest first as the
// model lists them.
func effortLevels(info *registry.ModelInfo) []string {
	if info == nil || info.Thinking == nil {
		return nil
	}
	levels := slices.Clone(info.Thinking.Levels)
	if len(levels) == 0 && info.Thinking.Max > 0 {
		// Budget models take the universal levels through the thinking suffix.
		levels = []string{"low", "medium", "high"}
		if info.Thinking.ZeroAllowed {
			levels = append([]string{"none"}, levels...)
		}
	}
	return levels
}

func levelNote(entry codexEntry, hasEntry bool, level string) string {
	if !hasEntry {
		return ""
	}
	for _, l := range entry.SupportedReasoningLevels {
		if l.Effort == level {
			return l.Description
		}
	}
	return ""
}
