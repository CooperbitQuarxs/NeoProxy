// Package neoparams derives the parameters a model supports from discovered
// facts and realizes chosen values on a client-protocol request body.
//
// A virtual model is a model plus properties that are always set. Which
// properties exist is never hard-coded per feature: Catalogue reads the model's
// registry facts, its provider siblings and the Codex client catalogue, and
// offers only what those facts support. A missing fact means the parameter is
// absent. The dashboard renders every Spec with a generic control.
package neoparams

import "encoding/json"

// Parameter kinds.
const (
	KindChoice = "choice"
	KindToggle = "toggle"
	KindRange  = "range"
	KindSet    = "set"
	KindNumber = "number"
)

// Parameter groups, in display order.
const (
	GroupPerformance = "Performance"
	GroupContext     = "Context"
	GroupReasoning   = "Reasoning"
	GroupSampling    = "Sampling"
	GroupOutput      = "Output"
)

// Stable parameter IDs.
const (
	IDSpeed            = "speed"
	IDContext          = "context"
	IDEffort           = "effort"
	IDEfforts          = "efforts"
	IDReasoningSummary = "reasoning_summary"
	IDVerbosity        = "verbosity"
	IDTemperature      = "temperature"
	IDTopP             = "top_p"
	IDMaxOutput        = "max_output"
)

// Setting modes: force overrides whatever the caller sent, default applies only
// when the caller sent nothing for that parameter.
const (
	ModeForce   = "force"
	ModeDefault = "default"
)

// Option is one value of a choice or set parameter.
type Option struct {
	Value      any     `json:"value"`
	Label      string  `json:"label"`
	Note       string  `json:"note,omitempty"`
	Multiplier float64 `json:"multiplier,omitempty"`
}

// Mark is a landmark on a range, for example the standard or maximum window.
type Mark struct {
	Value float64 `json:"value"`
	Label string  `json:"label"`
	Note  string  `json:"note,omitempty"`
}

// Band is a price band of a range: usage is billed at Multiplier between From
// and To.
type Band struct {
	From       float64 `json:"from"`
	To         float64 `json:"to"`
	Multiplier float64 `json:"multiplier"`
	Note       string  `json:"note,omitempty"`
}

// Spec describes one parameter a model supports.
type Spec struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Help    string   `json:"help"`
	Group   string   `json:"group"`
	Kind    string   `json:"kind"`
	Options []Option `json:"options,omitempty"`
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Step    float64  `json:"step,omitempty"`
	Default any      `json:"default"`
	Marks   []Mark   `json:"marks,omitempty"`
	Bands   []Band   `json:"bands,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	// Modes lists the setting modes the parameter accepts; empty means both.
	Modes []string `json:"modes,omitempty"`
	// Realize says in plain words what the parameter does on the wire.
	Realize string `json:"realize"`

	sibling string // model ID a "fast" speed swaps to (speed realized by a sibling)
}

// Sibling is the model ID the speed parameter switches to for "fast", or ""
// when speed is realized on the wire (service tier) or is not a speed spec.
func (s Spec) Sibling() string { return s.sibling }

// AllowsMode reports whether the parameter accepts the setting mode.
func (s Spec) AllowsMode(mode string) bool {
	if mode == "" {
		mode = ModeForce
	}
	if len(s.Modes) == 0 {
		return mode == ModeForce || mode == ModeDefault
	}
	for _, m := range s.Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// MarshalJSON always emits min, max and step for ranges and numbers: zero is a
// meaningful bound there.
func (s Spec) MarshalJSON() ([]byte, error) {
	type plain Spec
	if s.Kind != KindRange && s.Kind != KindNumber {
		return json.Marshal(plain(s))
	}
	return json.Marshal(struct {
		plain
		Min  float64 `json:"min"`
		Max  float64 `json:"max"`
		Step float64 `json:"step"`
	}{plain(s), s.Min, s.Max, s.Step})
}

// Applied is one parameter value the router realized for a request. Via says
// how: "tier" (service tier on the wire), "model" (swap to a sibling model),
// "suffix" (reasoning suffix on the model), "window" (advertised and enforced
// window) or "body" (a request field).
type Applied struct {
	ID      string `json:"id"`
	Value   any    `json:"value"`
	Realize string `json:"realize"`
	Via     string `json:"via,omitempty"`
}
