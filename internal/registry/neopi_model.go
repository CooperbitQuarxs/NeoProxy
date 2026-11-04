package registry

import (
	"maps"
	"slices"
)

// NeoPiModel is a model as NeoPi's catalog resolves it (pi-catalog buildModel):
// the wire API, limits, effort ladder and the compat facts its OpenAI-family
// request builders branch on. Providers ported from NeoPi register it on
// ModelInfo.NeoPi; their executors shape requests from it alone.
type NeoPiModel struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	API           string         `json:"api"`
	Provider      string         `json:"provider"`
	BaseURL       string         `json:"baseUrl"`
	Reasoning     bool           `json:"reasoning"`
	Input         []string       `json:"input"`
	ContextWindow int            `json:"contextWindow"`
	MaxTokens     int            `json:"maxTokens"`
	Thinking      *NeoPiThinking `json:"thinking,omitempty"`
	Compat        *NeoPiCompat   `json:"compat,omitempty"`
}

// NeoPiThinking is the model's effort ladder (ThinkingConfig efforts/effortMap).
type NeoPiThinking struct {
	Efforts   []string          `json:"efforts"`
	EffortMap map[string]string `json:"effortMap,omitempty"`
}

// NeoPiCompat carries the resolved OpenAI compat fields NeoPi's Responses
// request builder reads (ResolvedOpenAIResponsesCompat), under NeoPi's names.
type NeoPiCompat struct {
	SupportsDeveloperRole                        bool              `json:"supportsDeveloperRole"`
	SupportsStrictMode                           bool              `json:"supportsStrictMode"`
	SupportsReasoningEffort                      bool              `json:"supportsReasoningEffort"`
	OmitReasoningEffort                          bool              `json:"omitReasoningEffort"`
	ReasoningEffortMap                           map[string]string `json:"reasoningEffortMap,omitempty"`
	ReasoningDisableMode                         string            `json:"reasoningDisableMode"`
	SupportsReasoningSummary                     bool              `json:"supportsReasoningSummary"`
	IncludeEncryptedReasoning                    bool              `json:"includeEncryptedReasoning"`
	FilterReasoningHistory                       bool              `json:"filterReasoningHistory"`
	RequiresReasoningContentForToolCalls         bool              `json:"requiresReasoningContentForToolCalls"`
	RequiresReasoningContentForAllAssistantTurns bool              `json:"requiresReasoningContentForAllAssistantTurns"`
	AllowsSyntheticReasoningContentForToolCalls  bool              `json:"allowsSyntheticReasoningContentForToolCalls"`
	DisableReasoningOnForcedToolChoice           bool              `json:"disableReasoningOnForcedToolChoice"`
	DisableReasoningOnToolChoice                 bool              `json:"disableReasoningOnToolChoice"`
	DisableReasoningWithTools                    bool              `json:"disableReasoningWithTools"`
	SupportsImageDetailOriginal                  bool              `json:"supportsImageDetailOriginal"`
	SupportsSamplingParams                       bool              `json:"supportsSamplingParams"`
	SupportsPenaltyAndStopParams                 bool              `json:"supportsPenaltyAndStopParams"`
	SupportsToolChoice                           bool              `json:"supportsToolChoice"`
	SupportsForcedToolChoice                     bool              `json:"supportsForcedToolChoice"`
	SupportsNamedToolChoice                      bool              `json:"supportsNamedToolChoice"`
	SupportsConfigurationUpdate                  bool              `json:"supportsConfigurationUpdate"`
	SupportsLongPromptCacheRetention             bool              `json:"supportsLongPromptCacheRetention"`
	SupportsObfuscationOptOut                    bool              `json:"supportsObfuscationOptOut"`
	StrictResponsesPairing                       bool              `json:"strictResponsesPairing"`
	StoreResponses                               bool              `json:"storeResponses"`
	OfficialEndpoint                             bool              `json:"officialEndpoint"`
	IsOpenRouterHost                             bool              `json:"isOpenRouterHost"`
	IsVercelGatewayHost                          bool              `json:"isVercelGatewayHost"`
	WireModelIDMode                              string            `json:"wireModelIdMode"`
	AlwaysSendMaxTokens                          bool              `json:"alwaysSendMaxTokens"`
	ClampOutputToModelMax                        bool              `json:"clampOutputToModelMax"`
	RejectRootObjectUnion                        bool              `json:"rejectRootObjectUnion"`
	PromptCacheSessionHeader                     string            `json:"promptCacheSessionHeader,omitempty"`
}

// Clone deep-copies the record so registry readers cannot alias its maps.
func (m *NeoPiModel) Clone() *NeoPiModel {
	if m == nil {
		return nil
	}
	out := *m
	out.Input = slices.Clone(m.Input)
	if m.Thinking != nil {
		thinking := *m.Thinking
		thinking.Efforts = slices.Clone(m.Thinking.Efforts)
		thinking.EffortMap = maps.Clone(m.Thinking.EffortMap)
		out.Thinking = &thinking
	}
	if m.Compat != nil {
		compat := *m.Compat
		compat.ReasoningEffortMap = maps.Clone(m.Compat.ReasoningEffortMap)
		out.Compat = &compat
	}
	return &out
}
