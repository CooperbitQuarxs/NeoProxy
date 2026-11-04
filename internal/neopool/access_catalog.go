package neopool

import (
	"regexp"
	"strings"
)

// ProviderPlan is a subscription tier or budget with its default monthly value.
type ProviderPlan struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	MonthlyUSD float64 `json:"monthlyUSD"`
}

// ProviderSubscription describes the existing engine sign-in for a provider.
// Flow is "browser" (provider page in a new tab, redirect back) or "device"
// (provider page plus a short code the person confirms).
type ProviderSubscription struct {
	Flow string `json:"flow"`
	Note string `json:"note"`
}

// ProviderAPIKey describes how a pay-as-you-go key is validated and routed.
// Style selects the model-listing protocol: openai, anthropic or gemini.
type ProviderAPIKey struct {
	Style           string `json:"style"`
	BaseURL         string `json:"baseUrl"`
	EditableBaseURL bool   `json:"editableBaseUrl"`
	KeyHint         string `json:"keyHint"`
	GetKeyURL       string `json:"getKeyUrl"`
	KeyOptional     bool   `json:"keyOptional,omitempty"`
}

// ProviderInfo is one entry of the dashboard's provider catalogue. Hue and
// Chroma are OKLCH components (L≈0.74); Chroma 0 renders a neutral grey/silver.
type ProviderInfo struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Vendor       string                `json:"vendor"`
	Hue          float64               `json:"hue"`
	Chroma       float64               `json:"chroma"`
	Methods      []string              `json:"methods"`
	Subscription *ProviderSubscription `json:"subscription,omitempty"`
	APIKey       *ProviderAPIKey       `json:"apiKey,omitempty"`
	Plans        []ProviderPlan        `json:"plans"`
	// engine is the native engine provider for API keys; empty means the key is
	// routed through an openai-compatibility entry named after the catalogue ID.
	engine string
	// regions are regional endpoints of one product whose keys each authenticate
	// at exactly one of them; validation keeps the first that accepts the key.
	// APIKey.BaseURL is the first region.
	regions []string
	// skipModels are model-id prefixes the provider lists but chat clients
	// cannot use (image, audio, video and embedding SKUs).
	skipModels []string
}

const providerChroma = 0.13

var (
	chatGPTPlans = []ProviderPlan{{"free", "ChatGPT Free", 0}, {"plus", "ChatGPT Plus", 20}, {"pro", "ChatGPT Pro", 200}, {"team", "ChatGPT Team", 30}, {"business", "ChatGPT Business", 30}, {"enterprise", "ChatGPT Enterprise", 60}}
	claudePlans  = []ProviderPlan{{"pro", "Claude Pro", 20}, {"max5x", "Claude Max 5x", 100}, {"max20x", "Claude Max 20x", 200}}
	grokPlans    = []ProviderPlan{{"supergrok", "SuperGrok", 30}, {"supergrok-heavy", "SuperGrok Heavy", 300}}
	googlePlans  = []ProviderPlan{{"google-ai-pro", "Google AI Pro", 20}, {"google-ai-ultra", "Google AI Ultra", 250}}
)

// QwenCloud Token Plan endpoints. Keys are region-locked: a China (Beijing) key
// is rejected by the International (Singapore) endpoint and vice versa.
const (
	qwenTokenPlanIntlURL = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"
	qwenTokenPlanCNURL   = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
)

// Default monthly values when nothing more specific is known.
const (
	defaultSubscriptionUSD = 20
	defaultAPIKeyUSD       = 20
)

func openAIKey(base, hint, getKey string) *ProviderAPIKey {
	return &ProviderAPIKey{Style: "openai", BaseURL: base, KeyHint: hint, GetKeyURL: getKey}
}

func browser(note string) *ProviderSubscription {
	return &ProviderSubscription{Flow: "browser", Note: note}
}
func device(note string) *ProviderSubscription {
	return &ProviderSubscription{Flow: "device", Note: note}
}

// providerCatalog lists only sign-ins whose engine handler exists
// (management StartOAuthV8: claude, codex, antigravity, kimi, xai, devin, meta).
// gemini-cli, qwen and iflow have no handler and are therefore key-only or absent.
var providerCatalog = []ProviderInfo{
	{ID: "codex", Name: "ChatGPT / Codex", Vendor: "OpenAI", Hue: 165, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: browser("Opens OpenAI's sign-in in a new tab. NeoProxy keeps the sign-in refreshed."),
		APIKey:       &ProviderAPIKey{Style: "openai", BaseURL: "https://api.openai.com/v1", KeyHint: "sk-...", GetKeyURL: "https://platform.openai.com/api-keys"},
		Plans:        chatGPTPlans, engine: "codex"},
	{ID: "claude", Name: "Claude", Vendor: "Anthropic", Hue: 45, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: browser("Opens Anthropic's sign-in in a new tab. NeoProxy keeps the sign-in refreshed."),
		APIKey:       &ProviderAPIKey{Style: "anthropic", BaseURL: "https://api.anthropic.com", KeyHint: "sk-ant-...", GetKeyURL: "https://console.anthropic.com/settings/keys"},
		Plans:        claudePlans, engine: "claude"},
	{ID: "gemini", Name: "Gemini", Vendor: "Google", Hue: 255, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "gemini", BaseURL: "https://generativelanguage.googleapis.com", KeyHint: "AIza...", GetKeyURL: "https://aistudio.google.com/apikey"},
		Plans:  []ProviderPlan{}, engine: "gemini"},
	{ID: "antigravity", Name: "Antigravity", Vendor: "Google", Hue: 255, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: browser("Opens Google's sign-in in a new tab. NeoProxy keeps the sign-in refreshed."),
		Plans:        googlePlans},
	{ID: "xai", Name: "Grok / xAI", Vendor: "xAI", Hue: 0, Chroma: 0, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens x.ai in a new tab and shows a code to confirm there. Covers SuperGrok and the Grok Build (Grok CLI) models."),
		APIKey:       openAIKey("https://api.x.ai/v1", "xai-...", "https://console.x.ai/"),
		Plans:        grokPlans, engine: "xai"},
	{ID: "kimi", Name: "Kimi", Vendor: "Moonshot AI", Hue: 340, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: device("Opens Kimi in a new tab and shows a code to confirm there."),
		Plans:        []ProviderPlan{}},
	{ID: "devin", Name: "Devin", Vendor: "Cognition", Hue: 205, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: browser("Opens Devin's sign-in in a new tab."),
		Plans:        []ProviderPlan{}},
	{ID: "meta", Name: "Meta AI", Vendor: "Meta", Hue: 280, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens Meta in a new tab and shows a code to confirm there."),
		APIKey:       openAIKey("https://api.meta.ai/v1", "LLM|...", "https://llama.developer.meta.com/"),
		Plans:        []ProviderPlan{}, engine: "meta"},
	{ID: "openrouter", Name: "OpenRouter", Vendor: "OpenRouter", Hue: 300, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://openrouter.ai/api/v1", "sk-or-v1-...", "https://openrouter.ai/settings/keys"), Plans: []ProviderPlan{}},
	{ID: "deepseek", Name: "DeepSeek", Vendor: "DeepSeek", Hue: 230, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.deepseek.com/v1", "sk-...", "https://platform.deepseek.com/api_keys"), Plans: []ProviderPlan{}},
	{ID: "groq", Name: "Groq", Vendor: "Groq", Hue: 25, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.groq.com/openai/v1", "gsk_...", "https://console.groq.com/keys"), Plans: []ProviderPlan{}},
	{ID: "mistral", Name: "Mistral", Vendor: "Mistral AI", Hue: 70, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.mistral.ai/v1", "32 letters and digits", "https://console.mistral.ai/api-keys"), Plans: []ProviderPlan{}},
	{ID: "together", Name: "Together AI", Vendor: "Together AI", Hue: 120, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.together.xyz/v1", "tgp_v1_...", "https://api.together.ai/settings/api-keys"), Plans: []ProviderPlan{}},
	{ID: "fireworks", Name: "Fireworks AI", Vendor: "Fireworks AI", Hue: 15, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.fireworks.ai/inference/v1", "fw_...", "https://fireworks.ai/account/api-keys"), Plans: []ProviderPlan{}},
	{ID: "cerebras", Name: "Cerebras", Vendor: "Cerebras", Hue: 95, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.cerebras.ai/v1", "csk-...", "https://cloud.cerebras.ai/"), Plans: []ProviderPlan{}},
	{ID: "moonshot", Name: "Moonshot", Vendor: "Moonshot AI", Hue: 340, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.moonshot.ai/v1", "sk-...", "https://platform.moonshot.ai/console/api-keys"), Plans: []ProviderPlan{}},
	{ID: "zai", Name: "Z.ai", Vendor: "Zhipu AI", Hue: 190, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.z.ai/api/paas/v4", "32 hex digits, a dot, then more", "https://z.ai/manage-apikey/apikey-list"), Plans: []ProviderPlan{}},
	{ID: "nanogpt", Name: "NanoGPT", Vendor: "NanoGPT", Hue: 150, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://nano-gpt.com/api/v1", "sk-nano-...", "https://nano-gpt.com/api"), Plans: []ProviderPlan{}},
	{ID: "alibaba-token-plan", Name: "QwenCloud Token Plan", Vendor: "Qwen", Hue: 285, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey:     openAIKey(qwenTokenPlanIntlURL, "sk-sp-... (International or China key)", "https://home.qwencloud.com/billing/subscription/token-plan-individual"),
		Plans:      []ProviderPlan{{"lite", "Token Plan Lite", 6}, {"essential", "Token Plan Essential", 10}, {"standard", "Token Plan Standard", 18}, {"pro", "Token Plan Pro", 68}},
		regions:    []string{qwenTokenPlanIntlURL, qwenTokenPlanCNURL},
		skipModels: []string{"fun-asr", "happyhorse-", "qwen-audio-", "qwen-image-", "text-embedding-", "wan"}},
	{ID: "custom", Name: "OpenAI-compatible server", Vendor: "Any", Hue: 0, Chroma: 0, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "openai", BaseURL: "http://localhost:11434/v1", EditableBaseURL: true, KeyHint: "Leave empty for local servers such as Ollama or LM Studio", KeyOptional: true},
		Plans:  []ProviderPlan{}},
}

// catalogBaseURL resolves a catalogue provider's fixed API base URL. Tests may
// replace it to point a known provider at a fake upstream.
var catalogBaseURL = func(id string) string {
	if p, ok := catalogProvider(id); ok && p.APIKey != nil {
		return p.APIKey.BaseURL
	}
	return ""
}

// catalogRegions resolves a catalogue provider's regional endpoints (nil for
// single-endpoint providers). Tests may replace it like catalogBaseURL.
var catalogRegions = func(id string) []string {
	p, _ := catalogProvider(id)
	return p.regions
}

func catalogProvider(id string) (ProviderInfo, bool) {
	for _, p := range providerCatalog {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderInfo{}, false
}

func catalogPlan(provider, planID string) (ProviderPlan, bool) {
	p, ok := catalogProvider(provider)
	if !ok {
		return ProviderPlan{}, false
	}
	for _, plan := range p.Plans {
		if plan.ID == planID {
			return plan, true
		}
	}
	return ProviderPlan{}, false
}

var compatSuffix = regexp.MustCompile(`-\d+$`)

// catalogProviderID maps an engine provider (or a NeoPi provider name) to the
// catalogue ID shown to people. Unknown engine providers keep their own ID.
func catalogProviderID(engineProvider, compatName string) string {
	if compatName = strings.ToLower(strings.TrimSpace(compatName)); compatName != "" {
		base := compatSuffix.ReplaceAllString(strings.TrimPrefix(compatName, "openai-compatible-"), "")
		if p, ok := catalogProvider(base); ok && p.engine == "" && p.APIKey != nil && base != "custom" {
			return base
		}
		return "custom"
	}
	switch id := strings.ToLower(strings.TrimSpace(engineProvider)); id {
	case "codex", "openai", "openai-codex", "chatgpt":
		return "codex"
	case "claude", "anthropic", "claude-code":
		return "claude"
	case "gemini", "gemini-cli", "google", "vertex", "gemini-interactions", "aistudio", "google-gemini":
		return "gemini"
	case "antigravity", "google-antigravity":
		return "antigravity"
	case "xai", "grok":
		return "xai"
	case "kimi", "kimi-ai", "kimi-code", "kimi-coding":
		return "kimi"
	case "meta", "meta-ai":
		return "meta"
	default:
		return id
	}
}

// providerList returns the catalogue plus a neutral entry for every engine
// provider present in items but unknown to the catalogue.
func providerList(items []AccessItem) []ProviderInfo {
	out := make([]ProviderInfo, 0, len(providerCatalog)+2)
	out = append(out, providerCatalog...)
	seen := map[string]bool{}
	for _, p := range providerCatalog {
		seen[p.ID] = true
	}
	for _, item := range items {
		if seen[item.Provider] {
			continue
		}
		seen[item.Provider] = true
		out = append(out, ProviderInfo{ID: item.Provider, Name: item.Provider, Vendor: item.Provider, Methods: []string{}, Plans: []ProviderPlan{}})
	}
	return out
}
