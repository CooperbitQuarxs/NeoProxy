package neopool

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cloudflare"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/gitlabduo"
	kiloauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kilo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/snowflake"
	umansauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/umans"
	zaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
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
// Flow is "browser" (provider page in a new tab, redirect back), "device"
// (provider page plus a short code the person confirms) or "code" (provider
// page in a new tab; the provider cannot redirect back, so the person pastes
// the final address or code into NeoProxy).
type ProviderSubscription struct {
	Flow  string   `json:"flow"`
	Note  string   `json:"note"`
	Modes []string `json:"modes,omitempty"`
	// Site names where the person signs in when that isn't the vendor
	// (Cursor, not Anysphere); the dashboard falls back to Vendor.
	Site string `json:"site,omitempty"`
	// Callback is, for the "code" flow, the shape of the address the
	// provider hands off to (its app's scheme), shown as the paste example.
	Callback string `json:"callback,omitempty"`
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
	// Regions are endpoints of one product whose keys each authenticate at
	// exactly one of them; the person picks one and only it checks the key.
	// BaseURL is the first.
	Regions []ProviderRegion `json:"regions,omitempty"`
	// CustomRegion offers administrators NeoPi's "Custom" endpoint option
	// besides Regions.
	CustomRegion bool `json:"customRegion,omitempty"`
}

// ProviderRegion is one endpoint choice of a regional provider.
type ProviderRegion struct {
	Label   string `json:"label"`
	BaseURL string `json:"baseUrl"`
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
	// Fields are the details a sign-in or key needs besides the credential,
	// asked before either flow starts (NeoPi's login prompts: Snowflake's
	// account; Cloudflare's account and gateway ids).
	Fields []ProviderField `json:"fields,omitempty"`
	Plans  []ProviderPlan  `json:"plans"`
	// engine is the native engine provider for API keys; empty means the key is
	// routed through an openai-compatibility entry named after the catalogue ID.
	engine string
	// upstreamErrors seeds that openai-compatibility entry's error
	// classification with provider quirks.
	upstreamErrors []config.RequestScopedErrorRule
	// dialect is the upstream API of every model (chat-completions when empty);
	// Responses-only gateways set "responses".
	dialect string
	// dialectRules route models whose model-list entry does not advertise its
	// API, by id prefix; the first match wins and unmatched ids stay on dialect.
	dialectRules []dialectRule
	// sessionHeader is the request header that must carry a session id.
	sessionHeader string
	// headers are static request headers for the provider's entry.
	headers map[string]string
}

// ProviderField is one detail a provider's sign-in or key needs; the request
// carries its value under ID. An optional field may be left blank.
type ProviderField struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	Hint        string `json:"hint"`
	Optional    bool   `json:"optional,omitempty"`
}

// dialectRule routes model ids starting with prefix to dialect.
type dialectRule struct {
	prefix  string
	dialect string
}

const providerChroma = 0.13

// tintChroma tints a provider whose brand is monochrome (Cursor, OpenCode Zen,
// Vercel) so tiles sharing one pool stay apart; xAI and custom servers keep
// pure grey (Chroma 0).
const tintChroma = 0.045

var (
	chatGPTPlans = []ProviderPlan{{"free", "ChatGPT Free", 0}, {"plus", "ChatGPT Plus", 20}, {"pro", "ChatGPT Pro", 200}, {"team", "ChatGPT Team", 30}, {"business", "ChatGPT Business", 30}, {"enterprise", "ChatGPT Enterprise", 60}}
	claudePlans  = []ProviderPlan{{"pro", "Claude Pro", 20}, {"max5x", "Claude Max 5x", 100}, {"max20x", "Claude Max 20x", 200}}
	grokPlans    = []ProviderPlan{{"supergrok", "SuperGrok", 30}, {"supergrok-heavy", "SuperGrok Heavy", 300}}
	googlePlans  = []ProviderPlan{{"google-ai-pro", "Google AI Pro", 20}, {"google-ai-ultra", "Google AI Ultra", 250}}
	// GLM Coding Plan list prices (monthly billing); ids match the level Z.ai's
	// quota API reports.
	zaiPlans = []ProviderPlan{{"lite", "GLM Coding Plan Lite", 18}, {"pro", "GLM Coding Plan Pro", 80}, {"max", "GLM Coding Plan Max", 168}}
	// Cursor's plans on The X Subscription, then the legacy plans existing subscribers keep
	// (cursor.com/help/account-and-billing/pricing).
	cursorPlans = []ProviderPlan{{"plus", "Cursor Plus", 8}, {"premium", "Cursor Premium", 30}, {"super", "Cursor Super", 100}, {"ultra", "Cursor Ultra", 200}, {"pro", "Cursor Pro (legacy)", 20}, {"pro-plus", "Cursor Pro+ (legacy)", 60}}
)

// QwenCloud Token Plan endpoints. Keys are region-locked: a China (Beijing) key
// is rejected by the International (Singapore) endpoint and vice versa.
const (
	qwenTokenPlanIntlURL = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"
	qwenTokenPlanCNURL   = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
	// The Alibaba Coding Plan sign-in's International and China endpoints.
	alibabaCodingPlanIntlURL = "https://coding-intl.dashscope.aliyuncs.com/v1"
	alibabaCodingPlanCNURL   = "https://coding.dashscope.aliyuncs.com/v1"
)

// Default monthly values when nothing more specific is known.
const (
	defaultSubscriptionUSD = 20
	defaultAPIKeyUSD       = 20
)

// OpenCode's model lists name each model's API (chat-completions, responses,
// messages, generate-content) and its base URL. These family rules only cover
// ids listed without that metadata. Every OpenCode inference request must carry
// x-opencode-session, or the gateway answers 400 MissingSessionID.
var openCodeDialectRules = []dialectRule{
	{"claude-", "messages"},
	{"gpt-", "responses"},
	{"grok-", "responses"},
	{"muse-", "responses"},
}

const openCodeSessionHeader = "x-opencode-session"

// openCodeHeaders replace the engine's default OpenAI-compatible User-Agent,
// which opencode.ai's Cloudflare front rejects (error 1010, HTTP 403).
var openCodeHeaders = map[string]string{"User-Agent": "NeoProxy"}

func openAIKey(base, hint, getKey string) *ProviderAPIKey {
	return &ProviderAPIKey{Style: "openai", BaseURL: base, KeyHint: hint, GetKeyURL: getKey}
}

// regional lists a key's endpoint choices after NeoPi's region prompt
// (1, 2, …, then Custom); the first is the default.
func (k *ProviderAPIKey) regional(regions ...ProviderRegion) *ProviderAPIKey {
	k.BaseURL, k.Regions, k.CustomRegion = regions[0].BaseURL, regions, true
	return k
}

func browser(note string) *ProviderSubscription {
	return &ProviderSubscription{Flow: "browser", Note: note}
}
func device(note string) *ProviderSubscription {
	return &ProviderSubscription{Flow: "device", Note: note}
}

// at sets where the person signs in, for sites not named after the vendor.
func (s *ProviderSubscription) at(site string) *ProviderSubscription {
	s.Site = site
	return s
}

// providerCatalog lists only sign-ins whose engine handler exists
// (management StartOAuthV8: claude, codex, antigravity, kimi, xai, grok-build, devin, meta, zai, cursor, nous-portal, factory-droid, github-copilot, gitlab-duo, gitlab-duo-agent).
// gemini-cli, qwen and iflow have no handler and are therefore key-only or absent.
var providerCatalog = []ProviderInfo{
	{ID: "codex", Name: "ChatGPT / Codex", Vendor: "OpenAI", Hue: 165, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: &ProviderSubscription{Flow: "browser", Modes: []string{"browser", "device"}, Note: "Use OpenAI's browser sign-in, or enter a device code on OpenAI's verification page. NeoProxy keeps the sign-in refreshed."},
		APIKey:       &ProviderAPIKey{Style: "openai", BaseURL: "https://api.openai.com/v1", KeyHint: "sk-...", GetKeyURL: "https://platform.openai.com/api-keys"},
		Plans:        chatGPTPlans, engine: "codex"},
	{ID: "claude", Name: "Claude", Vendor: "Anthropic", Hue: 45, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: browser("Opens Anthropic's sign-in in a new tab. If this page doesn't continue after you approve, copy the address your browser ends up on and paste it here. NeoProxy keeps the sign-in refreshed."),
		APIKey:       &ProviderAPIKey{Style: "anthropic", BaseURL: "https://api.anthropic.com", KeyHint: "sk-ant-...", GetKeyURL: "https://console.anthropic.com/settings/keys"},
		Plans:        claudePlans, engine: "claude"},
	{ID: "gemini", Name: "Gemini", Vendor: "Google", Hue: 255, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "gemini", BaseURL: "https://generativelanguage.googleapis.com", KeyHint: "AIza...", GetKeyURL: "https://aistudio.google.com/apikey"},
		Plans:  []ProviderPlan{}, engine: "gemini"},
	{ID: "antigravity", Name: "Antigravity", Vendor: "Google", Hue: 255, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: browser("Opens Google's sign-in in a new tab. NeoProxy keeps the sign-in refreshed."),
		Plans:        googlePlans},
	{ID: "xai", Name: "Grok / xAI", Vendor: "xAI", Hue: 0, Chroma: 0, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens x.ai in a new tab and shows a code to confirm there. Requests go to the official xAI API."),
		APIKey:       openAIKey("https://api.x.ai/v1", "xai-...", "https://console.x.ai/"),
		Plans:        grokPlans, engine: "xai"},
	// Same xAI sign-in and token as xai, served through the Grok CLI chat-proxy,
	// which has its own speed, usage limits and model list.
	{ID: "grok-build", Name: "Grok Build", Vendor: "xAI", Hue: 250, Chroma: tintChroma, Methods: []string{"subscription"},
		Subscription: device("Opens x.ai in a new tab and shows a code to confirm there. Requests go to Grok Build, the Grok CLI's endpoint, with its own speed, usage limits and models."),
		Plans:        grokPlans},
	{ID: "kimi", Name: "Kimi", Vendor: "Moonshot AI", Hue: 340, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: device("Opens Kimi in a new tab and shows a code to confirm there.").at("Kimi"),
		Plans:        []ProviderPlan{}},
	{ID: "factory-droid", Name: "Factory Droid", Vendor: "Factory", Hue: 30, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: device("Opens Factory's sign-in in a new tab and shows a code to confirm there. NeoProxy keeps the sign-in refreshed.").at("Factory"),
		Plans:        []ProviderPlan{}},
	// GitHub's brand is monochrome: tinted so its tile stays apart.
	{ID: "github-copilot", Name: "GitHub Copilot", Vendor: "GitHub", Hue: 290, Chroma: tintChroma, Methods: []string{"subscription"},
		Subscription: device("Opens GitHub in a new tab and shows a code to confirm there."),
		Fields: []ProviderField{{ID: "enterprise_url", Label: "GitHub Enterprise", Placeholder: "github.com", Hint: "Leave blank for github.com, or enter your GitHub Enterprise URL or domain.",
			Optional: true}},
		Plans: []ProviderPlan{}},
	// GitLab Duo: NeoPi's browser sign-in at gitlab.com, or a personal
	// access token (GITLAB_TOKEN), which NeoPi never checks.
	{ID: gitlabduo.Provider, Name: "GitLab Duo", Vendor: "GitLab", Hue: 40, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: browser("Opens GitLab's sign-in in a new tab. NeoProxy keeps the sign-in refreshed.").at("GitLab"),
		APIKey:       openAIKey(gitlabduo.BaseURL, "Personal access token with the api scope, glpat-... (not checked before it is added)", "https://gitlab.com/-/user_settings/personal_access_tokens"),
		Plans:        []ProviderPlan{}},
	// GitLab Duo Agent: NeoPi's sign-in with GitLab's VS Code application,
	// whose redirect opens VS Code (the person pastes that address back), or
	// a personal access token (GITLAB_TOKEN) kept as the agent's own key.
	{ID: gitlabduo.AgentProvider, Name: "GitLab Duo Agent", Vendor: "GitLab", Hue: 40, Chroma: tintChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: (&ProviderSubscription{Flow: "code", Callback: gitlabduo.AgentRedirectURI + "?code=…", Note: "Opens GitLab's sign-in for its VS Code extension in a new tab. GitLab then hands off to VS Code, so copy that vscode:// address (or the code in it) and paste it here. NeoProxy keeps the sign-in refreshed."}).at("GitLab"),
		APIKey:       openAIKey(gitlabduo.BaseURL, "Personal access token with the api scope, glpat-... (not checked before it is added)", "https://gitlab.com/-/user_settings/personal_access_tokens"),
		Plans:        []ProviderPlan{}, engine: gitlabduo.AgentProvider},
	{ID: "devin", Name: "Devin", Vendor: "Cognition", Hue: 205, Chroma: providerChroma, Methods: []string{"subscription"},
		Subscription: browser("Opens Devin's sign-in in a new tab.").at("Devin"),
		Plans:        []ProviderPlan{}},
	{ID: "meta", Name: "Meta AI", Vendor: "Meta", Hue: 280, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens Meta in a new tab and shows a code to confirm there."),
		APIKey:       openAIKey("https://api.meta.ai/v1", "LLM|...", "https://developer.meta.com/ai/"),
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
		APIKey: openAIKey("https://api.together.xyz/v1", "sk-...", "https://api.together.xyz/settings/api-keys"), Plans: []ProviderPlan{}},
	{ID: "fireworks", Name: "Fireworks AI", Vendor: "Fireworks AI", Hue: 15, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.fireworks.ai/inference/v1", "fw_...", "https://app.fireworks.ai/settings/users/api-keys"), Plans: []ProviderPlan{}},
	{ID: "cerebras", Name: "Cerebras", Vendor: "Cerebras", Hue: 95, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.cerebras.ai/v1", "csk-...", "https://cloud.cerebras.ai/platform/"), Plans: []ProviderPlan{}},
	{ID: "moonshot", Name: "Moonshot", Vendor: "Moonshot AI", Hue: 340, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.moonshot.ai/v1", "sk-...", "https://platform.moonshot.ai/console/api-keys"), Plans: []ProviderPlan{}},
	// MiniMax serves its models over Anthropic Messages; the Token Plans add
	// two Chat Completions models at /v1, the account's endpoint. NeoPi has no
	// MiniMax pay-as-you-go login, so those keys are kept unprobed; Token Plan
	// keys are checked with a one-token Messages request.
	{ID: "minimax", Name: "MiniMax", Vendor: "MiniMax", Hue: 350, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.minimax.io/anthropic", "sk-...", "https://platform.minimax.io/"), Plans: []ProviderPlan{}},
	{ID: "minimax-cn", Name: "MiniMax (China)", Vendor: "MiniMax", Hue: 350, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.minimaxi.com/anthropic", "sk-...", "https://platform.minimaxi.com/"), Plans: []ProviderPlan{}},
	{ID: "minimax-code", Name: "MiniMax Token Plan (International)", Vendor: "MiniMax", Hue: 350, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.minimax.io/v1", "sk-...", "https://platform.minimax.io/subscribe/token-plan"), Plans: []ProviderPlan{}},
	{ID: "minimax-code-cn", Name: "MiniMax Token Plan (China)", Vendor: "MiniMax", Hue: 350, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.minimaxi.com/v1", "sk-...", "https://platform.minimaxi.com/subscribe/token-plan"), Plans: []ProviderPlan{}},
	// Z.ai keys and sign-ins both use the GLM Coding Plan endpoint, as NeoPi
	// does; the general api/paas/v4 endpoint bills pay-as-you-go balance instead.
	{ID: "zai", Name: "Z.ai", Vendor: "Zhipu AI", Hue: 190, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: (&ProviderSubscription{Flow: "code", Callback: "zcode://zai-auth/callback?code=…", Note: "Opens Z.ai's sign-in in a new tab. Z.ai then hands off to its ZCode app, so copy that zcode:// address (or the code in it) and paste it here. NeoProxy creates or reuses the durable API key NeoPi uses, named \"oh-my-pi\"; it never expires."}).at("Z.ai"),
		APIKey:       openAIKey(zaiauth.CodingPlanBaseURL, "32 hex digits, a dot, then more", "https://z.ai/manage-apikey/apikey-list"),
		Plans:        zaiPlans},
	{ID: "nanogpt", Name: "NanoGPT", Vendor: "NanoGPT", Hue: 150, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://nano-gpt.com/api/v1", "sk-...", "https://nano-gpt.com/api"), Plans: []ProviderPlan{}},
	{ID: "alibaba-token-plan", Name: "QwenCloud Token Plan", Vendor: "Qwen", Hue: 267, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey(qwenTokenPlanIntlURL, "sk-sp-... (International or China key)", "https://home.qwencloud.com/billing/subscription/token-plan-individual").
			regional(ProviderRegion{"International", qwenTokenPlanIntlURL}, ProviderRegion{"China (Beijing)", qwenTokenPlanCNURL}),
		Plans: []ProviderPlan{{"lite", "Token Plan Lite", 6}, {"essential", "Token Plan Essential", 10}, {"standard", "Token Plan Standard", 18}, {"pro", "Token Plan Pro", 68}}},
	{ID: "opencode-go", Name: "OpenCode Go", Vendor: "OpenCode", Hue: 242, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey:       openAIKey("https://opencode.ai/zen/go/v1", "oc_... (OpenCode Go key)", "https://opencode.ai/auth"),
		Plans:        []ProviderPlan{{"go", "Go", 10}, {"go-plus", "Go Plus", 40}, {"go-teams", "Go Teams (per member)", 20}},
		dialectRules: openCodeDialectRules, sessionHeader: openCodeSessionHeader, headers: openCodeHeaders},
	{ID: "opencode-zen", Name: "OpenCode Zen", Vendor: "OpenCode", Hue: 160, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey:       openAIKey("https://opencode.ai/zen/v1", "oc_... (OpenCode Zen key)", "https://opencode.ai/auth"),
		Plans:        []ProviderPlan{},
		dialectRules: openCodeDialectRules, sessionHeader: openCodeSessionHeader, headers: openCodeHeaders},
	{ID: "sakana", Name: "Sakana AI", Vendor: "Sakana AI", Hue: 357, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.sakana.ai/v1", "sk-...", "https://console.sakana.ai/api-keys"), Plans: []ProviderPlan{},
		dialect: "responses"},
	{ID: "abliteration", Name: "Abliteration", Vendor: "Abliteration", Hue: 57, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.abliteration.ai/v1", "ak_...", "https://abliteration.ai/console"), Plans: []ProviderPlan{},
		dialect: "responses"},
	{ID: "huggingface", Name: "Hugging Face", Vendor: "Hugging Face", Hue: 85, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://router.huggingface.co/v1", "hf_...", "https://huggingface.co/settings/tokens/new?ownUserPermissions=inference.serverless.write&tokenType=fineGrained"), Plans: []ProviderPlan{}},
	// NVIDIA's free catalogue keeps listing models an account cannot call
	// (404 "Function ... Not found for account") or that reached end of life
	// (410 Gone). Those concern the requested model only, never the whole key.
	{ID: "nvidia", Name: "NVIDIA NIM", Vendor: "NVIDIA", Hue: 135, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://integrate.api.nvidia.com/v1", "nvapi-...", "https://org.ngc.nvidia.com/setup/personal-keys"), Plans: []ProviderPlan{},
		upstreamErrors: []config.RequestScopedErrorRule{
			{Status: 404, Match: []string{"Not Found", "not found"}, Action: "stop"},
			{Status: 410, Match: []string{"Gone", "end of life"}, Action: "stop"},
		}},
	{ID: "vercel-ai-gateway", Name: "Vercel AI Gateway", Vendor: "Vercel", Hue: 300, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://ai-gateway.vercel.sh/v1", "vck_...", "https://vercel.com/d?to=%2F%5Bteam%5D%2F%7E%2Fai-gateway%2Fapi-keys&title=AI+Gateway+API+Keys"), Plans: []ProviderPlan{}},
	{ID: "nous-portal", Name: "Nous Portal", Vendor: "Nous Research", Hue: 320, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens the Nous Portal in a new tab and shows a code to confirm there. NeoProxy keeps the sign-in refreshed.").at("Nous Portal"),
		APIKey:       openAIKey("https://inference-api.nousresearch.com/v1", "sk-...", "https://portal.nousresearch.com/"), Plans: []ProviderPlan{}},
	// Marineris is a self-hosted gateway, so its address is chosen per key;
	// the default is the gateway's own documented listen address.
	{ID: "marineris", Name: "Marineris", Vendor: "Marineris gateway", Hue: 35, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "openai", BaseURL: "http://192.168.50.50:8801/v1", EditableBaseURL: true, KeyHint: "mrn_... (issued by the gateway's administrator)"},
		Plans:  []ProviderPlan{}},
	{ID: "cursor", Name: "Cursor", Vendor: "Anysphere", Hue: 75, Chroma: tintChroma, Methods: []string{"subscription"},
		Subscription: browser("Opens Cursor's sign-in in a new tab; NeoProxy finishes once you approve it there and keeps the sign-in refreshed.").at("Cursor"),
		Plans:        cursorPlans},
	// Umans keys are Anthropic Messages keys sent as X-Api-Key; the account
	// serves the models its /v1/models/info lists, as NeoPi does.
	{ID: "umans", Name: umansauth.Name, Vendor: "Umans", Hue: 280, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "anthropic", BaseURL: umansauth.BaseURL, KeyHint: "sk-...", GetKeyURL: umansauth.KeyPageURL}, Plans: []ProviderPlan{}},
	// NeoPi's Chat Completions key providers: names, key hints and key pages
	// are NeoPi's login rules; base URLs are its bundled catalogs'.
	{ID: "aiand", Name: "ai&", Vendor: "ai&", Hue: 10, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.aiand.com/v1", "sk-...", "https://console.aiand.com/api-keys"), Plans: []ProviderPlan{}},
	{ID: "baseten", Name: "Baseten", Vendor: "Baseten", Hue: 140, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://inference.baseten.co/v1", "bt_...", "https://app.baseten.co/settings/api_keys"), Plans: []ProviderPlan{}},
	{ID: "coralbricks", Name: "CoralBricks", Vendor: "CoralBricks", Hue: 20, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://inference.coralbricks.ai/v1", "cb_...", "https://www.coralbricks.ai/api-keys"), Plans: []ProviderPlan{}},
	{ID: "deepinfra", Name: "DeepInfra", Vendor: "DeepInfra", Hue: 215, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.deepinfra.com/v1/openai", "DeepInfra API key", "https://deepinfra.com/dash/api_keys"), Plans: []ProviderPlan{}},
	{ID: "firepass", Name: "Fire Pass (Fireworks subscription)", Vendor: "Fireworks AI", Hue: 15, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.fireworks.ai/inference/v1", "fpk_...", "https://app.fireworks.ai/settings/users/api-keys"), Plans: []ProviderPlan{}},
	{ID: "gmi-cloud", Name: "GMI Cloud", Vendor: "GMI Cloud", Hue: 180, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.gmi-serving.com/v1", "eyJ...", "https://console.gmicloud.ai"), Plans: []ProviderPlan{}},
	{ID: "helmcode", Name: "Helmcode", Vendor: "Helmcode", Hue: 225, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.helmcode.com/v1", "sk-...", "https://cloud.helmcode.com"), Plans: []ProviderPlan{}},
	{ID: "inception", Name: "Inception", Vendor: "Inception Labs", Hue: 60, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.inceptionlabs.ai/v1", "sk_...", "https://platform.inceptionlabs.ai/dashboard/api-keys"), Plans: []ProviderPlan{}},
	{ID: "novita", Name: "Novita", Vendor: "Novita AI", Hue: 110, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.novita.ai/openai/v1", "sk_...", "https://novita.ai/settings/key-management"), Plans: []ProviderPlan{}},
	{ID: "qianfan", Name: "Qianfan", Vendor: "Baidu", Hue: 235, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://qianfan.baidubce.com/v2", "bce-v3/ALTAK-...", "https://console.bce.baidu.com/qianfan/ais/console/apiKey"), Plans: []ProviderPlan{}},
	{ID: "qwen-portal", Name: "Qwen Portal", Vendor: "Qwen", Hue: 267, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://portal.qwen.ai/v1", "sk-... (Qwen OAuth token or API key)", "https://chat.qwen.ai"), Plans: []ProviderPlan{}},
	{ID: "sambanova", Name: "SambaNova", Vendor: "SambaNova", Hue: 30, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.sambanova.ai/v1", "SambaNova API key", "https://cloud.sambanova.ai/apis"), Plans: []ProviderPlan{}},
	{ID: "stepfun", Name: "StepFun", Vendor: "StepFun", Hue: 200, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.stepfun.ai/v1", "StepFun API key", "https://platform.stepfun.ai/interface-key"), Plans: []ProviderPlan{}},
	{ID: "synthetic", Name: "Synthetic", Vendor: "Synthetic", Hue: 290, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.synthetic.new/openai/v1", "sk-...", "https://dev.synthetic.new/docs/api/overview"), Plans: []ProviderPlan{}},
	// SiliconFlow's two platforms take separate keys; discovery hydrates the
	// bare listing from models.dev and NeoPi's bundled reference index.
	{ID: "siliconflow", Name: "SiliconFlow", Vendor: "SiliconFlow", Hue: 255, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.siliconflow.com/v1", "sk-...", "https://cloud.siliconflow.com/account/ak"), Plans: []ProviderPlan{}},
	{ID: "siliconflow-cn", Name: "SiliconFlow (China)", Vendor: "SiliconFlow", Hue: 255, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.siliconflow.cn/v1", "sk-...", "https://cloud.siliconflow.cn/account/ak"), Plans: []ProviderPlan{}},
	{ID: "venice", Name: "Venice", Vendor: "Venice", Hue: 350, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.venice.ai/api/v1", "vapi_...", "https://venice.ai/settings/api"), Plans: []ProviderPlan{}},
	{ID: "wafer-serverless", Name: "Wafer Serverless (pay-as-you-go)", Vendor: "Wafer", Hue: 40, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://pass.wafer.ai/v1", "wfr_...", "https://app.wafer.ai/usage"), Plans: []ProviderPlan{}},
	{ID: "xiaomi-token-plan-sgp", Name: "Xiaomi Token Plan (Singapore)", Vendor: "Xiaomi", Hue: 30, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://token-plan-sgp.xiaomimimo.com/v1", "tp-...", "https://platform.xiaomimimo.com/console/plan-manage"), Plans: []ProviderPlan{}},
	{ID: "xiaomi-token-plan-ams", Name: "Xiaomi Token Plan (Europe)", Vendor: "Xiaomi", Hue: 30, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://token-plan-ams.xiaomimimo.com/v1", "tp-...", "https://platform.xiaomimimo.com/console/plan-manage"), Plans: []ProviderPlan{}},
	{ID: "xiaomi-token-plan-cn", Name: "Xiaomi Token Plan (China)", Vendor: "Xiaomi", Hue: 30, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://token-plan-cn.xiaomimimo.com/v1", "tp-...", "https://platform.xiaomimimo.com/console/plan-manage"), Plans: []ProviderPlan{}},
	{ID: "yolo-auto", Name: "Yolo-Auto", Vendor: "Yolo-Auto", Hue: 160, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://yolo-auto.com/v1", "yolo_...", "https://yolo-auto.com/app"), Plans: []ProviderPlan{}},
	// ZenMux serves one key over two transports: Chat Completions at /api/v1
	// and, for Claude models, Anthropic Messages at /api/anthropic.
	{ID: "zenmux", Name: "ZenMux", Vendor: "ZenMux", Hue: 280, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://zenmux.ai/api/v1", "sk-...", "https://zenmux.ai/settings/keys"), Plans: []ProviderPlan{}},
	{ID: "zhipu-coding-plan", Name: "Zhipu Coding Plan (智谱)", Vendor: "Zhipu AI", Hue: 190, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://open.bigmodel.cn/api/coding/paas/v4", "<id>.<secret>", "https://bigmodel.cn/coding-plan/personal/overview"), Plans: []ProviderPlan{}},
	// CoreWeave bills each request to the W&B project COREWEAVE_PROJECT (or
	// WANDB_ENTITY/WANDB_PROJECT) names in the server's environment, as NeoPi does.
	{ID: "coreweave", Name: "CoreWeave Serverless Inference", Vendor: "CoreWeave", Hue: 255, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.inference.wandb.ai/v1", "api-key", "https://wandb.ai/settings"), Plans: []ProviderPlan{}},
	{ID: "cline-pass", Name: "ClinePass", Vendor: "Cline", Hue: 205, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.cline.bot/api/v1", "sk_...", "https://app.cline.bot/dashboard/account"), Plans: []ProviderPlan{}},
	// NeoPi takes AIML API keys from AIMLAPI_API_KEY only, so the pasted key is
	// not probed (like Groq and Mistral).
	{ID: "aimlapi", Name: "AIML API", Vendor: "AIML API", Hue: 320, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.aimlapi.com/v1", "AIML API key", "https://aimlapi.com/app/keys"), Plans: []ProviderPlan{}},
	// A Token Plan key (tp-/ttp-) pasted here is probed and listed on the
	// regional clusters, as NeoPi's Xiaomi login does.
	{ID: "xiaomi", Name: "Xiaomi MiMo", Vendor: "Xiaomi", Hue: 30, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.xiaomimimo.com/v1", "sk-..., tp-..., or ttp-...", "https://platform.xiaomimimo.com/#/console/api-keys"), Plans: []ProviderPlan{}},
	// SingularityAPI's two products take disjoint keys: the pay-as-you-go
	// gateway (chat and image models) and the slot-reserved DeepSeek lanes.
	{ID: "singularityapi-dev", Name: "SingularityAPI", Vendor: "SingularityAPI", Hue: 280, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.singularityapi.dev/v1", "sk-sapi-...", "https://app.singularityapi.dev"), Plans: []ProviderPlan{}},
	{ID: "singularityapi-tech", Name: "SingularityAPI Reserved Lanes", Vendor: "SingularityAPI", Hue: 280, Chroma: tintChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.singularityapi.tech/v1", "sk-...", "https://app.singularityapi.tech/compute/billing"), Plans: []ProviderPlan{}},
	{ID: "charm-hyper", Name: "Charm Hyper", Vendor: "Charm", Hue: 330, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://hyper.charm.land/v1", "sk-hyper-...", "https://hyper.charm.land/"), Plans: []ProviderPlan{}},
	// One Command Code Provider API key serves three transports: Claude models
	// over Anthropic Messages, the GPT, DeepSeek and other routed models over
	// Responses, the rest over Chat Completions. NeoPi checks it at the
	// identity route and rejects it only on a 401.
	{ID: "commandcode", Name: "Command Code", Vendor: "Command Code", Hue: 170, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey("https://api.commandcode.ai/provider/v1", "user_...", "https://commandcode.ai/studio"), Plans: []ProviderPlan{}},
	// The Coding Plan sign-in picks the International or China endpoint (or,
	// for an administrator, a custom one) and checks the key there.
	{ID: "alibaba-coding-plan", Name: "Alibaba Coding Plan", Vendor: "Alibaba Cloud", Hue: 25, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey(alibabaCodingPlanIntlURL, "sk-... (International or China key)", "https://modelstudio.console.alibabacloud.com/").
			regional(ProviderRegion{"International", alibabaCodingPlanIntlURL}, ProviderRegion{"China", alibabaCodingPlanCNURL}),
		Plans: []ProviderPlan{}},
	// Kilo's sign-in approves a device code and hands back the gateway's
	// bearer token, which the account keeps like a key; a pasted or imported
	// token is the same key. NeoPi never checks it, so neither does the paste.
	{ID: "kilo", Name: "Kilo Gateway", Vendor: "Kilo", Hue: 50, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: device("Opens Kilo in a new tab and shows a code to confirm there. The token it grants lasts a year.").at("Kilo"),
		APIKey:       openAIKey(kiloauth.BaseURL, "Kilo Gateway token (not checked before it is added)", "https://app.kilo.ai"),
		Plans:        []ProviderPlan{}},
	// Snowflake Cortex serves each customer from its own account host. NeoPi
	// signs in there (OAuth, refreshed) or takes a programmatic access token
	// (PAT) for the account; neither is checked before it is added.
	{ID: "snowflake", Name: "Snowflake Cortex", Vendor: "Snowflake", Hue: 200, Chroma: providerChroma, Methods: []string{"subscription", "apiKey"},
		Subscription: browser("Opens your Snowflake account's sign-in in a new tab. NeoProxy keeps the sign-in refreshed.").at("Snowflake"),
		APIKey:       openAIKey(snowflake.Placeholder+"/api/v2/cortex/v1", "Programmatic access token (not checked before it is added)", "https://docs.snowflake.com/en/user-guide/programmatic-access-tokens"),
		Fields:       []ProviderField{{ID: "account", Label: "Snowflake account", Placeholder: "myorg-myaccount", Hint: "Your account identifier (orgname-accountname) or account URL."}},
		Plans:        []ProviderPlan{}},
	// Cloudflare AI Gateway routes each request through the account's own
	// gateway; NeoPi's login collects the gateway token with both ids and
	// never checks them.
	{ID: cloudflare.Provider, Name: "Cloudflare AI Gateway", Vendor: "Cloudflare", Hue: 55, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: openAIKey(cloudflare.GatewayBase+"/openai", "AI Gateway token, cfut_... (not checked before it is added)", "https://developers.cloudflare.com/ai-gateway/configuration/authentication/"),
		Fields: []ProviderField{
			{ID: "accountId", Label: "Cloudflare account ID", Placeholder: "32-character account ID", Hint: "Shown on your Cloudflare dashboard's account overview."},
			{ID: "gatewayId", Label: "AI Gateway ID", Placeholder: "default", Hint: "The name of the gateway under AI → AI Gateway."},
		},
		Plans: []ProviderPlan{}},
	// Local OpenAI-compatible servers: an administrator names the server, and
	// an empty key stands for NeoPi's no-auth placeholder.
	{ID: "vllm", Name: "vLLM", Vendor: "vLLM", Hue: 60, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "openai", BaseURL: "http://127.0.0.1:8000/v1", EditableBaseURL: true, KeyHint: "Leave empty unless the server requires a key", GetKeyURL: "https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html", KeyOptional: true},
		Plans:  []ProviderPlan{}},
	{ID: "lm-studio", Name: "LM Studio", Vendor: "LM Studio", Hue: 220, Chroma: providerChroma, Methods: []string{"apiKey"},
		APIKey: &ProviderAPIKey{Style: "openai", BaseURL: "http://127.0.0.1:1234/v1", EditableBaseURL: true, KeyHint: "Leave empty unless the server requires a key", KeyOptional: true},
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

// catalogRegions resolves a catalogue provider's regional endpoints (nil for
// single-endpoint providers). Tests may replace it like catalogBaseURL.
var catalogRegions = func(id string) []string {
	p, ok := catalogProvider(id)
	if !ok || p.APIKey == nil {
		return nil
	}
	urls := make([]string, 0, len(p.APIKey.Regions))
	for _, region := range p.APIKey.Regions {
		urls = append(urls, region.BaseURL)
	}
	return urls
}

// regionChoice is the endpoint a regional key was entered for: one of the
// catalogue's regions, or (Custom) any URL an administrator named. Empty
// means the default region.
type regionChoice struct {
	BaseURL string
	Custom  bool
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
	case "kimi", "kimi-code", "kimi-coding":
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
