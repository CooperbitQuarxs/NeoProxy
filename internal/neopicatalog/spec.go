package neopicatalog

import (
	"encoding/json"
	"maps"
	"slices"
)

// Spec is the subset of NeoPi's ModelSpec/Model the catalog pipeline reads.
// Pointer, map and slice fields are nil when the JavaScript object lacks the
// key (or holds undefined): the object spreads NeoPi merges and collapses
// with depend on key presence, so absence is kept distinct from zero values.
// ContextWindow and MaxTokens are nil for null.
type Spec struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	API           string   `json:"api"`
	Provider      string   `json:"provider"`
	BaseURL       string   `json:"baseUrl"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
	Cost          Cost     `json:"cost"`
	ContextWindow *int     `json:"contextWindow"`
	MaxTokens     *int     `json:"maxTokens"`

	RequestModelID    *string   `json:"requestModelId,omitempty"`
	Thinking          *Thinking `json:"thinking,omitempty"`
	IsProviderDefault *bool     `json:"isProviderDefault,omitempty"`

	CursorMaxMode                      *bool                  `json:"cursorMaxMode,omitempty"`
	CursorMaxModeRoutes                map[string]bool        `json:"cursorMaxModeRoutes,omitempty"`
	CursorModelParameters              []ModelParameter       `json:"cursorModelParameters,omitempty"`
	CursorModelRoutes                  map[string]CursorRoute `json:"cursorModelRoutes,omitempty"`
	RequiresCursorToolSchemaProjection *bool                  `json:"requiresCursorToolSchemaProjection,omitempty"`
}

// MarshalJSON keeps a present but empty cursorModelParameters, which
// omitempty would drop.
func (s Spec) MarshalJSON() ([]byte, error) {
	type plain Spec
	out := struct {
		plain
		CursorModelParameters *[]ModelParameter `json:"cursorModelParameters,omitempty"`
	}{plain: plain(s)}
	if s.CursorModelParameters != nil {
		out.CursorModelParameters = &s.CursorModelParameters
	}
	return json.Marshal(out)
}

// ModelParameter is one Cursor model parameter value (RequestedModel parameters).
type ModelParameter struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// CursorRoute is CursorModelRoute: the rich model id and parameters a wire
// slug stands for.
type CursorRoute struct {
	ModelID    string           `json:"modelId"`
	Parameters []ModelParameter `json:"parameters"`
	MaxMode    *bool            `json:"maxMode,omitempty"`
}

// Thinking is ThinkingConfig. Booleans are only ever set true by NeoPi, so
// false stands for an absent key.
type Thinking struct {
	Mode            string            `json:"mode"`
	Efforts         []string          `json:"efforts"`
	EffortRouting   map[string]string `json:"effortRouting,omitempty"`
	DefaultLevel    string            `json:"defaultLevel,omitempty"`
	RequiresEffort  bool              `json:"requiresEffort,omitempty"`
	PrefixBinding   bool              `json:"prefixBinding,omitempty"`
	SuppressWhenOff bool              `json:"suppressWhenOff,omitempty"`
}

// Cost is ModelCost: per-million rates, a long-context tier and a dated
// schedule.
type Cost struct {
	Input       float64          `json:"input"`
	Output      float64          `json:"output"`
	CacheRead   float64          `json:"cacheRead"`
	CacheWrite  float64          `json:"cacheWrite"`
	LongContext *LongContextCost `json:"longContext,omitempty"`
	TimeBased   *TimeBasedCost   `json:"timeBased,omitempty"`
}

// LongContextCost is the tier billed past InputThreshold.
type LongContextCost struct {
	InputThreshold          float64 `json:"inputThreshold"`
	InputThresholdInclusive bool    `json:"inputThresholdInclusive,omitempty"`
	Input                   float64 `json:"input"`
	Output                  float64 `json:"output"`
	CacheRead               float64 `json:"cacheRead"`
	CacheWrite              float64 `json:"cacheWrite"`
}

// TimeBasedCost is a dated rate schedule without a recurring tariff (no peak
// windows, off-peak multiplier 1), the only form dated cards produce.
type TimeBasedCost struct {
	OffPeakMultiplier float64         `json:"offPeakMultiplier"`
	EffectiveRates    []EffectiveRate `json:"effectiveRates"`
}

// EffectiveRate is one dated card of a schedule.
type EffectiveRate struct {
	EffectiveFrom int64            `json:"effectiveFrom"`
	Input         float64          `json:"input"`
	Output        float64          `json:"output"`
	CacheRead     float64          `json:"cacheRead"`
	CacheWrite    float64          `json:"cacheWrite"`
	LongContext   *LongContextCost `json:"longContext,omitempty"`
}

// Clone deep-copies s.
func (s *Spec) Clone() *Spec {
	out := *s
	out.Input = slices.Clone(s.Input)
	out.Cost = s.Cost.clone()
	out.ContextWindow = clonePtr(s.ContextWindow)
	out.MaxTokens = clonePtr(s.MaxTokens)
	out.RequestModelID = clonePtr(s.RequestModelID)
	out.Thinking = s.Thinking.Clone()
	out.IsProviderDefault = clonePtr(s.IsProviderDefault)
	out.CursorMaxMode = clonePtr(s.CursorMaxMode)
	out.CursorMaxModeRoutes = maps.Clone(s.CursorMaxModeRoutes)
	out.CursorModelParameters = slices.Clone(s.CursorModelParameters)
	if s.CursorModelRoutes != nil {
		out.CursorModelRoutes = make(map[string]CursorRoute, len(s.CursorModelRoutes))
		for key, route := range s.CursorModelRoutes {
			route.Parameters = slices.Clone(route.Parameters)
			route.MaxMode = clonePtr(route.MaxMode)
			out.CursorModelRoutes[key] = route
		}
	}
	out.RequiresCursorToolSchemaProjection = clonePtr(s.RequiresCursorToolSchemaProjection)
	return &out
}

// Clone deep-copies t.
func (t *Thinking) Clone() *Thinking {
	if t == nil {
		return nil
	}
	out := *t
	out.Efforts = slices.Clone(t.Efforts)
	out.EffortRouting = maps.Clone(t.EffortRouting)
	return &out
}

func (c Cost) clone() Cost {
	out := c
	out.LongContext = clonePtr(c.LongContext)
	if c.TimeBased != nil {
		timeBased := *c.TimeBased
		timeBased.EffectiveRates = slices.Clone(c.TimeBased.EffectiveRates)
		for i := range timeBased.EffectiveRates {
			timeBased.EffectiveRates[i].LongContext = clonePtr(timeBased.EffectiveRates[i].LongContext)
		}
		out.TimeBased = &timeBased
	}
	return out
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// Ptr returns a pointer to v.
func Ptr[T any](v T) *T { return &v }

// ThinkingEfforts is THINKING_EFFORTS, least to most intensive.
var ThinkingEfforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// RoutingKeys is VARIANT_ROUTING_KEYS: "off" then every effort.
var RoutingKeys = append([]string{"off"}, ThinkingEfforts...)
