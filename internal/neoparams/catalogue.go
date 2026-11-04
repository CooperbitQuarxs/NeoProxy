package neoparams

import (
	"fmt"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// minContext is the smallest window the dashboard offers for the context range;
// a window may be narrowed below the model's standard window.
const minContext = 16000

var levelLabels = map[string]string{
	"none": "None", "auto": "Auto", "minimal": "Minimal", "low": "Low", "medium": "Medium",
	"high": "High", "xhigh": "Extra high", "max": "Maximum", "ultra": "Ultra",
}

func levelLabel(level string) string {
	if l, ok := levelLabels[level]; ok {
		return l
	}
	return level
}

func has(list []string, names ...string) bool {
	for _, n := range names {
		if slices.Contains(list, n) {
			return true
		}
	}
	return false
}

// Catalogue derives the parameters model supports. info is the model's registry
// entry (nil when unknown) and siblings every model of the inventory, which is
// scanned for the model's "-fast" variant. The result is deterministic and
// contains only what the facts support: a model with no facts has no parameters.
func Catalogue(model neopool.Model, info *registry.ModelInfo, siblings []neopool.Model) []Spec {
	var entry codexEntry
	hasEntry := false
	if model.ProviderID == providerCodex {
		entry, hasEntry = codexClientEntry(model.Name)
	}
	var supported []string
	if info != nil {
		supported = info.SupportedParameters
	}
	var specs []Spec
	if s, ok := speedSpec(model, info, entry, hasEntry, siblings); ok {
		specs = append(specs, s)
	}
	if s, ok := contextSpec(model, info, entry, hasEntry); ok {
		specs = append(specs, s)
	}
	if levels := effortLevels(info); len(levels) > 0 {
		specs = append(specs, effortSpecs(levels, entry, hasEntry)...)
	}
	if hasEntry && entry.DefaultReasoningSummary != "" {
		specs = append(specs, Spec{
			ID: IDReasoningSummary, Label: "Reasoning summary", Group: GroupReasoning, Kind: KindChoice,
			Help: "How much of the model's reasoning is summarized in the answer.",
			Options: []Option{
				{Value: "none", Label: "None"}, {Value: "auto", Label: "Auto"},
				{Value: "concise", Label: "Concise"}, {Value: "detailed", Label: "Detailed"},
			},
			Default: entry.DefaultReasoningSummary,
			Realize: "Sets reasoning.summary on Responses requests.",
		})
	}
	if (hasEntry && entry.SupportVerbosity) || has(supported, "verbosity") {
		var def any
		if hasEntry && entry.DefaultVerbosity != "" {
			def = entry.DefaultVerbosity
		}
		specs = append(specs, Spec{
			ID: IDVerbosity, Label: "Verbosity", Group: GroupOutput, Kind: KindChoice,
			Help:    "How long the answers are.",
			Options: []Option{{Value: "low", Label: "Low"}, {Value: "medium", Label: "Medium"}, {Value: "high", Label: "High"}},
			Default: def,
			Realize: "Sets text.verbosity (verbosity on Chat Completions).",
		})
	}
	// Discovery says whether the provider passes sampling knobs on; NeoPi's Codex strips them for these models.
	sampling := !discovered(info) || info.Codex.Compat == nil || info.Codex.Compat.SupportsSamplingParams
	if sampling && has(supported, "temperature") {
		max := 2.0
		if info != nil && info.Type == "claude" {
			max = 1
		}
		specs = append(specs, Spec{
			ID: IDTemperature, Label: "Temperature", Group: GroupSampling, Kind: KindRange,
			Help: "How random the sampling is; lower is more deterministic.",
			Min:  0, Max: max, Step: 0.05, Default: nil,
			Realize: "sets temperature",
		})
	}
	if sampling && has(supported, "top_p") {
		specs = append(specs, Spec{
			ID: IDTopP, Label: "Top P", Group: GroupSampling, Kind: KindRange,
			Help: "Only the most likely words that add up to this share are considered.",
			Min:  0, Max: 1, Step: 0.01, Default: nil,
			Realize: "sets top_p",
		})
	}
	if sampling && has(supported, "max_tokens", "max_completion_tokens", "max_output_tokens") && info != nil {
		limit := info.MaxCompletionTokens
		if limit <= 0 {
			limit = info.OutputTokenLimit
		}
		if limit > 0 {
			specs = append(specs, Spec{
				ID: IDMaxOutput, Label: "Maximum output", Group: GroupOutput, Kind: KindNumber,
				Help: "The most tokens one answer may use.",
				Min:  1, Max: float64(limit), Step: 1, Default: nil, Unit: "tokens",
				Realize: "sets max_tokens (max_output_tokens on Responses)",
			})
		}
	}
	return specs
}

func speedSpec(model neopool.Model, info *registry.ModelInfo, entry codexEntry, hasEntry bool, siblings []neopool.Model) (Spec, bool) {
	standard := Option{Value: "standard", Label: "Standard", Note: "Normal speed", Multiplier: 1}
	price, _ := priceFor(model.ProviderID, model.Name)
	if tiers := serviceTiers(model.ProviderID, model.Name, info, entry, hasEntry); len(tiers) > 0 {
		spec := Spec{
			ID: IDSpeed, Label: "Speed", Group: GroupPerformance, Kind: KindChoice,
			Help:    "How quickly the model answers. Faster tiers use more of the plan.",
			Options: []Option{standard}, Default: "standard",
		}
		realize := ""
		for _, t := range tiers {
			spec.Options = append(spec.Options, Option{Value: t.Value, Label: t.Label, Note: t.Note, Multiplier: t.Multiplier})
			if realize != "" {
				realize += "; "
			}
			realize += t.Label + " sends service_tier=" + t.Wire
		}
		spec.Realize = realize
		return spec, true
	}
	for _, suffix := range []string{"-fast", "-build-fast"} {
		i := slices.IndexFunc(siblings, func(s neopool.Model) bool {
			return s.ProviderID == model.ProviderID && s.ID == model.ID+suffix && s.Exposed
		})
		if i < 0 {
			continue
		}
		fast := Option{Value: "fast", Label: "Fast", Note: "Served by " + siblings[i].Name, Multiplier: price.fastSibling}
		if price.fastSibling > 0 {
			fast.Note += " · " + formatMultiplier(price.fastSibling) + " price"
		}
		return Spec{
			ID: IDSpeed, Label: "Speed", Group: GroupPerformance, Kind: KindChoice,
			Help:    "How quickly the model answers. Fast uses the provider's faster variant of this model.",
			Options: []Option{standard, fast}, Default: "standard",
			Realize: "Fast switches to " + siblings[i].Name,
			sibling: siblings[i].ID,
		}, true
	}
	return Spec{}, false
}

func contextSpec(model neopool.Model, info *registry.ModelInfo, entry codexEntry, hasEntry bool) (Spec, bool) {
	// Live discovery (with NeoPi's per-model corrections) is authoritative; the
	// Codex client catalogue and the static registry are fallbacks.
	standard := 0
	switch {
	case discovered(info) && info.ContextLength > 0:
		standard = info.ContextLength
	case hasEntry && entry.ContextWindow > 0:
		standard = entry.ContextWindow
	case info != nil && info.ContextLength > 0:
		standard = info.ContextLength
	default:
		standard = model.Context
	}
	if standard <= 0 {
		return Spec{}, false
	}
	max := standard
	if hasEntry && entry.MaxContextWindow > max {
		max = entry.MaxContextWindow
	}
	if info != nil && info.MaxContextLength > max {
		max = info.MaxContextLength
	}
	spec := Spec{
		ID: IDContext, Label: "Context window", Group: GroupContext, Kind: KindRange,
		Help: "The largest prompt this model accepts. Larger prompts are refused.",
		Min:  float64(min(minContext, standard)), Max: float64(max), Step: 1000, Default: standard, Unit: "tokens",
		Marks: []Mark{{Value: float64(standard), Label: "Standard", Note: "the model's own window"}},
		Modes: []string{ModeForce},
	}
	if hasEntry && entry.EffectiveContextPercent > 0 && entry.EffectiveContextPercent < 100 {
		spec.Marks = append(spec.Marks, Mark{Value: float64(standard * entry.EffectiveContextPercent / 100), Label: "Effective", Note: "usable before the client compacts"})
	}
	if max > standard {
		spec.Marks = append(spec.Marks, Mark{Value: float64(max), Label: "Maximum", Note: "the provider's extended window"})
	}
	if price, ok := priceFor(model.ProviderID, model.Name); ok && price.longMultiplier > 0 {
		from := price.longFrom
		if from == 0 {
			from = standard
		}
		if from < max {
			spec.Bands = []Band{{From: float64(from), To: float64(max), Multiplier: price.longMultiplier, Note: price.longNote}}
		}
	}
	spec.Realize = "Shown as the window in /v1/models; longer prompts are refused. Nothing extra is sent: the provider takes up to " + compactTokens(max) + "."
	return spec, true
}

func effortSpecs(levels []string, entry codexEntry, hasEntry bool) []Spec {
	options := make([]Option, 0, len(levels))
	all := make([]any, 0, len(levels))
	for _, l := range levels {
		options = append(options, Option{Value: l, Label: levelLabel(l), Note: levelNote(entry, hasEntry, l)})
		all = append(all, l)
	}
	var def any
	if hasEntry && slices.Contains(levels, entry.DefaultReasoningLevel) {
		def = entry.DefaultReasoningLevel
	}
	return []Spec{
		{
			ID: IDEffort, Label: "Reasoning effort", Group: GroupReasoning, Kind: KindChoice,
			Help:    "How hard the model thinks before it answers.",
			Options: options, Default: def,
			Realize: "Sent as the request's reasoning effort.",
		},
		{
			ID: IDEfforts, Label: "Allowed efforts", Group: GroupReasoning, Kind: KindSet,
			Help:    "The reasoning levels a caller may pick; anything else becomes the highest allowed.",
			Options: options, Default: all, Modes: []string{ModeForce},
			Realize: "Any other effort a caller asks for becomes the highest allowed one.",
		},
	}
}

// ByID finds a spec.
func ByID(specs []Spec, id string) (Spec, bool) {
	i := slices.IndexFunc(specs, func(s Spec) bool { return s.ID == id })
	if i < 0 {
		return Spec{}, false
	}
	return specs[i], true
}

// String describes a spec for error messages.
func (s Spec) String() string { return fmt.Sprintf("%s (%s)", s.ID, s.Kind) }
