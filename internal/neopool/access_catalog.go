package neopool

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
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
	// upstreamErrors seeds that openai-compatibility entry's error
	// classification with provider quirks.
	upstreamErrors []config.RequestScopedErrorRule
}

const providerChroma = 0.13

var (
	chatGPTPlans = []ProviderPlan{{"free", "ChatGPT Free", 0}, {"plus", "ChatGPT Plus", 20}, {"pro", "ChatGPT Pro", 200}, {"team", "ChatGPT Team", 30}, {"business", "ChatGPT Business", 30}, {"enterprise", "ChatGPT Enterprise", 60}}
	claudePlans  = []ProviderPlan{{"pro", "Claude Pro", 20}, {"max5x", "Claude Max 5x", 100}, {"max20x", "Claude Max 20x", 200}}
	grokPlans    = []ProviderPlan{{"supergrok", "SuperGrok", 30}, {"supergrok-heavy", "SuperGrok Heavy", 300}}
	googlePlans  = []ProviderPlan{{"google-ai-pro", "Google AI Pro", 20}, {"google-ai-ultra", "Google AI Ultra", 250}}
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
		Subscription: device("Opens x.ai in a new tab and shows a code to confirm there."),
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
	{ID: "huggingface", Name: "Hugging Face", Vendor: "Hugging Face", Hue: 85, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://router.huggingface.co/v1", "hf_...", "https://huggingface.co/settings/tokens/new?ownUserPermissions=inference.serverless.write&tokenType=fineGrained"), Plans: []ProviderPlan{}},
	// NVIDIA's free catalogue keeps listing models an account cannot call
	// (404 "Function ... Not found for account") or that reached end of life
	// (410 Gone). Those concern the requested model only, never the whole key.
	{ID: "nvidia", Name: "NVIDIA NIM", Vendor: "NVIDIA", Hue: 135, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://integrate.api.nvidia.com/v1", "nvapi-...", "https://build.nvidia.com/settings/api-keys"), Plans: []ProviderPlan{},
		upstreamErrors: []config.RequestScopedErrorRule{
			{Status: 404, Match: []string{"Not Found", "not found"}, Action: "stop"},
			{Status: 410, Match: []string{"Gone", "end of life"}, Action: "stop"},
		}},
	{ID: "vercel-ai-gateway", Name: "Vercel AI Gateway", Vendor: "Vercel", Hue: 0, Chroma: 0, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://ai-gateway.vercel.sh/v1", "vck_...", "https://vercel.com/d?to=%2F%5Bteam%5D%2F%7E%2Fai-gateway%2Fapi-keys&title=AI+Gateway+API+Keys"), Plans: []ProviderPlan{}},
	{ID: "nous-portal", Name: "Nous Portal", Vendor: "Nous Research", Hue: 320, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://inference-api.nousresearch.com/v1", "sk-...", "https://portal.nousresearch.com/"), Plans: []ProviderPlan{}},
	// Marineris is a self-hosted gateway, so its address is chosen per key;
	// the default is the gateway's own documented listen address.
	{ID: "marineris", Name: "Marineris", Vendor: "Marineris gateway", Hue: 35, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "openai", BaseURL: "http://192.168.50.50:8801/v1", EditableBaseURL: true, KeyHint: "mrn_... (issued by the gateway's administrator)"},
		Plans:  []ProviderPlan{}},
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

// accessProviderID is the catalogue ID an engine credential is shown under.
// NeoPi-served credentials keep NeoPi's provider name; custom OpenAI-compatible
// entries whose address is a known provider's fixed API (for example keys
// imported before that provider had its own entry) show as that provider.
func accessProviderID(a *coreauth.Auth, kind string) string {
	if kind == "neopi" {
		return catalogProviderID(a.Attributes["neopi_provider"], "")
	}
	id := catalogProviderID(a.Provider, a.Attributes["compat_name"])
	if id != "custom" {
		return id
	}
	base := strings.TrimRight(strings.TrimSpace(a.Attributes["base_url"]), "/")
	for _, p := range providerCatalog {
		if p.engine == "" && p.APIKey != nil && !p.APIKey.EditableBaseURL && base != "" && strings.EqualFold(p.APIKey.BaseURL, base) {
			return p.ID
		}
	}
	return id
}

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
