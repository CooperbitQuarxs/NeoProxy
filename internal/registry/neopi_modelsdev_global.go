package registry

import (
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
	"github.com/tidwall/gjson"
)

// NeoPi's global models.dev reference map (provider-models/openai-compat.ts
// loadModelsDevReferences): mapModelsDevToModels over every provider
// descriptor (MODELS_DEV_PROVIDER_DESCRIPTORS), keyed by id by
// createModelsDevReferenceMap. Managers that hydrate ids no provider bundles
// (LiteLLM) resolve against it first, then against the global bundled
// references.

// neoPiModelsDevDescriptor is a ModelsDevProviderDescriptor: the models.dev
// key it reads, the provider and transport its rows take, its limit
// fallbacks (0 is null) and authored compat, its filter (nil: keep rows
// whose tool_call is true), per-model transport (nil: the descriptor's) and
// transform (nil: unchanged).
type neoPiModelsDevDescriptor struct {
	key, provider, api, baseURL string
	defaultContext, defaultMax  int
	compat                      map[string]any
	filter                      func(id string, raw gjson.Result) bool
	resolveAPI                  func(id string, raw gjson.Result) (api, baseURL string)
	transform                   func(row *NeoPiModel, id string, raw gjson.Result) []*NeoPiModel
}

const (
	neoPiVertexBaseURL          = "https://{location}-aiplatform.googleapis.com"
	neoPiVertexOpenAIBaseURL    = "https://{location}-aiplatform.googleapis.com/v1/projects/{project}/locations/{location}/endpoints/openapi"
	neoPiVertexAnthropicBaseURL = "https://{location}-aiplatform.googleapis.com/v1/projects/{project}/locations/{location}/publishers/anthropic/models/{model}:streamRawPredict"
	neoPiOpenCodeZenBasePath    = "https://opencode.ai/zen"
	neoPiOpenCodeGoBasePath     = "https://opencode.ai/inference/go"
	neoPiOpenCodeZenGoogleBase  = "https://opencode.ai/inference/google/v1beta"
	neoPiZenMuxOpenAIBaseURL    = "https://zenmux.ai/api/v1"
	neoPiZenMuxAnthropicBaseURL = "https://zenmux.ai/api/anthropic"
)

// neoPiResolveGoogleVertexAPI is resolveGoogleVertexApi.
func neoPiResolveGoogleVertexAPI(id string, raw gjson.Result) (string, string) {
	npm := raw.Get("provider.npm")
	if npm.Type == gjson.String && npm.String() == "@ai-sdk/google-vertex/anthropic" {
		return "anthropic-messages", strings.Replace(neoPiVertexAnthropicBaseURL, "{model}", id, 1)
	}
	if strings.Contains(id, "/") || npm.Type == gjson.String && npm.String() == "@ai-sdk/openai-compatible" {
		return "openai-completions", neoPiVertexOpenAIBaseURL
	}
	return "google-vertex", neoPiVertexBaseURL
}

// neoPiResolveOpenCodeAPI is resolveApiByRules over an OpenCode gateway's
// createOpenCodeApiResolution: its api-routes pin first, then the row's
// AI SDK package, else Chat Completions.
func neoPiResolveOpenCodeAPI(provider, basePath string) func(string, gjson.Result) (string, string) {
	baseFor := func(api string) string {
		if api == "google-generative-ai" && basePath == neoPiOpenCodeZenBasePath {
			return neoPiOpenCodeZenGoogleBase
		}
		if api == "anthropic-messages" {
			return basePath
		}
		return basePath + "/v1"
	}
	return func(id string, raw gjson.Result) (string, string) {
		if api, _ := neoPiAPIRouteFor(provider, id); slices.Contains([]string{"openai-completions", "openai-responses", "anthropic-messages", "google-generative-ai"}, api) {
			return api, baseFor(api)
		}
		switch npm := raw.Get("provider.npm"); {
		case npm.Type != gjson.String:
		case npm.String() == "@ai-sdk/openai":
			return "openai-responses", basePath + "/v1"
		case npm.String() == "@ai-sdk/anthropic":
			return "anthropic-messages", basePath
		case npm.String() == "@ai-sdk/google":
			return "google-generative-ai", basePath + "/v1"
		}
		return "openai-completions", basePath + "/v1"
	}
}

// neoPiModelsDevToolCall is the default descriptor filter.
func neoPiModelsDevToolCall(_ string, raw gjson.Result) bool {
	return raw.Get("tool_call").Type == gjson.True
}

// neoPiModelsDevActiveToolCall is filterActiveToolCallModels.
func neoPiModelsDevActiveToolCall(_ string, raw gjson.Result) bool {
	if raw.Get("tool_call").Type != gjson.True {
		return false
	}
	status := raw.Get("status")
	return status.Type != gjson.String || status.String() != "deprecated"
}

// neoPiModelsDevAzureID is the OpenAI-family filter on Azure's rows.
var neoPiModelsDevAzureID = regexp.MustCompile(`^(gpt-|o1|o3|o4|codex|chatgpt)`)

var (
	neoPiBedrockGlobalPrefixes = []string{"anthropic.claude-fable-5", "anthropic.claude-mythos-5", "anthropic.claude-haiku-4-5",
		"anthropic.claude-sonnet-4", "anthropic.claude-opus-4-5", "amazon.nova-2-lite", "cohere.embed-v4", "twelvelabs.pegasus-1-2"}
	neoPiBedrockUSPrefixes = []string{"amazon.nova-lite", "amazon.nova-micro", "amazon.nova-premier", "amazon.nova-pro",
		"anthropic.claude-3-7-sonnet", "anthropic.claude-opus-4-1", "anthropic.claude-opus-4-20250514", "deepseek.r1",
		"meta.llama3-2", "meta.llama3-3", "meta.llama4"}
)

// neoPiBedrockCrossRegionID is bedrockCrossRegionId.
func neoPiBedrockCrossRegionID(id string) string {
	for _, prefix := range neoPiBedrockGlobalPrefixes {
		if strings.HasPrefix(id, prefix) {
			return "global." + id
		}
	}
	for _, prefix := range neoPiBedrockUSPrefixes {
		if strings.HasPrefix(id, prefix) {
			return "us." + id
		}
	}
	return id
}

// neoPiXaiResponsesReasoningEffortMap is xaiResponsesReasoningEffortMap:
// Grok 4.6 and later clamp only minimal; older Grok clamps xhigh and max too.
func neoPiXaiResponsesReasoningEffortMap(id string) map[string]any {
	identity := neopitaxonomy.Classify("xai", id)
	if identity.Class == "xai" && identity.Family == "grok" && identity.HasRevision && identity.AtLeast("4.6") {
		return map[string]any{"minimal": "low"}
	}
	return map[string]any{"minimal": "low", "xhigh": "high", "max": "high"}
}

// neoPiApplyXaiResponsesThinkingPolicy is applyXaiResponsesThinkingPolicy:
// the effort dial the compat rules allow the row, with its wire remap.
func neoPiApplyXaiResponsesThinkingPolicy(row *NeoPiModel) {
	compat := maps.Clone(row.CompatConfig)
	if compat == nil {
		compat = map[string]any{}
	}
	capable, explicit := compat["supportsReasoningEffort"].(bool)
	if !explicit {
		policy, err := neopicompat.Resolve(neopicompat.Spec{Provider: row.Provider, API: row.API, ID: row.ID, BaseURL: row.BaseURL,
			Reasoning: row.Reasoning, Compat: row.CompatConfig})
		var resolved bool
		if err == nil {
			resolved, explicit = policy.Compat["supportsReasoningEffort"].(bool)
		}
		capable = resolved
	}
	if explicit {
		compat["supportsReasoningEffort"] = capable
	} else {
		delete(compat, "supportsReasoningEffort")
	}
	if _, set := compat["omitReasoningEffort"].(bool); !set {
		compat["omitReasoningEffort"] = !capable
	}
	if capable {
		compat["reasoningEffortMap"] = neoPiXaiResponsesReasoningEffortMap(row.ID)
	} else {
		delete(compat, "reasoningEffortMap")
	}
	row.CompatConfig = compat
}

// neoPiModelsDevDescriptors is MODELS_DEV_PROVIDER_DESCRIPTORS in order:
// Bedrock, Google Vertex, core, coding plans, specialized.
var neoPiModelsDevDescriptors = []neoPiModelsDevDescriptor{
	{key: "amazon-bedrock", provider: "amazon-bedrock", api: "bedrock-converse-stream", baseURL: "https://bedrock-runtime.us-east-1.amazonaws.com",
		filter: func(id string, raw gjson.Result) bool {
			return raw.Get("tool_call").Type == gjson.True && !neoPiExcludedModel("amazon-bedrock", id)
		},
		transform: func(row *NeoPiModel, id string, raw gjson.Result) []*NeoPiModel {
			crossRegion := neoPiBedrockCrossRegionID(id)
			row.ID, row.Name = crossRegion, neoPiModelName(raw.Get("name"), crossRegion)
			vendor, _, _ := strings.Cut(id, ".")
			if vendor != "anthropic" {
				return []*NeoPiModel{row}
			}
			display := neoPiModelName(raw.Get("name"), id)
			eu, gov := row.clone(), row.clone()
			eu.ID, eu.Name = "eu."+id, display+" (EU)"
			gov.ID, gov.Name = "us-gov."+id, display+" (GovCloud)"
			return []*NeoPiModel{row, eu, gov}
		}},
	{key: "google-vertex", provider: "google-vertex", api: "google-vertex", baseURL: neoPiVertexBaseURL, filter: neoPiModelsDevActiveToolCall, resolveAPI: neoPiResolveGoogleVertexAPI},
	{key: "anthropic", provider: "anthropic", api: "anthropic-messages", baseURL: "https://api.anthropic.com",
		filter: func(id string, raw gjson.Result) bool {
			return raw.Get("tool_call").Type == gjson.True && !neoPiExcludedModel("anthropic", id)
		}},
	{key: "google", provider: "google", api: "google-generative-ai", baseURL: "https://generativelanguage.googleapis.com/v1beta"},
	{key: "openai", provider: "openai", api: "openai-responses", baseURL: "https://api.openai.com/v1"},
	{key: "groq", provider: "groq", api: "openai-completions", baseURL: "https://api.groq.com/openai/v1"},
	{key: "cerebras", provider: "cerebras", api: "openai-completions", baseURL: "https://api.cerebras.ai/v1"},
	// ClinePass is reference-isolated: its rows never enter the map, so
	// its metadata transform is not ported.
	{key: "cline-pass", provider: "cline-pass", api: "openai-completions", baseURL: "https://api.cline.bot/api/v1"},
	{key: "togetherai", provider: "together", api: "openai-completions", baseURL: "https://api.together.xyz/v1"},
	{key: "wandb", provider: "coreweave", api: "openai-completions", baseURL: "https://api.inference.wandb.ai/v1",
		transform: func(row *NeoPiModel, _ string, _ gjson.Result) []*NeoPiModel {
			if neopitaxonomy.Classify("coreweave", row.ID).Class == "gpt-oss" {
				row.Reasoning = true
			}
			return []*NeoPiModel{row}
		}},
	{key: "nvidia", provider: "nvidia", api: "openai-completions", baseURL: "https://integrate.api.nvidia.com/v1", defaultContext: 131072},
	{key: "xai", provider: "xai", api: "openai-responses", baseURL: "https://api.x.ai/v1",
		transform: func(row *NeoPiModel, _ string, _ gjson.Result) []*NeoPiModel {
			neoPiApplyXaiResponsesThinkingPolicy(row)
			return []*NeoPiModel{row}
		}},
	{key: "deepseek", provider: "deepseek", api: "openai-completions", baseURL: "https://api.deepseek.com",
		filter: func(id string, raw gjson.Result) bool {
			return raw.Get("tool_call").Type == gjson.True && neoPiIsDeepseekV4Generation("deepseek", id)
		},
		compat: map[string]any{"supportsDeveloperRole": false, "supportsReasoningEffort": true, "maxTokensField": "max_tokens",
			"supportsToolChoice": false, "extraBody": map[string]any{"thinking": map[string]any{"type": "enabled"}},
			"reasoningContentField": "reasoning_content", "requiresReasoningContentForToolCalls": true, "requiresAssistantContentForToolCalls": true}},
	{key: "zai", provider: "zai", api: "anthropic-messages", baseURL: "https://api.z.ai/api/anthropic",
		resolveAPI: func(id string, _ gjson.Result) (string, string) {
			// resolveZaiApi: the api-routes pin, else Anthropic Messages.
			if api, _ := neoPiAPIRouteFor("zai", id); api == "openai-completions" {
				return api, "https://api.z.ai/api/coding/paas/v4"
			}
			return "anthropic-messages", "https://api.z.ai/api/anthropic"
		}},
	{key: "umans-ai", provider: "umans", api: "anthropic-messages", baseURL: "https://api.code.umans.ai"},
	{key: "xiaomi", provider: "xiaomi", api: "openai-completions", baseURL: "https://api.xiaomimimo.com/v1", defaultContext: 262144, defaultMax: 8192,
		compat: map[string]any{"supportsStore": false, "thinkingFormat": "zai", "reasoningContentField": "reasoning_content",
			"requiresReasoningContentForToolCalls": true, "allowsSyntheticReasoningContentForToolCalls": false}},
	{key: "minimax-coding-plan", provider: "minimax-code", api: "anthropic-messages", baseURL: "https://api.minimax.io/anthropic"},
	{key: "minimax-cn-coding-plan", provider: "minimax-code-cn", api: "anthropic-messages", baseURL: "https://api.minimaxi.com/anthropic"},
	{key: "alibaba-coding-plan", provider: "alibaba-coding-plan", api: "openai-completions", baseURL: "https://coding-intl.dashscope.aliyuncs.com/v1",
		compat: map[string]any{"supportsDeveloperRole": false}},
	{key: "zhipuai-coding-plan", provider: "zhipu-coding-plan", api: "openai-completions", baseURL: "https://open.bigmodel.cn/api/coding/paas/v4",
		compat: map[string]any{"thinkingFormat": "zai", "reasoningContentField": "reasoning_content", "supportsDeveloperRole": false}},
	{key: "azure", provider: "azure", api: "azure-openai-responses",
		filter: func(id string, raw gjson.Result) bool {
			return raw.Get("tool_call").Type == gjson.True && neoPiModelsDevAzureID.MatchString(id)
		}},
	{key: "cloudflare-ai-gateway", provider: "cloudflare-ai-gateway", api: "anthropic-messages", baseURL: "https://gateway.ai.cloudflare.com/v1/<account>/<gateway>/anthropic"},
	{key: "cloudflare-workers-ai", provider: "cloudflare-ai-gateway", api: "openai-completions", baseURL: "https://gateway.ai.cloudflare.com/v1/<account>/<gateway>/compat",
		filter: neoPiModelsDevActiveToolCall,
		transform: func(row *NeoPiModel, _ string, _ gjson.Result) []*NeoPiModel {
			row.ID = "workers-ai/" + row.ID
			return []*NeoPiModel{row}
		}},
	{key: "mistral", provider: "mistral", api: "openai-completions", baseURL: "https://api.mistral.ai/v1"},
	{key: "opencode", provider: "opencode-zen", api: "openai-completions", baseURL: neoPiOpenCodeZenBasePath + "/v1", filter: neoPiModelsDevActiveToolCall,
		resolveAPI: neoPiResolveOpenCodeAPI("opencode-zen", neoPiOpenCodeZenBasePath)},
	{key: "opencode-go", provider: "opencode-go", api: "openai-completions", baseURL: neoPiOpenCodeGoBasePath + "/v1", filter: neoPiModelsDevActiveToolCall,
		resolveAPI: neoPiResolveOpenCodeAPI("opencode-go", neoPiOpenCodeGoBasePath)},
	{key: "github-copilot", provider: "github-copilot", api: "openai-completions", baseURL: "https://api.githubcopilot.com", defaultContext: 128000, defaultMax: 8192,
		filter: neoPiModelsDevActiveToolCall,
		resolveAPI: func(id string, _ gjson.Result) (string, string) {
			if api, _ := neoPiAPIRouteFor("github-copilot", id); api == "anthropic-messages" || api == "openai-responses" {
				return api, "https://api.githubcopilot.com"
			}
			return "openai-completions", "https://api.githubcopilot.com"
		},
		transform: func(row *NeoPiModel, _ string, _ gjson.Result) []*NeoPiModel {
			if row.API == "openai-completions" {
				row.CompatConfig = map[string]any{"supportsStore": false, "supportsDeveloperRole": false, "supportsReasoningEffort": false}
			}
			return []*NeoPiModel{row}
		}},
	{key: "minimax", provider: "minimax", api: "anthropic-messages", baseURL: "https://api.minimax.io/anthropic"},
	{key: "minimax-cn", provider: "minimax-cn", api: "anthropic-messages", baseURL: "https://api.minimaxi.com/anthropic"},
	{key: "huggingface", provider: "huggingface", api: "openai-completions", baseURL: "https://router.huggingface.co/v1"},
	{key: "kilo", provider: "kilo", api: "openai-completions", baseURL: "https://api.kilo.ai/api/gateway"},
	{key: "moonshotai", provider: "moonshot", api: "openai-completions", baseURL: "https://api.moonshot.ai/v1"},
	{key: "nano-gpt", provider: "nanogpt", api: "openai-completions", baseURL: "https://nano-gpt.com/api/v1"},
	{key: "synthetic", provider: "synthetic", api: "openai-completions", baseURL: "https://api.synthetic.new/openai/v1"},
	{key: "venice", provider: "venice", api: "openai-completions", baseURL: "https://api.venice.ai/api/v1"},
	{key: "ollama-cloud", provider: "ollama-cloud", api: "ollama-chat", baseURL: "https://ollama.com"},
	{key: "xiaomi-token-plan-ams", provider: "xiaomi-token-plan-ams", api: "openai-completions", baseURL: "https://token-plan-ams.xiaomimimo.com/v1"},
	{key: "xiaomi-token-plan-cn", provider: "xiaomi-token-plan-cn", api: "openai-completions", baseURL: "https://token-plan-cn.xiaomimimo.com/v1"},
	{key: "xiaomi-token-plan-sgp", provider: "xiaomi-token-plan-sgp", api: "openai-completions", baseURL: "https://token-plan-sgp.xiaomimimo.com/v1"},
	{key: "qwen-portal", provider: "qwen-portal", api: "openai-completions", baseURL: "https://portal.qwen.ai/v1", defaultContext: 128000, defaultMax: 8192},
	{key: "zenmux", provider: "zenmux", api: "openai-completions", baseURL: neoPiZenMuxOpenAIBaseURL, filter: neoPiModelsDevActiveToolCall,
		resolveAPI: func(id string, _ gjson.Result) (string, string) {
			if api, _ := neoPiAPIRouteFor("zenmux", id); api == "anthropic-messages" {
				return api, neoPiZenMuxAnthropicBaseURL
			}
			return "openai-completions", neoPiZenMuxOpenAIBaseURL
		}},
}

// neoPiKindAPIKinds is KIND_API_KINDS: the kinds a provider may serve on a
// runner api of its own.
var neoPiKindAPIKinds = []string{"image", "tts", "stt", "embedding", "rerank", "video"}

// neoPiModelsDevGlobalReferences is loadModelsDevReferences over a fetched
// models.dev payload (createModelsDevReferenceMap): every descriptor's rows,
// the reference-isolated providers' left out, one per id (the larger context
// window, then the larger output limit, else the first). A payload that does
// not parse yields no references, as NeoPi's failed load does.
func neoPiModelsDevGlobalReferences(payload []byte) map[string]*NeoPiModel {
	rows := neoPiModelsDevMapAll(payload)
	if rows == nil {
		return nil
	}
	references := map[string]*NeoPiModel{}
	for _, row := range rows {
		if slices.Contains(neoPiReferenceIsolated, row.Provider) {
			continue
		}
		existing := references[row.ID]
		switch {
		case existing == nil,
			row.ContextWindow > existing.ContextWindow,
			row.ContextWindow == existing.ContextWindow && row.MaxTokens > existing.MaxTokens:
			references[row.ID] = row
		}
	}
	return references
}

// neoPiModelsDevMapAll is mapModelsDevToModels over every descriptor, in
// descriptor and then property order, except the reference-isolated
// providers', whose rows no reference map keeps (ClinePass's transform is
// not ported). nil for a payload that does not parse.
func neoPiModelsDevMapAll(payload []byte) []*NeoPiModel {
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return nil
	}
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		return nil
	}
	neoPiReferencesOnce.Do(loadNeoPiReferences)
	if neoPiReferencesErr != nil {
		return nil
	}
	topLevel := neoPiJSObjectEntries(root)
	rows := []*NeoPiModel{}
	for i := range neoPiModelsDevDescriptors {
		desc := &neoPiModelsDevDescriptors[i]
		if slices.Contains(neoPiReferenceIsolated, desc.provider) {
			continue
		}
		var providerData gjson.Result
		for _, entry := range topLevel {
			if entry.key == desc.key {
				providerData = entry.value
			}
		}
		if !providerData.IsObject() {
			continue
		}
		var models gjson.Result
		for _, entry := range neoPiJSObjectEntries(providerData) {
			if entry.key == "models" {
				models = entry.value
			}
		}
		if !models.IsObject() {
			continue
		}
		for _, entry := range neoPiJSObjectEntries(models) {
			rows = append(rows, neoPiModelsDevRows(desc, entry.key, neoPiJSONParse(entry.value.Raw))...)
		}
	}
	return rows
}

// neoPiModelsDevRows maps one models.dev row through desc (the body of
// mapModelsDevToModels' inner loop): nil when the row is filtered out.
func neoPiModelsDevRows(desc *neoPiModelsDevDescriptor, id string, raw gjson.Result) []*NeoPiModel {
	if !raw.IsObject() {
		return nil
	}
	name := neoPiModelName(raw.Get("name"), id)
	kind, kindAPI := "", ""
	if authored := raw.Get("kind"); authored.Exists() {
		normalized := ""
		if policy, err := neopicompat.Resolve(neopicompat.Spec{Provider: desc.provider, API: desc.api, ID: id, BaseURL: desc.baseURL}); err == nil {
			normalized, _ = policy.Catalog["kind"].(string)
		}
		if normalized == "" {
			if authored.Type != gjson.String {
				return nil
			}
			normalized = authored.String()
		}
		if !slices.Contains(neoPiModelKinds, normalized) {
			return nil
		}
		if normalized != "chat" {
			if !slices.Contains(neoPiKindAPIKinds, normalized) {
				return nil
			}
			if kindAPI = neoPiKindAPIs[desc.provider][normalized]; kindAPI == "" {
				return nil
			}
			kind = normalized
		}
	}
	if kind == "" {
		filter := desc.filter
		if filter == nil {
			filter = neoPiModelsDevToolCall
		}
		if !filter(id, raw) {
			return nil
		}
	}
	api, baseURL := desc.api, desc.baseURL
	switch {
	case kind != "":
		api = kindAPI
	case desc.resolveAPI != nil:
		api, baseURL = desc.resolveAPI(id, raw)
	}
	row := &NeoPiModel{
		ID:            id,
		Name:          name,
		API:           api,
		Kind:          kind,
		Provider:      desc.provider,
		BaseURL:       baseURL,
		Reasoning:     kind == "" && raw.Get("reasoning").Type == gjson.True,
		Input:         neoPiModelsDevInput(raw.Get("modalities.input")),
		ContextWindow: neoPiPositive(raw.Get("limit.context"), desc.defaultContext),
		MaxTokens:     neoPiPositive(raw.Get("limit.output"), desc.defaultMax),
		CompatConfig:  maps.Clone(desc.compat),
		cost: NeoPiCost{
			Input:      neoPiModelsDevRate(raw.Get("cost.input")),
			Output:     neoPiModelsDevRate(raw.Get("cost.output")),
			CacheRead:  neoPiModelsDevRate(raw.Get("cost.cache_read")),
			CacheWrite: neoPiModelsDevRate(raw.Get("cost.cache_write")),
		},
	}
	if kind == "" && desc.transform != nil {
		return desc.transform(row, id, raw)
	}
	return []*NeoPiModel{row}
}

// clone is a copy of the row whose slices and compat are its own.
func (m *NeoPiModel) clone() *NeoPiModel {
	out := *m
	out.Input = slices.Clone(m.Input)
	out.CompatConfig = maps.Clone(m.CompatConfig)
	out.Thinking = m.Thinking.Clone()
	return &out
}

// neoPiJSEntry is one property of a parsed JSON object.
type neoPiJSEntry struct {
	key   string
	value gjson.Result
}

// neoPiJSObjectEntries is a JSON object's properties in the order a
// for...in loop over JSON.parse's result visits them: array-index keys
// ascending, then the rest by first appearance, a duplicate key taking its
// last value.
func neoPiJSObjectEntries(object gjson.Result) []neoPiJSEntry {
	var entries []neoPiJSEntry
	at := map[string]int{}
	object.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if i, seen := at[name]; seen {
			entries[i].value = value
			return true
		}
		at[name] = len(entries)
		entries = append(entries, neoPiJSEntry{key: name, value: value})
		return true
	})
	index := func(key string) (uint64, bool) {
		if key == "" || len(key) > 1 && key[0] == '0' {
			return 0, false
		}
		n, err := strconv.ParseUint(key, 10, 32)
		return n, err == nil && n < 1<<32-1
	}
	sort.SliceStable(entries, func(a, b int) bool {
		na, aIndex := index(entries[a].key)
		nb, bIndex := index(entries[b].key)
		if aIndex != bIndex {
			return aIndex
		}
		return aIndex && na < nb
	})
	return entries
}
