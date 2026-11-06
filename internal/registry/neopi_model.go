package registry

import (
	"maps"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicatalog"
)

// NeoPiModel is a model as NeoPi's catalog resolves it (pi-catalog buildModel):
// the wire API, limits, effort ladder and the compat facts its OpenAI-family
// request builders branch on. Providers ported from NeoPi register it on
// ModelInfo.NeoPi; their executors shape requests from it alone.
type NeoPiModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	API  string `json:"api"`
	// Kind is NeoPi's model kind ("image", "embedding", ...); empty is chat.
	Kind          string         `json:"kind,omitempty"`
	Provider      string         `json:"provider"`
	BaseURL       string         `json:"baseUrl"`
	Reasoning     bool           `json:"reasoning"`
	Input         []string       `json:"input"`
	ContextWindow int            `json:"contextWindow"`
	MaxTokens     int            `json:"maxTokens"`
	Thinking      *NeoPiThinking `json:"thinking,omitempty"`
	Compat        *NeoPiCompat   `json:"compat,omitempty"`
	// AnthropicCompat and CompletionsCompat carry the compat NeoPi resolves for
	// an anthropic-messages or openai-completions row respectively.
	AnthropicCompat   *NeoPiAnthropicCompat   `json:"anthropicCompat,omitempty"`
	CompletionsCompat *NeoPiCompletionsCompat `json:"completionsCompat,omitempty"`
	// CompatConfig is the row's authored sparse compat (NeoPi's compatConfig),
	// which a discovery merge rebuilds the resolved compat from.
	CompatConfig map[string]any `json:"compatConfig,omitempty"`
	// thinkingRequiresEffortSet marks a ladder whose requiresEffort NeoPi
	// holds explicitly (false included), which a rebuild must not refill.
	thinkingRequiresEffortSet bool
	// RequestModelID is the default wire id of a collapsed family row.
	RequestModelID string `json:"requestModelId,omitempty"`
	// OmitMaxOutputTokens is model.omitMaxOutputTokens: the request carries
	// no output-token limit, the caller's included.
	OmitMaxOutputTokens bool `json:"omitMaxOutputTokens,omitempty"`
	// cost is the row's token tariff (USD per million). NeoProxy bills
	// nothing from it; NeoPi's variant collapse keeps a thinking pair apart
	// when both members are priced differently.
	cost NeoPiCost
	// DevinCompat is the compat NeoPi resolves for a devin-agent row.
	DevinCompat *NeoPiDevinCompat `json:"devinCompat,omitempty"`
	// GoogleCompat is the compat NeoPi resolves for a google-generative-ai row.
	GoogleCompat *NeoPiGoogleCompat `json:"googleCompat,omitempty"`
	// Cursor is the cursor-agent routing NeoPi's discovery resolves.
	Cursor *NeoPiCursor `json:"cursor,omitempty"`
	// FactoryDroid is the account scope a factory-droid-agent row carries.
	FactoryDroid *NeoPiFactoryDroid `json:"factoryDroid,omitempty"`
	// OllamaCompat is the compat NeoPi resolves for an ollama-chat row.
	OllamaCompat *NeoPiOllamaCompat `json:"ollamaCompat,omitempty"`
}

// NeoPiOllamaCompat is ResolvedOllamaCompat: whether replayed assistant
// thinking is stripped from an ollama-chat request.
type NeoPiOllamaCompat struct {
	StripThinkingHistory bool `json:"stripThinkingHistory"`
}

// NeoPiGoogleCompat is the resolved compat NeoPi's Google request builders read
// (ResolvedGoogleCompat), under NeoPi's names.
type NeoPiGoogleCompat struct {
	MultimodalFunctionResponse                      bool `json:"multimodalFunctionResponse"`
	RequiresSkipThoughtSignature                    bool `json:"requiresSkipThoughtSignature"`
	RequiresSkipThoughtSignatureOnFirstFunctionCall bool `json:"requiresSkipThoughtSignatureOnFirstFunctionCall"`
	SupportsFunctionPartID                          bool `json:"supportsFunctionPartId"`
	DropUnsignedThinking                            bool `json:"dropUnsignedThinking"`
	CCALegacyParametersSchema                       bool `json:"ccaLegacyParametersSchema"`
}

// NeoPiCursor is a cursor-agent row's routing: its max-mode markers, fixed
// parameters, the rich route each wire id stands for, and whether its MCP
// tool schemas need Cursor's projection.
type NeoPiCursor struct {
	MaxMode                      *bool                               `json:"maxMode,omitempty"`
	MaxModeRoutes                map[string]bool                     `json:"maxModeRoutes,omitempty"`
	ModelParameters              []neopicatalog.ModelParameter       `json:"modelParameters,omitempty"`
	ModelRoutes                  map[string]neopicatalog.CursorRoute `json:"modelRoutes,omitempty"`
	RequiresToolSchemaProjection bool                                `json:"requiresToolSchemaProjection,omitempty"`
}

// NeoPiDevinCompat is DevinCompat: whether the row is an AssignModel router
// and whether it takes parallel tool calls.
type NeoPiDevinCompat struct {
	ModelRouter               bool `json:"modelRouter"`
	SupportsParallelToolCalls bool `json:"supportsParallelToolCalls"`
}

// NeoPiThinking is the model's effort ladder (ThinkingConfig efforts/effortMap)
// and, for budget-based providers, how an effort reaches the wire.
type NeoPiThinking struct {
	Efforts   []string          `json:"efforts"`
	EffortMap map[string]string `json:"effortMap,omitempty"`
	// Mode is NeoPi's ThinkingConfig.mode ("budget", "anthropic-budget-effort", ...).
	Mode string `json:"mode,omitempty"`
	// RequiresEffort models always think: no effort or thinking-off sends the lowest effort.
	RequiresEffort bool `json:"requiresEffort,omitempty"`
	// SuppressWhenOff models turn thinking off provider-side instead of
	// clamping an effort-less request to their floor.
	SuppressWhenOff bool  `json:"suppressWhenOff,omitempty"`
	SupportsDisplay *bool `json:"supportsDisplay,omitempty"`
	// EffortRouting maps an effort (or "off") to the wire id that serves it.
	EffortRouting map[string]string `json:"effortRouting,omitempty"`
	DefaultLevel  string            `json:"defaultLevel,omitempty"`
	// EffortBudgets maps an effort to its thinking-token budget (budget mode).
	EffortBudgets map[string]int `json:"effortBudgets,omitempty"`
	// PrefixBinding models bind thinking blocks to their conversation prefix
	// (thinking.block_binding) where the endpoint supports binding controls.
	PrefixBinding bool `json:"prefixBinding,omitempty"`
}

// NeoPiAnthropicCompat is the resolved compat NeoPi's Anthropic Messages
// provider reads for a non-Anthropic model (ResolvedAnthropicCompat).
type NeoPiAnthropicCompat struct {
	OfficialEndpoint bool `json:"officialEndpoint"`
	// SigningEndpoint marks an endpoint that enforces Anthropic thinking
	// signatures on replay (ZenMux, Copilot, Bedrock, ...).
	SigningEndpoint             bool `json:"signingEndpoint"`
	FirstPartyProvider          bool `json:"firstPartyProvider"`
	InjectClaudeCodeInstruction bool `json:"injectClaudeCodeInstruction"`
	ReplayUnsignedThinking      bool `json:"replayUnsignedThinking"`
	// EscapeBuiltinToolNames prefixes every tool name, built-in names included,
	// for gateways that reserve Anthropic's built-in tool names.
	EscapeBuiltinToolNames             bool `json:"escapeBuiltinToolNames"`
	RequiresToolResultID               bool `json:"requiresToolResultId"`
	DisableStrictTools                 bool `json:"disableStrictTools"`
	SupportsContextManagement          bool `json:"supportsContextManagement"`
	SupportsOutputEffort               bool `json:"supportsOutputEffort"`
	SupportsSamplingParams             bool `json:"supportsSamplingParams"`
	SupportsForcedToolChoice           bool `json:"supportsForcedToolChoice"`
	SupportsEagerToolInputStreaming    bool `json:"supportsEagerToolInputStreaming"`
	SupportsLongCacheRetention         bool `json:"supportsLongCacheRetention"`
	SupportsTurnScopedSystem           bool `json:"supportsTurnScopedSystem"`
	SupportsMidConversationToolChanges bool `json:"supportsMidConversationToolChanges"`
	SupportsPerMessageEffort           bool `json:"supportsPerMessageEffort"`
	SupportsThinkingBindingControls    bool `json:"supportsThinkingBindingControls"`
	SupportsBetweenToolsThinking       bool `json:"supportsBetweenToolsThinking"`
	// DisabledThinking is how an adaptive model's thinking-off request goes
	// out ("disabled", "omit", "adaptive", "between-tools"; empty for the default).
	DisabledThinking string `json:"disabledThinking,omitempty"`
}

// NeoPiCompletionsCompat is the resolved compat NeoPi's OpenAI Completions
// provider reads (ResolvedOpenAICompat), under NeoPi's names. WhenThinking is
// NeoPi's complete alternate view applied while reasoning is engaged.
type NeoPiCompletionsCompat struct {
	ThinkingFormat                               string                  `json:"thinkingFormat"`
	MaxTokensField                               string                  `json:"maxTokensField"`
	ReasoningContentField                        string                  `json:"reasoningContentField"`
	ReasoningDisableMode                         string                  `json:"reasoningDisableMode"`
	ReasoningEffortMap                           map[string]string       `json:"reasoningEffortMap,omitempty"`
	StreamMarkupHealingPattern                   string                  `json:"streamMarkupHealingPattern"`
	ToolStrictMode                               string                  `json:"toolStrictMode"`
	ToolSchemaFlavor                             string                  `json:"toolSchemaFlavor,omitempty"`
	WireModelIDMode                              string                  `json:"wireModelIdMode"`
	ThinkingKeep                                 string                  `json:"thinkingKeep,omitempty"`
	ThinkingLoopGuard                            string                  `json:"thinkingLoopGuard,omitempty"`
	SyntheticReasoningContentFallback            string                  `json:"syntheticReasoningContentFallback,omitempty"`
	PromptCacheSessionHeader                     string                  `json:"promptCacheSessionHeader,omitempty"`
	SessionHeader                                string                  `json:"sessionHeader,omitempty"`
	KimiAPIFormat                                string                  `json:"kimiApiFormat,omitempty"`
	PromptCacheBreakpointTTL                     string                  `json:"promptCacheBreakpointTtl,omitempty"`
	StreamRevision                               string                  `json:"streamRevision,omitempty"`
	UnsupportedSamplingParams                    []string                `json:"unsupportedSamplingParams,omitempty"`
	TrustExplicitThinkingOnly                    *bool                   `json:"trustExplicitThinkingOnly,omitempty"`
	SupportsPromptCacheBreakpoints               *bool                   `json:"supportsPromptCacheBreakpoints,omitempty"`
	StripThinkingHistory                         *bool                   `json:"stripThinkingHistory,omitempty"`
	OfficialEndpoint                             *bool                   `json:"officialEndpoint,omitempty"`
	ExtraBody                                    map[string]any          `json:"extraBody,omitempty"`
	StreamIdleTimeoutMs                          int                     `json:"streamIdleTimeoutMs,omitempty"`
	StreamFirstEventTimeoutMs                    *int                    `json:"streamFirstEventTimeoutMs,omitempty"`
	AlwaysSendMaxTokens                          bool                    `json:"alwaysSendMaxTokens"`
	ClampOutputToModelMax                        bool                    `json:"clampOutputToModelMax"`
	SupportsDeveloperRole                        bool                    `json:"supportsDeveloperRole"`
	SupportsStrictMode                           bool                    `json:"supportsStrictMode"`
	SupportsStore                                bool                    `json:"supportsStore"`
	SupportsUsageInStreaming                     bool                    `json:"supportsUsageInStreaming"`
	SupportsSamplingParams                       bool                    `json:"supportsSamplingParams"`
	SupportsPenaltyAndStopParams                 bool                    `json:"supportsPenaltyAndStopParams"`
	SupportsReasoningEffort                      bool                    `json:"supportsReasoningEffort"`
	SupportsReasoningParams                      bool                    `json:"supportsReasoningParams"`
	SupportsToolChoice                           bool                    `json:"supportsToolChoice"`
	SupportsForcedToolChoice                     bool                    `json:"supportsForcedToolChoice"`
	SupportsNamedToolChoice                      bool                    `json:"supportsNamedToolChoice"`
	SupportsMultipleSystemMessages               bool                    `json:"supportsMultipleSystemMessages"`
	SupportsPromptCacheKey                       bool                    `json:"supportsPromptCacheKey"`
	IncludeEncryptedReasoning                    bool                    `json:"includeEncryptedReasoning"`
	FilterReasoningHistory                       bool                    `json:"filterReasoningHistory"`
	RetryWithoutStrictOnGrammarError             bool                    `json:"retryWithoutStrictOnGrammarError"`
	OmitReasoningEffort                          bool                    `json:"omitReasoningEffort"`
	DisableReasoningOnForcedToolChoice           bool                    `json:"disableReasoningOnForcedToolChoice"`
	DisableReasoningOnToolChoice                 bool                    `json:"disableReasoningOnToolChoice"`
	DisableReasoningWithTools                    bool                    `json:"disableReasoningWithTools"`
	DropThinkingWhenReasoningEffort              bool                    `json:"dropThinkingWhenReasoningEffort"`
	NativeKimiK3Reasoning                        bool                    `json:"nativeKimiK3Reasoning"`
	ZaiReasoningEffortDialect                    bool                    `json:"zaiReasoningEffortDialect"`
	MistralReasoningContentParts                 bool                    `json:"mistralReasoningContentParts"`
	RequiresReasoningContentForToolCalls         bool                    `json:"requiresReasoningContentForToolCalls"`
	RequiresReasoningContentForAllAssistantTurns bool                    `json:"requiresReasoningContentForAllAssistantTurns"`
	AllowsSyntheticReasoningContentForToolCalls  bool                    `json:"allowsSyntheticReasoningContentForToolCalls"`
	ReplayReasoningContent                       bool                    `json:"replayReasoningContent"`
	QwenPreserveThinking                         bool                    `json:"qwenPreserveThinking"`
	QwenTemplateReasoningEffort                  bool                    `json:"qwenTemplateReasoningEffort"`
	RequiresThinkingAsText                       bool                    `json:"requiresThinkingAsText"`
	RequiresMistralToolIDs                       bool                    `json:"requiresMistralToolIds"`
	RequiresToolResultName                       bool                    `json:"requiresToolResultName"`
	RequiresAssistantAfterToolResult             bool                    `json:"requiresAssistantAfterToolResult"`
	RequiresAssistantContentForToolCalls         bool                    `json:"requiresAssistantContentForToolCalls"`
	UsesOpenAIToolCallIDLimit                    bool                    `json:"usesOpenAIToolCallIdLimit"`
	StripDeepseekSpecialTokens                   bool                    `json:"stripDeepseekSpecialTokens"`
	StripImageInput                              bool                    `json:"stripImageInput"`
	ReasoningDeltasMayBeCumulative               bool                    `json:"reasoningDeltasMayBeCumulative"`
	EmptyLengthFinishIsContextError              bool                    `json:"emptyLengthFinishIsContextError"`
	RejectRootObjectUnion                        bool                    `json:"rejectRootObjectUnion"`
	IsOpenRouterHost                             bool                    `json:"isOpenRouterHost"`
	IsVercelGatewayHost                          bool                    `json:"isVercelGatewayHost"`
	CacheControlFormat                           string                  `json:"cacheControlFormat,omitempty"`
	OpenRouterRouting                            *NeoPiRouting           `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting                         *NeoPiVercelRouting     `json:"vercelGatewayRouting,omitempty"`
	WhenThinking                                 *NeoPiCompletionsCompat `json:"whenThinking,omitempty"`
}

// NeoPiRouting is OpenRouter's provider routing preference: the upstreams a
// request may use, and the order to try them in.
type NeoPiRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

// NeoPiVercelRouting is Vercel AI Gateway's provider routing preference.
type NeoPiVercelRouting struct {
	Only             []string `json:"only,omitempty"`
	Order            []string `json:"order,omitempty"`
	Caching          string   `json:"caching,omitempty"`
	CacheTtl         string   `json:"cacheTtl,omitempty"`
	CacheAnchorItems *int     `json:"cacheAnchorItems,omitempty"`
}

// Clone deep-copies the compat so readers cannot alias its maps.
func (c *NeoPiCompletionsCompat) Clone() *NeoPiCompletionsCompat {
	if c == nil {
		return nil
	}
	out := *c
	if c.StreamFirstEventTimeoutMs != nil {
		timeout := *c.StreamFirstEventTimeoutMs
		out.StreamFirstEventTimeoutMs = &timeout
	}
	out.UnsupportedSamplingParams = slices.Clone(c.UnsupportedSamplingParams)
	if c.TrustExplicitThinkingOnly != nil {
		value := *c.TrustExplicitThinkingOnly
		out.TrustExplicitThinkingOnly = &value
	}
	if c.SupportsPromptCacheBreakpoints != nil {
		value := *c.SupportsPromptCacheBreakpoints
		out.SupportsPromptCacheBreakpoints = &value
	}
	if c.StripThinkingHistory != nil {
		value := *c.StripThinkingHistory
		out.StripThinkingHistory = &value
	}
	if c.OfficialEndpoint != nil {
		value := *c.OfficialEndpoint
		out.OfficialEndpoint = &value
	}
	out.ReasoningEffortMap = maps.Clone(c.ReasoningEffortMap)
	out.ExtraBody = maps.Clone(c.ExtraBody)
	out.WhenThinking = c.WhenThinking.Clone()
	if c.OpenRouterRouting != nil {
		routing := *c.OpenRouterRouting
		routing.Only, routing.Order = slices.Clone(c.OpenRouterRouting.Only), slices.Clone(c.OpenRouterRouting.Order)
		out.OpenRouterRouting = &routing
	}
	if c.VercelGatewayRouting != nil {
		routing := *c.VercelGatewayRouting
		routing.Only, routing.Order = slices.Clone(c.VercelGatewayRouting.Only), slices.Clone(c.VercelGatewayRouting.Order)
		out.VercelGatewayRouting = &routing
	}
	return &out
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
	// SessionHeader carries the conversation id (applySessionHeader).
	SessionHeader string `json:"sessionHeader,omitempty"`
	// StreamIdleTimeoutMs and StreamFirstEventTimeoutMs decide the
	// pre-response deadline NeoPi announces in X-Stainless-Timeout; nil is
	// NeoPi's undefined (the 300 s default), distinct from an explicit 0.
	StreamIdleTimeoutMs       *int `json:"streamIdleTimeoutMs,omitempty"`
	StreamFirstEventTimeoutMs *int `json:"streamFirstEventTimeoutMs,omitempty"`
}

// Clone deep-copies the record so registry readers cannot alias its maps.
func (m *NeoPiModel) Clone() *NeoPiModel {
	if m == nil {
		return nil
	}
	out := *m
	out.Input = slices.Clone(m.Input)
	out.Thinking = m.Thinking.Clone()
	if m.Compat != nil {
		compat := *m.Compat
		compat.ReasoningEffortMap = maps.Clone(m.Compat.ReasoningEffortMap)
		for _, timeout := range []**int{&compat.StreamIdleTimeoutMs, &compat.StreamFirstEventTimeoutMs} {
			if *timeout != nil {
				value := **timeout
				*timeout = &value
			}
		}
		out.Compat = &compat
	}
	if m.AnthropicCompat != nil {
		compat := *m.AnthropicCompat
		out.AnthropicCompat = &compat
	}
	out.CompletionsCompat = m.CompletionsCompat.Clone()
	if m.GoogleCompat != nil {
		compat := *m.GoogleCompat
		out.GoogleCompat = &compat
	}
	if m.DevinCompat != nil {
		compat := *m.DevinCompat
		out.DevinCompat = &compat
	}
	if m.Cursor != nil {
		spec := (&neopicatalog.Spec{CursorMaxMode: m.Cursor.MaxMode, CursorMaxModeRoutes: m.Cursor.MaxModeRoutes,
			CursorModelParameters: m.Cursor.ModelParameters, CursorModelRoutes: m.Cursor.ModelRoutes}).Clone()
		out.Cursor = &NeoPiCursor{MaxMode: spec.CursorMaxMode, MaxModeRoutes: spec.CursorMaxModeRoutes, ModelParameters: spec.CursorModelParameters,
			ModelRoutes: spec.CursorModelRoutes, RequiresToolSchemaProjection: m.Cursor.RequiresToolSchemaProjection}
	}
	if m.FactoryDroid != nil {
		scope := *m.FactoryDroid
		scope.APIProviders = slices.Clone(m.FactoryDroid.APIProviders)
		out.FactoryDroid = &scope
	}
	if m.OllamaCompat != nil {
		compat := *m.OllamaCompat
		out.OllamaCompat = &compat
	}
	out.CompatConfig = maps.Clone(m.CompatConfig)
	return &out
}

// Clone deep-copies the effort ladder.
func (t *NeoPiThinking) Clone() *NeoPiThinking {
	if t == nil {
		return nil
	}
	out := *t
	out.Efforts = slices.Clone(t.Efforts)
	out.EffortMap = maps.Clone(t.EffortMap)
	out.EffortRouting = maps.Clone(t.EffortRouting)
	out.EffortBudgets = maps.Clone(t.EffortBudgets)
	if t.SupportsDisplay != nil {
		display := *t.SupportsDisplay
		out.SupportsDisplay = &display
	}
	return &out
}
