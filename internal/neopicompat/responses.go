package neopicompat

// resolveResponsesPolicy is resolveOpenAIResponsesPolicy for an
// openai-responses spec (the buildOpenAIResponsesCompat baseline): the
// host-detected record, the resolved wire axes, the spec's own overrides with
// the effort map overlaid, then the xAI and reasoning-off fixups.
func resolveResponsesPolicy(spec Spec, facts identityFacts, a axes) map[string]any {
	baseURL, provider := spec.BaseURL, spec.Provider
	backend := spec.ProviderType
	if backend == "" {
		backend = provider
	}
	isAzure := modelMatchesHost(provider, baseURL, "azureOpenAI")
	isOpenRouter := modelMatchesHost(provider, baseURL, "openrouter")
	isOpenAIURL := hostMatchesURL(baseURL, "openai")
	isVercelGateway := modelMatchesHost(provider, baseURL, "vercelAIGateway")
	isXaiHost := modelMatchesHost(provider, baseURL, "xai")
	supportsPromptCacheBreakpoints := isOfficialOpenAIEndpoint(provider, baseURL) && facts.is("openai") && facts.revGte("5.6")
	thinkingFormat := "openai"
	if isOpenRouter {
		thinkingFormat = "openrouter"
	}
	reasoningCapable := compatReasoning(spec, a)
	isLocalServingBackend := (backend != "litellm" && localOpenAICompatProviders[backend]) || hasLocalLoopbackBaseURL(baseURL)
	isAnthropicModel := facts.is("anthropic")
	isDeepseekFamily := facts.is("deepseek")

	var promptCacheBreakpointTTL, cacheControlFormat, toolSchemaFlavor, promptCacheSessionHeader any
	if supportsPromptCacheBreakpoints {
		promptCacheBreakpointTTL = "30m"
	}
	if isOpenRouter && isAnthropicModel {
		cacheControlFormat = "anthropic"
	}
	if facts.is("kimi") {
		toolSchemaFlavor = "moonshot-mfjs"
	}
	if hostMatchesURL(baseURL, "xai") {
		promptCacheSessionHeader = "x-grok-conv-id"
	}
	wireModelIDMode := "raw"
	if isOpenRouter {
		wireModelIDMode = "openrouter"
	}
	streamFirstEvent, streamIdle := spec.Compat["streamFirstEventTimeoutMs"], spec.Compat["streamIdleTimeoutMs"]
	if isLocalServingBackend {
		streamFirstEvent, streamIdle = 0, 300_000
	}

	compat := map[string]any{
		"supportsDeveloperRole": isAzure || isOpenAIURL || hostMatchesURL(baseURL, "githubCopilot"),
		"supportsStrictMode": isAzure || hostMatchesURL(baseURL, "openai") || hostMatchesURL(baseURL, "azureOpenAI") ||
			hostMatchesURL(baseURL, "cerebras") || hostMatchesURL(baseURL, "together") || hostMatchesURL(baseURL, "openrouter") ||
			hostMatchesURL(baseURL, "deepseekFamily"),
		"supportsReasoningEffort":          !isXaiHost,
		"trustExplicitThinkingOnly":        nil,
		"supportsLongPromptCacheRetention": isOpenAIURL,
		"supportsPromptCacheBreakpoints":   supportsPromptCacheBreakpoints,
		"promptCacheBreakpointTtl":         promptCacheBreakpointTTL,
		"strictResponsesPairing":           hostMatchesURL(baseURL, "azureOpenAI"),
		"supportsImageDetailOriginal": isOpenAIURL || hostMatchesURL(baseURL, "azureOpenAI") || (isAzure && baseURL == "") ||
			hostMatchesURL(baseURL, "openaiCodex"),
		"supportsReasoningSummary":                     !isXaiHost,
		"statefulResponses":                            nil,
		"supportsAllTurnsReasoningContext":             false,
		"supportsConfigurationUpdate":                  false,
		"supportsSteering":                             false,
		"requiresReasoningOffJuiceInstruction":         false,
		"stripImageInput":                              false,
		"thinkingLoopGuard":                            nil,
		"reasoningEffortMap":                           map[string]any{},
		"supportsReasoningParams":                      true,
		"supportsSamplingParams":                       true,
		"supportsPenaltyAndStopParams":                 !isXaiHost,
		"thinkingFormat":                               thinkingFormat,
		"reasoningDisableMode":                         resolveReasoningDisableMode(thinkingFormat),
		"omitReasoningEffort":                          false,
		"includeEncryptedReasoning":                    true,
		"filterReasoningHistory":                       isOpenRouter && isAnthropicModel,
		"disableReasoningOnForcedToolChoice":           facts.is("kimi"),
		"disableReasoningOnToolChoice":                 isDeepseekFamily && reasoningCapable && !isOpenRouter,
		"disableReasoningWithTools":                    false,
		"supportsToolChoice":                           true,
		"supportsForcedToolChoice":                     true,
		"supportsNamedToolChoice":                      true,
		"reasoningContentField":                        "reasoning_content",
		"requiresReasoningContentForToolCalls":         (facts.is("kimi") || (isDeepseekFamily && reasoningCapable) || (isOpenRouter && reasoningCapable)) && reasoningCapable,
		"requiresReasoningContentForAllAssistantTurns": isDeepseekFamily && reasoningCapable && !isOpenRouter,
		"allowsSyntheticReasoningContentForToolCalls":  !isDeepseekFamily || !reasoningCapable,
		"syntheticReasoningContentFallback":            nil,
		"replayReasoningContent":                       false,
		"qwenPreserveThinking":                         false,
		"qwenTemplateReasoningEffort":                  false,
		"requiresThinkingAsText":                       false,
		"requiresMistralToolIds":                       false,
		"requiresToolResultName":                       false,
		"requiresAssistantAfterToolResult":             false,
		"requiresAssistantContentForToolCalls":         facts.is("kimi"),
		"openRouterRouting":                            nil,
		"vercelGatewayRouting":                         nil,
		"isOpenRouterHost":                             isOpenRouter,
		"isVercelGatewayHost":                          isVercelGateway,
		"wireModelIdMode":                              wireModelIDMode,
		"toolSchemaFlavor":                             toolSchemaFlavor,
		"alwaysSendMaxTokens":                          facts.is("kimi"),
		"clampOutputToModelMax":                        backend != "litellm" && (localOpenAICompatProviders[backend] || hasLocalLoopbackBaseURL(baseURL)),
		"supportsObfuscationOptOut":                    isOpenAIURL || provider == "openai",
		"storeResponses":                               false,
		"connectionBoundNativeHistory":                 false,
		// Only openai-codex-responses also counts its Codex backend URL.
		"officialEndpoint":                 isOfficialOpenAIEndpoint(provider, baseURL),
		"harmonyLeakMitigation":            false,
		"rejectRootObjectUnion":            false,
		"retryWithoutStrictOnGrammarError": false,
		"cacheControlFormat":               cacheControlFormat,
		"stripDeepseekSpecialTokens":       false,
		"streamMarkupHealingPattern":       detectStreamMarkupHealing(provider, facts, baseURL),
		"reasoningDeltasMayBeCumulative":   false,
		"emptyLengthFinishIsContextError":  false,
		"usesOpenAIToolCallIdLimit":        false,
		"promptCacheSessionHeader":         promptCacheSessionHeader,
		"streamFirstEventTimeoutMs":        streamFirstEvent,
		"streamIdleTimeoutMs":              streamIdle,
	}
	applyWireAxes(compat, a.wire, APIOpenAIResponses)
	applyCompatOverrides(compat, spec.Compat)
	// overlayEffortMapAxis: spec effort keys overlay the cascade's map.
	if axisMap := effortRecord(a.wire["reasoningEffortMap"]); axisMap != nil {
		if specMap, ok := spec.Compat["reasoningEffortMap"].(map[string]any); ok && specMap != nil {
			merged := map[string]any{}
			for key, value := range axisMap {
				merged[key] = value
			}
			for key, value := range specMap {
				merged[key] = value
			}
			compat["reasoningEffortMap"] = merged
		}
	}
	if _, hasAxis := a.wire["reasoningEffortMap"]; isXaiHost && hasAxis {
		// The rules carry the canonical first-party xAI map: re-assert it over
		// stale overrides and drop clamp keys it no longer declares.
		canonical := effortRecord(a.wire["reasoningEffortMap"])
		merged := map[string]any{}
		if current, ok := compat["reasoningEffortMap"].(map[string]any); ok {
			for key, value := range current {
				merged[key] = value
			}
		}
		for key, value := range canonical {
			merged[key] = value
		}
		for _, key := range []string{"xhigh", "max"} {
			if _, declared := canonical[key]; !declared {
				delete(merged, key)
			}
		}
		compat["reasoningEffortMap"] = merged
	}
	_, specDisable := spec.Compat["reasoningDisableMode"]
	_, axisDisable := a.wire["reasoningDisableMode"]
	if !specDisable && !axisDisable {
		compat["reasoningDisableMode"] = resolveReasoningDisableMode(compat["thinkingFormat"])
	}
	_, specOmit := spec.Compat["omitReasoningEffort"]
	_, axisOmit := a.wire["omitReasoningEffort"]
	if supports, _ := compat["supportsReasoningEffort"].(bool); !specOmit && !axisOmit && !supports {
		compat["omitReasoningEffort"] = true
	}
	if axisEffort, _ := a.wire["supportsReasoningEffort"].(bool); provider == "xai-oauth" && axisEffort {
		// Stale rows predating a SKU's effort-capable rule still carry
		// omitReasoningEffort; the rules are the live wire contract.
		if specEffort, set := spec.Compat["supportsReasoningEffort"].(bool); !set || specEffort {
			compat["supportsReasoningEffort"] = true
			compat["omitReasoningEffort"] = false
		}
	}
	return compat
}
