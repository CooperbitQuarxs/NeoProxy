package neopicompat

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
)

// The NeoPi APIs whose model policy this package resolves.
const (
	APIOpenAICompletions = "openai-completions"
	APIAnthropicMessages = "anthropic-messages"
	APIGoogleVertex      = "google-vertex"
)

// Spec is the part of a NeoPi ModelSpec the policy resolves from. Thinking
// and Compat are the spec's own (sparse, JSON-shaped) authored values: a key
// present with a nil value is an explicit JSON null, and NeoPi's undefined is
// an absent key, which is how JSON decoding presents them.
type Spec struct {
	Provider     string
	ProviderType string
	API          string
	ID           string
	BaseURL      string
	Reasoning    bool
	Thinking     map[string]any
	Compat       map[string]any
}

// Policy is the resolved policy of one spec (ResolvedModelPolicy) and the
// reasoning flag buildModel materializes from it.
type Policy struct {
	// Compat is the complete resolved compat record; keys NeoPi leaves
	// undefined hold nil.
	Compat map[string]any
	// Thinking is the resolved thinking config, nil when the model has none.
	Thinking map[string]any
	Catalog  map[string]any
	// Reasoning is spec.reasoning || thinking !== undefined.
	Reasoning bool
}

// Resolve is resolveModelPolicy for an openai-completions, anthropic-messages
// or google-vertex spec, plus buildModel's reasoning materialization.
func Resolve(spec Spec) (Policy, error) {
	if err := load(); err != nil {
		return Policy{}, err
	}
	if spec.API != APIOpenAICompletions && spec.API != APIAnthropicMessages && spec.API != APIGoogleVertex {
		return Policy{}, fmt.Errorf("neopicompat: api %q is not ported", spec.API)
	}
	// withoutUnverifiedThinking: an unverified ladder was derived, not authored.
	if unverified, _ := spec.Thinking["unverified"].(bool); unverified {
		spec.Thinking = nil
		spec.Reasoning = false
	}
	identity := neopitaxonomy.Classify(spec.Provider, spec.ID)
	facts := identityFacts{identity}
	providerType := spec.ProviderType
	if providerType == "" {
		providerType = spec.Provider
	}
	t := target{provider: providerType, api: spec.API, class: identity.Class, family: identity.Family,
		revision: identity.Revision, hasRevision: identity.HasRevision, model: spec.ID, reasoning: spec.Reasoning}
	resolved, err := resolveCascade(t)
	if err != nil {
		return Policy{}, err
	}
	var compat map[string]any
	switch spec.API {
	case APIAnthropicMessages:
		compat = resolveAnthropicPolicy(spec, facts, resolved)
	case APIGoogleVertex:
		compat = resolveGooglePolicy(spec, resolved)
	default:
		compat = resolveCompletionsPolicy(spec, facts, resolved)
	}
	thinking, err := resolveThinkingPolicy(spec, facts, resolved, compat)
	if err != nil {
		return Policy{}, err
	}
	return Policy{Compat: compat, Thinking: thinking, Catalog: resolved.catalog, Reasoning: spec.Reasoning || thinking != nil}, nil
}

// resolveGooglePolicy is resolveGooglePolicy: the Google wire defaults, the
// resolved wire axes, then the spec's overrides. The image contract keys are
// declared but left undefined (nil).
func resolveGooglePolicy(spec Spec, a axes) map[string]any {
	compat := map[string]any{
		"supportsFunctionPartId":                          false,
		"requiresSkipThoughtSignature":                    false,
		"requiresSkipThoughtSignatureOnFirstFunctionCall": false,
		"dropUnsignedThinking":                            false,
		"ccaLegacyParametersSchema":                       false,
		"multimodalFunctionResponse":                      false,
		"flashStreamLeakWorkaround":                       false,
		"claudeThinkingBetaHeader":                        false,
		"antigravityClaudeToolMode":                       false,
		"stripImageInput":                                 false,
		"maxImageDimension":                               nil,
		"maxImagePayloadBytes":                            nil,
	}
	applyWireAxes(compat, a.wire, spec.API)
	applyCompatOverrides(compat, spec.Compat)
	return compat
}

// identityFacts is IdentityFacts.
type identityFacts struct{ identity neopitaxonomy.Identity }

func (f identityFacts) is(class string) bool { return f.identity.Class == class }

func (f identityFacts) family(families ...string) bool {
	return f.identity.Family != "" && slices.Contains(families, f.identity.Family)
}

func (f identityFacts) revGte(floor string) bool {
	if !f.identity.HasRevision {
		return false
	}
	bound, ok := parseRevision(floor)
	return ok && f.identity.Revision.Compare(bound) >= 0
}

// kimiMandatoryThinking: Kimi families whose API rejects disabled thinking.
func (f identityFacts) kimiMandatoryThinking() bool {
	return f.is("kimi") && f.family("k2.7-code", "k3")
}

// anthropicAdaptiveGenAtLeast is the adaptive-thinking Claude generation floor
// (Opus >= opusMin; Sonnet/Fable/Mythos >= 5; Haiku >= 5.5).
func (f identityFacts) anthropicAdaptiveGenAtLeast(opusMin string) bool {
	if !f.is("anthropic") {
		return false
	}
	switch {
	case f.family("opus"):
		return f.revGte(opusMin)
	case f.family("sonnet", "fable", "mythos"):
		return f.revGte("5")
	case f.family("haiku"):
		return f.revGte("5.5")
	}
	return false
}

// hostMatchesURL is hosts.ts hostMatchesUrl: a case-insensitive substring
// match of the host class's URL markers.
func hostMatchesURL(baseURL, host string) bool {
	if baseURL == "" {
		return false
	}
	lower := strings.ToLower(baseURL)
	for _, marker := range data.Hosts[host].URLMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// modelMatchesHost is hosts.ts modelMatchesHost: the provider id, a
// provider-id prefix, or a URL marker.
func modelMatchesHost(provider, baseURL, host string) bool {
	spec := data.Hosts[host]
	if slices.Contains(spec.Providers, provider) {
		return true
	}
	for _, prefix := range spec.ProviderPrefixes {
		if strings.HasPrefix(provider, prefix) {
			return true
		}
	}
	return hostMatchesURL(baseURL, host)
}

var privateRange172 = regexp.MustCompile(`^172\.(1[6-9]|2[0-9]|3[01])\.`)

// whatwgSpecialSchemes are the WHATWG URL special schemes, whose hosts are
// normalized (lowercased, IPv4 numbers canonicalized).
var whatwgSpecialSchemes = map[string]bool{"http": true, "https": true, "ws": true, "wss": true, "ftp": true, "file": true}

// urlHostname is `new URL(baseURL).hostname`: false where the WHATWG parser
// throws (a relative URL, an invalid IPv4 number). Special-scheme hosts are
// lowercased and numeric IPv4 forms (127.1, 2130706433, 0x7f.1) canonicalized;
// IPv6 literals keep their brackets in compressed form.
func urlHostname(baseURL string) (string, bool) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Opaque != "" {
		return "", false
	}
	hostname := parsed.Hostname()
	if !whatwgSpecialSchemes[strings.ToLower(parsed.Scheme)] {
		return hostname, true
	}
	if hostname == "" && parsed.Scheme != "file" {
		return "", false
	}
	if strings.Contains(hostname, ":") {
		ip := net.ParseIP(hostname)
		if ip == nil {
			return "", false
		}
		return "[" + ip.String() + "]", true
	}
	hostname = strings.ToLower(hostname)
	if !endsInNumber(hostname) {
		return hostname, true
	}
	return parseWHATWGIPv4(hostname)
}

// endsInNumber is the WHATWG ends-in-a-number checker.
func endsInNumber(host string) bool {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" {
		if len(parts) == 1 {
			return false
		}
		parts = parts[:len(parts)-1]
	}
	last := parts[len(parts)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	if len(last) >= 2 && (last[:2] == "0x" || last[:2] == "0X") {
		_, err := strconv.ParseUint("0"+last[2:], 16, 64)
		return last[2:] == "" || err == nil
	}
	return false
}

// parseWHATWGIPv4 is the WHATWG IPv4 parser, returning the dotted form.
func parseWHATWGIPv4(host string) (string, bool) {
	parts := strings.Split(host, ".")
	if parts[len(parts)-1] == "" && len(parts) > 1 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 4 {
		return "", false
	}
	numbers := make([]uint64, len(parts))
	for i, part := range parts {
		if part == "" {
			return "", false
		}
		base, digits := 10, part
		switch {
		case len(part) >= 2 && (part[:2] == "0x" || part[:2] == "0X"):
			base, digits = 16, part[2:]
		case len(part) >= 2 && part[0] == '0':
			base, digits = 8, part[1:]
		}
		if digits == "" {
			numbers[i] = 0
			continue
		}
		value, err := strconv.ParseUint(digits, base, 64)
		if err != nil {
			return "", false
		}
		numbers[i] = value
	}
	for _, value := range numbers[:len(numbers)-1] {
		if value > 255 {
			return "", false
		}
	}
	last := numbers[len(numbers)-1]
	if last >= 1<<(8*(5-len(numbers))) {
		return "", false
	}
	ipv4 := last
	for i, value := range numbers[:len(numbers)-1] {
		ipv4 += value << (8 * (3 - i))
	}
	return fmt.Sprintf("%d.%d.%d.%d", ipv4>>24, ipv4>>16&0xff, ipv4>>8&0xff, ipv4&0xff), true
}

// hasLocalLoopbackBaseURL is hasLocalLoopbackBaseUrl.
func hasLocalLoopbackBaseURL(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	hostname, ok := urlHostname(baseURL)
	if !ok {
		return false
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	switch hostname {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]":
		return true
	}
	return strings.HasPrefix(hostname, "10.") || strings.HasPrefix(hostname, "192.168.") ||
		privateRange172.MatchString(hostname) || strings.HasSuffix(hostname, ".local")
}

func resolveReasoningDisableMode(thinkingFormat any) string {
	switch thinkingFormat {
	case "openrouter":
		return "openrouter-enabled-false"
	case "zai", "kimi":
		return "zai-thinking-disabled"
	case "qwen":
		return "qwen-enable-thinking-false"
	case "qwen-chat-template":
		return "qwen-template-false"
	case "chat-template":
		return "chat-template-thinking-false"
	}
	return "lowest-effort"
}

func isOfficialOpenAIEndpoint(provider, baseURL string) bool {
	if provider != "openai" {
		return false
	}
	if baseURL == "" {
		return true
	}
	hostname, ok := urlHostname(baseURL)
	return ok && hostname == "api.openai.com"
}

var localOpenAICompatProviders = map[string]bool{"llama.cpp": true, "lm-studio": true, "vllm": true, "ollama": true}

// detection is OpenAIDetection.
type detection struct {
	isZai, isZhipu, isMoonshotNative, isOpenCodeHost, isOpenCodeProvider    bool
	isDirectDeepseekAPI, isDeepseekReasoning, isDirectDeepseekReasoning     bool
	isVenice, requiresEnabledThinking, isXiaomiMimo, isLocalOpenAICompatBkd bool
	isLocalServingBackend, isOpenRouter                                     bool
}

func detectOpenAI(spec Spec, facts identityFacts, reasoningCapable bool) detection {
	provider, baseURL := spec.Provider, spec.BaseURL
	backend := spec.ProviderType
	if backend == "" {
		backend = provider
	}
	isDirectDeepseek := modelMatchesHost(provider, baseURL, "deepseekDirect")
	isDeepseekFamily := modelMatchesHost(provider, baseURL, "deepseekFamily") || facts.is("deepseek")
	isDeepseekReasoning := isDeepseekFamily && reasoningCapable
	isMoonshotNative := modelMatchesHost(provider, baseURL, "moonshotNative")
	isLocal := backend != "litellm" && (localOpenAICompatProviders[backend] || hasLocalLoopbackBaseURL(baseURL))
	return detection{
		isZai:                     modelMatchesHost(provider, baseURL, "zai"),
		isZhipu:                   modelMatchesHost(provider, baseURL, "zhipu"),
		isMoonshotNative:          isMoonshotNative,
		isOpenCodeHost:            modelMatchesHost(provider, baseURL, "opencode"),
		isOpenCodeProvider:        provider == "opencode-go" || provider == "opencode-zen",
		isDirectDeepseekAPI:       isDirectDeepseek,
		isDeepseekReasoning:       isDeepseekReasoning,
		isDirectDeepseekReasoning: isDirectDeepseek && isDeepseekReasoning,
		isVenice:                  modelMatchesHost(provider, baseURL, "venice"),
		requiresEnabledThinking:   isMoonshotNative && facts.is("kimi") && facts.family("k2.7-code"),
		isXiaomiMimo:              modelMatchesHost(provider, baseURL, "xiaomi") && facts.is("mimo"),
		isLocalOpenAICompatBkd:    isLocal,
		isLocalServingBackend:     isLocal || hasLocalLoopbackBaseURL(baseURL),
		isOpenRouter:              modelMatchesHost(provider, baseURL, "openrouter"),
	}
}

// orNil is a TS `cond ? value : undefined`.
func orNil(cond bool, value any) any {
	if cond {
		return value
	}
	return nil
}

// detectOpenAICompat is the detected chat-completions baseline.
func detectOpenAICompat(spec Spec, d detection, facts identityFacts, reasoningCapable bool) map[string]any {
	provider, baseURL := spec.Provider, spec.BaseURL
	host := func(name string) bool { return modelMatchesHost(provider, baseURL, name) }
	urlHost := func(name string) bool { return hostMatchesURL(baseURL, name) }
	isCerebras := host("cerebras")
	isCerebrasHost := urlHost("cerebras")
	isKilo := host("kilo")
	isAlibaba := host("alibabaDashscope")
	isXiaomiHost := host("xiaomi")
	isGrok := host("xai")
	isMistral := host("mistral")
	isGoogleAistudio := urlHost("googleAistudio")
	isOpenAIHost := host("openai")
	isAzureHost := host("azureOpenAI")
	isVercelGateway := host("vercelAIGateway")
	isTogether := host("together")
	isFireworks := urlHost("fireworks")
	isGroqHost := host("groq")
	isMiniMaxHost := host("minimax")
	isQwenPortal := host("qwenPortal")
	isMoonshotKimi := facts.is("kimi") && d.isMoonshotNative
	isMoonshotKimiK3 := isMoonshotKimi && facts.family("k3")
	preservedThinking := isMoonshotKimi && facts.family("k2.6")
	isAnthropicModel := host("anthropic") || facts.is("anthropic")
	isQwen := facts.is("qwen")
	isDeepseekFamily := host("deepseekFamily") || facts.is("deepseek")
	supportsZaiReasoningEffort := (d.isZai || d.isZhipu) && facts.is("glm") && facts.revGte("5.2")

	isNonStandard := isCerebras || isGrok || isMistral || isGoogleAistudio || urlHost("chutes") || urlHost("deepseekFamily") ||
		isFireworks || isAlibaba || d.isZai || d.isZhipu || isKilo || isQwen || isXiaomiHost || d.isMoonshotNative || d.isOpenCodeHost
	useMaxTokens := isMistral || d.isMoonshotNative || d.isZai || d.isZhipu || urlHost("chutes") || isFireworks || d.isDirectDeepseekAPI
	cacheBreakpoints := isOfficialOpenAIEndpoint(provider, baseURL) && facts.is("openai") && facts.revGte("5.6")
	multipleSystem := !isMiniMaxHost && !isAlibaba && !isQwenPortal && !isQwen &&
		(isOpenAIHost || isAzureHost || d.isOpenRouter || isCerebras || isTogether || isFireworks || isGroqHost ||
			isDeepseekFamily || isMistral || isGrok || d.isZai || d.isZhipu || provider == "github-copilot" || provider == "zenmux")

	var streamIdle any
	switch {
	case facts.is("glm") && facts.revGte("5") && !facts.family("vision") && (urlHost("zai") || urlHost("zhipu") || urlHost("opencode")):
		streamIdle = 600_000
	case facts.is("mimo") && urlHost("xiaomi"):
		streamIdle = 300_000
	case reasoningCapable && facts.is("kimi") && (facts.family("k3") || facts.family("k2.7-code")) && urlHost("moonshotNative"):
		streamIdle = 300_000
	case reasoningCapable && facts.is("deepseek") && urlHost("deepseekDirect"):
		streamIdle = 300_000
	case d.isLocalServingBackend:
		streamIdle = 300_000
	}

	wireModelIDMode := "raw"
	if urlHost("openrouter") {
		wireModelIDMode = "openrouter"
	}
	var thinkingFormat string
	switch {
	case facts.is("kimi") && !facts.family("k3") && urlHost("moonshotNative") || urlHost("zai") || urlHost("zhipu") || facts.is("mimo") && urlHost("xiaomi"):
		thinkingFormat = "zai"
	case urlHost("openrouter"):
		thinkingFormat = "openrouter"
	case isQwen && urlHost("nvidia"):
		thinkingFormat = "qwen-chat-template"
	case isQwen && (isFireworks || urlHost("venice")):
		thinkingFormat = "openai"
	case urlHost("alibabaDashscope") || isQwen:
		thinkingFormat = "qwen"
	default:
		thinkingFormat = "openai"
	}
	reasoningDisableMode := resolveReasoningDisableMode(thinkingFormat)
	if d.isVenice {
		reasoningDisableMode = "venice-disable-thinking"
	}
	maxTokensField := "max_completion_tokens"
	if useMaxTokens {
		maxTokensField = "max_tokens"
	}
	toolStrictMode := "mixed"
	if isCerebrasHost {
		toolStrictMode = "all_strict"
	}
	var toolSchemaFlavor any
	switch {
	case d.isMoonshotNative || facts.is("kimi"):
		toolSchemaFlavor = "moonshot-mfjs"
	case d.isLocalOpenAICompatBkd:
		toolSchemaFlavor = "grammar"
	}
	official := isOfficialOpenAIEndpoint(provider, baseURL)
	return map[string]any{
		"supportsStore":                                !isNonStandard,
		"supportsDeveloperRole":                        isOpenAIHost || isAzureHost,
		"supportsMultipleSystemMessages":               multipleSystem,
		"supportsReasoningEffort":                      !isGrok && !d.isXiaomiMimo && (!(d.isZai || d.isZhipu) || supportsZaiReasoningEffort),
		"trustExplicitThinkingOnly":                    nil,
		"supportsReasoningParams":                      true,
		"supportsSamplingParams":                       true,
		"supportsPenaltyAndStopParams":                 !(isGrok && reasoningCapable),
		"reasoningEffortMap":                           map[string]any{},
		"supportsUsageInStreaming":                     !isCerebrasHost,
		"alwaysSendMaxTokens":                          facts.is("kimi"),
		"disableReasoningOnForcedToolChoice":           (facts.is("kimi") && !isMoonshotKimiK3) || isAnthropicModel,
		"disableReasoningOnToolChoice":                 isDeepseekFamily && reasoningCapable && !d.isOpenRouter,
		"disableReasoningWithTools":                    false,
		"supportsToolChoice":                           !d.isDirectDeepseekReasoning,
		"supportsForcedToolChoice":                     !d.requiresEnabledThinking && !(d.isOpenCodeHost && d.isDeepseekReasoning),
		"supportsNamedToolChoice":                      true,
		"maxTokensField":                               maxTokensField,
		"requiresToolResultName":                       isMistral,
		"requiresAssistantAfterToolResult":             isMistral,
		"requiresThinkingAsText":                       isMistral,
		"requiresMistralToolIds":                       isMistral,
		"thinkingFormat":                               thinkingFormat,
		"kimiApiFormat":                                nil,
		"reasoningDisableMode":                         reasoningDisableMode,
		"omitReasoningEffort":                          false,
		"includeEncryptedReasoning":                    true,
		"filterReasoningHistory":                       d.isOpenRouter && isAnthropicModel,
		"thinkingKeep":                                 orNil(preservedThinking, "all"),
		"reasoningContentField":                        "reasoning_content",
		"mistralReasoningContentParts":                 nil,
		"requiresReasoningContentForToolCalls":         (facts.is("kimi") && !d.isOpenCodeProvider) || (isDeepseekFamily && reasoningCapable) || d.isXiaomiMimo || (d.isOpenRouter && reasoningCapable),
		"requiresReasoningContentForAllAssistantTurns": ((isDeepseekFamily && reasoningCapable) || d.isXiaomiMimo) && !d.isOpenRouter,
		"allowsSyntheticReasoningContentForToolCalls":  (!isDeepseekFamily || !reasoningCapable) && !d.isXiaomiMimo,
		"syntheticReasoningContentFallback":            nil,
		"replayReasoningContent":                       d.isLocalOpenAICompatBkd,
		"qwenPreserveThinking":                         (thinkingFormat == "qwen" || thinkingFormat == "qwen-chat-template") && d.isLocalOpenAICompatBkd,
		"qwenTemplateReasoningEffort":                  false,
		"requiresAssistantContentForToolCalls":         facts.is("kimi") || d.isDirectDeepseekReasoning,
		"cacheControlFormat":                           orNil(d.isOpenRouter && isAnthropicModel, "anthropic"),
		"supportsPromptCacheBreakpoints":               cacheBreakpoints,
		"promptCacheBreakpointTtl":                     orNil(cacheBreakpoints, "30m"),
		"openRouterRouting":                            nil,
		"vercelGatewayRouting":                         nil,
		"isOpenRouterHost":                             d.isOpenRouter,
		"wireModelIdMode":                              wireModelIDMode,
		"isVercelGatewayHost":                          isVercelGateway,
		"supportsStrictMode": urlHost("openai") || urlHost("azureOpenAI") || urlHost("cerebras") || urlHost("together") ||
			urlHost("openrouter") || urlHost("deepseekFamily"),
		"extraBody":                        nil,
		"toolStrictMode":                   toolStrictMode,
		"toolSchemaFlavor":                 toolSchemaFlavor,
		"streamFirstEventTimeoutMs":        orNil(d.isLocalServingBackend, 0),
		"streamIdleTimeoutMs":              streamIdle,
		"stripDeepseekSpecialTokens":       false,
		"stripThinkingHistory":             nil,
		"officialEndpoint":                 orNil(official, true),
		"streamMarkupHealingPattern":       detectStreamMarkupHealing(provider, facts, baseURL),
		"reasoningDeltasMayBeCumulative":   false,
		"emptyLengthFinishIsContextError":  false,
		"usesOpenAIToolCallIdLimit":        false,
		"promptCacheSessionHeader":         orNil(urlHost("xai"), "x-grok-conv-id"),
		"dropThinkingWhenReasoningEffort":  false,
		"nativeKimiK3Reasoning":            false,
		"zaiReasoningEffortDialect":        false,
		"clampOutputToModelMax":            d.isLocalOpenAICompatBkd,
		"stripImageInput":                  false,
		"thinkingLoopGuard":                nil,
		"rejectRootObjectUnion":            false,
		"retryWithoutStrictOnGrammarError": false,
		"supportsPromptCacheKey":           false,
	}
}

// detectStreamMarkupHealing is the default leaked-markup healer.
func detectStreamMarkupHealing(provider string, facts identityFacts, baseURL string) any {
	isKimiK2 := facts.is("kimi") && strings.HasPrefix(facts.identity.Family, "k2")
	if provider == "kimi-code" || provider == "moonshot" || isKimiK2 {
		return "kimi"
	}
	if facts.is("deepseek") {
		return "dsml"
	}
	if isOfficialOpenAIEndpoint(provider, baseURL) {
		return nil
	}
	return "thinking"
}

// applyWireAxes assigns the resolved wire axes the API's compat record takes.
func applyWireAxes(compat map[string]any, wire map[string]any, api string) {
	keys := wireKeys[api]
	for key, value := range wire {
		if keys[key] {
			compat[key] = cloneValue(value)
		}
	}
}

// applyCompatOverrides is applyCompatOverrides: defined override values,
// explicit nulls included, onto keys the record declares.
func applyCompatOverrides(compat map[string]any, overrides map[string]any) {
	for key, value := range overrides {
		if _, declared := compat[key]; declared {
			compat[key] = cloneValue(value)
		}
	}
}

// defined is `record[key] !== undefined` for an authored JSON-shaped record.
func defined(record map[string]any, key string) bool {
	_, ok := record[key]
	return ok
}

func compatReasoning(spec Spec, a axes) bool {
	if spec.Reasoning {
		return true
	}
	upgrade, _ := a.thinking["upgradeNeutral"].(bool)
	return a.reasoning && upgrade
}

func resolveCompletionsPolicy(spec Spec, facts identityFacts, a axes) map[string]any {
	reasoningCapable := compatReasoning(spec, a)
	d := detectOpenAI(spec, facts, reasoningCapable)
	compat := detectOpenAICompat(spec, d, facts, reasoningCapable)
	applyWireAxes(compat, a.wire, APIOpenAICompletions)
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
	fixupOpenAICompat(spec, compat, d, a)
	return compat
}

// officialAnthropicURL is compat/anthropic.ts OFFICIAL_ANTHROPIC_URL.
const officialAnthropicURL = "https://api.anthropic.com"

var (
	cloudflareAnthropicGatewayMarker = regexp.MustCompile(`(?i)gateway\.ai\.cloudflare\.com/.+/anthropic(?:/|$)`)
	vertexAnthropicMarker            = regexp.MustCompile(`(?i)aiplatform\.googleapis\.com/.+/publishers/anthropic/`)
	bedrockAnthropicMarker           = regexp.MustCompile(`(?i)(?:^|//|\.)bedrock-runtime\.[a-z0-9-]+\.amazonaws\.com`)
	azureAnthropicMarker             = regexp.MustCompile(`(?i)(?:^|//|\.)[a-z0-9-]+\.(?:inference|services)\.ai\.azure\.com`)
	bedrockPublicHost                = regexp.MustCompile(`^(?:bedrock-runtime(?:-fips)?\.[a-z0-9-]+\.amazonaws\.com|bedrock-mantle\.(?:[a-z0-9-]+|\{region\})\.api\.aws)$`)
	bedrockPrivateLinkHost           = regexp.MustCompile(`^vpce-[a-z0-9-]+\.(?:bedrock-runtime(?:-fips)?|bedrock-mantle)\.[a-z0-9-]+\.vpce\.amazonaws\.com$`)
)

// isOfficialAnthropicAPIURL is isOfficialAnthropicApiUrl: no base URL, or
// the first-party API origin.
func isOfficialAnthropicAPIURL(baseURL string) bool {
	if baseURL == "" {
		return true
	}
	lower := strings.ToLower(baseURL)
	return lower == officialAnthropicURL || strings.HasPrefix(lower, officialAnthropicURL+"/")
}

// isBedrockAnthropicRoute is isBedrockRouteUrl(baseUrl, "anthropic"): an
// https Bedrock runtime/mantle (or PrivateLink) host under /anthropic.
func isBedrockAnthropicRoute(baseURL string) bool {
	if baseURL == "" {
		return false
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	hostname, ok := urlHostname(baseURL)
	if !ok || !(bedrockPublicHost.MatchString(hostname) || bedrockPrivateLinkHost.MatchString(hostname)) {
		return false
	}
	path := parsed.EscapedPath()
	return path == "/anthropic" || strings.HasPrefix(path, "/anthropic/")
}

// isAzureAnthropicRoute is isAzureAnthropicRoute: Azure AI Inference / Foundry.
func isAzureAnthropicRoute(baseURL string) bool {
	return baseURL != "" && azureAnthropicMarker.MatchString(baseURL)
}

// isAnthropicSigningProxyURL is isAnthropicSigningProxyUrl: non-official URLs
// that enforce Anthropic thinking signatures on replay.
func isAnthropicSigningProxyURL(baseURL string) bool {
	return hostMatchesURL(baseURL, "githubCopilot") || hostMatchesURL(baseURL, "zenmux") ||
		(baseURL != "" && (cloudflareAnthropicGatewayMarker.MatchString(baseURL) ||
			vertexAnthropicMarker.MatchString(baseURL) || bedrockAnthropicMarker.MatchString(baseURL))) ||
		isBedrockAnthropicRoute(baseURL) || isAzureAnthropicRoute(baseURL)
}

// resolveAnthropicPolicy is resolveAnthropicPolicy: the anthropic-messages
// compat record, then wire axes and the spec's own overrides.
func resolveAnthropicPolicy(spec Spec, facts identityFacts, a axes) map[string]any {
	provider, baseURL := spec.Provider, spec.BaseURL
	official := isOfficialAnthropicAPIURL(baseURL)
	isCopilot := modelMatchesHost(provider, baseURL, "githubCopilot")
	isZenmux := modelMatchesHost(provider, baseURL, "zenmux")
	requiresThinkingEnabled := modelMatchesHost(provider, baseURL, "moonshotNative") && facts.kimiMandatoryThinking()
	bedrockMessagesAPI := isBedrockAnthropicRoute(baseURL)
	// Both routes reject the top-level tool `strict` field.
	rejectsStrictTools := isAzureAnthropicRoute(baseURL) || bedrockMessagesAPI
	signingEndpoint := official || isCopilot || isZenmux || isAnthropicSigningProxyURL(baseURL)
	compat := map[string]any{
		"officialEndpoint":                   official,
		"signingEndpoint":                    signingEndpoint,
		"supportsContextManagement":          true,
		"supportsServerCompaction":           false,
		"firstPartyProvider":                 false,
		"honorsClaudeCodeBetaKillSwitch":     false,
		"supportsOutputEffort":               true,
		"disableStrictTools":                 rejectsStrictTools,
		"bedrockMessagesApi":                 orNil(bedrockMessagesAPI, true),
		"disableAdaptiveThinking":            false,
		"allowAnthropicHeaderOverrides":      false,
		"supportsEagerToolInputStreaming":    official,
		"supportsLongCacheRetention":         official,
		"supportsMidConversationSystem":      official && !facts.family("sonnet") && facts.anthropicAdaptiveGenAtLeast("4.8"),
		"supportsTurnScopedSystem":           false,
		"supportsMidConversationToolChanges": false,
		"supportsPerMessageEffort":           false,
		"supportsThinkingBindingControls":    false,
		"supportsBetweenToolsThinking":       false,
		"supportsForcedToolChoice":           !requiresThinkingEnabled && !facts.family("fable", "mythos"),
		"supportsSamplingParams":             true,
		"requiresToolResultId":               false,
		"requiresThinkingEnabled":            requiresThinkingEnabled,
		"requiresThinkingPresence":           false,
		"replayUnsignedThinking": !signingEndpoint &&
			(compatReasoning(spec, a) || modelMatchesHost(provider, baseURL, "deepseekFamily")),
		"escapeBuiltinToolNames":      false,
		"injectClaudeCodeInstruction": true,
		"stripImageInput":             false,
		"maxImageDimension":           nil,
		"maxImagePayloadBytes":        nil,
		"thinkingLoopGuard":           nil,
		"stripThinkingHistory":        nil,
		"fastMode":                    nil,
		"streamIdleTimeoutMs":         cloneValue(spec.Compat["streamIdleTimeoutMs"]),
	}
	applyWireAxes(compat, a.wire, APIAnthropicMessages)
	applyCompatOverrides(compat, spec.Compat)
	return compat
}

func fixupOpenAICompat(spec Spec, compat map[string]any, d detection, a axes) {
	if extra, ok := compat["extraBody"].(map[string]any); ok && d.isDirectDeepseekReasoning {
		if thinking, ok := extra["thinking"].(map[string]any); ok && thinking["type"] == "enabled" {
			trimmed := map[string]any{}
			for key, value := range extra {
				if key != "thinking" {
					trimmed[key] = value
				}
			}
			if len(trimmed) > 0 {
				compat["extraBody"] = trimmed
			} else {
				compat["extraBody"] = nil
			}
		}
	}
	if _, axisDisable := a.wire["reasoningDisableMode"]; !defined(spec.Compat, "reasoningDisableMode") && !axisDisable {
		switch {
		case d.requiresEnabledThinking:
			compat["reasoningDisableMode"] = "omit"
		case d.isDirectDeepseekReasoning:
			compat["reasoningDisableMode"] = "zai-thinking-disabled"
		case d.isVenice:
			compat["reasoningDisableMode"] = "venice-disable-thinking"
		default:
			compat["reasoningDisableMode"] = resolveReasoningDisableMode(compat["thinkingFormat"])
		}
	}
	_, axisOmit := a.wire["omitReasoningEffort"]
	if !defined(spec.Compat, "omitReasoningEffort") && !axisOmit && compat["supportsReasoningEffort"] != true {
		compat["omitReasoningEffort"] = true
	}

	var policy map[string]any
	if specWhen, ok := spec.Compat["whenThinking"].(map[string]any); ok {
		policy = specWhen
	} else if axisWhen, ok := a.wire["whenThinking"].(map[string]any); ok && compatReasoning(spec, a) {
		policy = axisWhen
	} else if d.isDirectDeepseekReasoning {
		extra := map[string]any{}
		if current, ok := compat["extraBody"].(map[string]any); ok {
			for key, value := range current {
				extra[key] = value
			}
		}
		extra["thinking"] = map[string]any{"type": "enabled"}
		policy = map[string]any{"extraBody": extra}
	}
	if policy == nil {
		compat["whenThinking"] = nil
		return
	}
	variant := make(map[string]any, len(compat))
	for key, value := range compat {
		variant[key] = cloneValue(value)
	}
	variant["whenThinking"] = nil
	applyCompatOverrides(variant, policy)
	if !defined(policy, "reasoningDisableMode") {
		if d.isVenice {
			variant["reasoningDisableMode"] = "venice-disable-thinking"
		} else {
			variant["reasoningDisableMode"] = resolveReasoningDisableMode(variant["thinkingFormat"])
		}
	}
	if !defined(policy, "omitReasoningEffort") && variant["supportsReasoningEffort"] != true {
		variant["omitReasoningEffort"] = true
	}
	compat["whenThinking"] = variant
}
