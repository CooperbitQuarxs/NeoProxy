import type { ProviderInfo } from "../api/types";

// Local fallback catalogue. The server's /v8/neo/access providers list wins when
// present; this keeps names and colors stable while it loads or on older servers.
export const FALLBACK_PROVIDERS: ProviderInfo[] = [
	{
		id: "codex",
		name: "ChatGPT / Codex",
		vendor: "OpenAI",
		hue: 165,
		methods: ["subscription"],
		subscription: { flow: "browser", note: "Opens OpenAI's sign-in page in a new tab." },
		plans: [
			{ id: "free", name: "ChatGPT Free", monthlyUSD: 0 },
			{ id: "plus", name: "ChatGPT Plus", monthlyUSD: 20 },
			{ id: "pro", name: "ChatGPT Pro", monthlyUSD: 200 },
			{ id: "team", name: "ChatGPT Team", monthlyUSD: 30 },
			{ id: "business", name: "ChatGPT Business", monthlyUSD: 30 },
			{ id: "enterprise", name: "ChatGPT Enterprise", monthlyUSD: 60 },
		],
	},
	{
		id: "openai",
		name: "OpenAI API",
		vendor: "OpenAI",
		hue: 165,
		methods: ["apiKey"],
		apiKey: { style: "openai", baseUrl: "https://api.openai.com/v1", keyHint: "sk-…", getKeyUrl: "https://platform.openai.com/api-keys" },
	},
	{
		id: "claude",
		name: "Claude",
		vendor: "Anthropic",
		hue: 45,
		methods: ["subscription", "apiKey"],
		subscription: { flow: "browser", note: "Opens Claude's sign-in page in a new tab." },
		apiKey: { style: "anthropic", baseUrl: "https://api.anthropic.com", keyHint: "sk-ant-…", getKeyUrl: "https://console.anthropic.com/settings/keys" },
		plans: [
			{ id: "pro", name: "Claude Pro", monthlyUSD: 20 },
			{ id: "max5", name: "Claude Max 5x", monthlyUSD: 100 },
			{ id: "max20", name: "Claude Max 20x", monthlyUSD: 200 },
		],
	},
	{
		id: "gemini",
		name: "Gemini",
		vendor: "Google",
		hue: 255,
		methods: ["apiKey"],
		apiKey: { style: "gemini", baseUrl: "https://generativelanguage.googleapis.com", keyHint: "AIza…", getKeyUrl: "https://aistudio.google.com/apikey" },
	},
	{
		id: "antigravity",
		name: "Antigravity",
		vendor: "Google",
		hue: 255,
		methods: ["subscription"],
		subscription: { flow: "browser", note: "Opens Google's sign-in page in a new tab." },
		plans: [
			{ id: "pro", name: "Google AI Pro", monthlyUSD: 20 },
			{ id: "ultra", name: "Google AI Ultra", monthlyUSD: 250 },
		],
	},
	{
		id: "xai",
		name: "Grok",
		vendor: "xAI",
		hue: 280,
		neutral: true,
		methods: ["subscription", "apiKey"],
		subscription: { flow: "device", note: "Opens x.ai in a new tab and shows a code to confirm there. Requests go to the official xAI API." },
		apiKey: { style: "openai", baseUrl: "https://api.x.ai/v1", keyHint: "xai-…", getKeyUrl: "https://console.x.ai" },
		plans: [
			{ id: "supergrok", name: "SuperGrok", monthlyUSD: 30 },
			{ id: "heavy", name: "SuperGrok Heavy", monthlyUSD: 300 },
		],
	},
	{
		id: "grok-build",
		name: "Grok Build",
		vendor: "xAI",
		hue: 250,
		chroma: 0.045,
		methods: ["subscription"],
		subscription: { flow: "device", note: "Opens x.ai in a new tab and shows a code to confirm there. Requests go to Grok Build, the Grok CLI's endpoint, with its own speed, usage limits and models." },
		plans: [
			{ id: "supergrok", name: "SuperGrok", monthlyUSD: 30 },
			{ id: "supergrok-heavy", name: "SuperGrok Heavy", monthlyUSD: 300 },
		],
	},
	{ id: "kimi", name: "Kimi", vendor: "Moonshot", hue: 340, methods: ["subscription"], subscription: { flow: "device", note: "Shows a code to confirm on Kimi's site.", site: "Kimi" } },
	{ id: "devin", name: "Devin", vendor: "Cognition", hue: 205, methods: ["subscription"], subscription: { flow: "browser", note: "Opens Devin's sign-in page in a new tab.", site: "Devin" } },
	{ id: "meta", name: "Meta AI", vendor: "Meta", hue: 280, methods: ["subscription"], subscription: { flow: "device", note: "Shows a code to confirm on Meta's site." } },
	{ id: "openrouter", name: "OpenRouter", vendor: "OpenRouter", hue: 300, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://openrouter.ai/api/v1", keyHint: "sk-or-…", getKeyUrl: "https://openrouter.ai/keys" } },
	{ id: "deepseek", name: "DeepSeek", vendor: "DeepSeek", hue: 230, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.deepseek.com/v1", keyHint: "sk-…", getKeyUrl: "https://platform.deepseek.com/api_keys" } },
	{ id: "groq", name: "Groq", vendor: "Groq", hue: 25, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.groq.com/openai/v1", keyHint: "gsk_…", getKeyUrl: "https://console.groq.com/keys" } },
	{ id: "mistral", name: "Mistral", vendor: "Mistral AI", hue: 70, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.mistral.ai/v1", keyHint: "…", getKeyUrl: "https://console.mistral.ai/api-keys" } },
	{ id: "together", name: "Together", vendor: "Together AI", hue: 120, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.together.xyz/v1", keyHint: "…", getKeyUrl: "https://api.together.ai/settings/api-keys" } },
	{ id: "fireworks", name: "Fireworks", vendor: "Fireworks AI", hue: 15, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.fireworks.ai/inference/v1", keyHint: "fw_…", getKeyUrl: "https://fireworks.ai/account/api-keys" } },
	{ id: "cerebras", name: "Cerebras", vendor: "Cerebras", hue: 95, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.cerebras.ai/v1", keyHint: "csk-…", getKeyUrl: "https://cloud.cerebras.ai" } },
	{ id: "moonshot", name: "Moonshot API", vendor: "Moonshot", hue: 340, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.moonshot.ai/v1", keyHint: "sk-…", getKeyUrl: "https://platform.moonshot.ai/console/api-keys" } },
	{
		id: "zai",
		name: "Z.ai",
		vendor: "Zhipu AI",
		hue: 190,
		methods: ["subscription", "apiKey"],
		subscription: { flow: "code", note: "Opens Z.ai's sign-in in a new tab. Z.ai then hands off to its ZCode app, so copy that zcode:// address (or the code in it) and paste it here. NeoProxy creates a durable API key named \"neoproxy\" on the account; it never expires.", site: "Z.ai" },
		apiKey: { style: "openai", baseUrl: "https://api.z.ai/api/coding/paas/v4", keyHint: "32 hex digits, a dot, then more", getKeyUrl: "https://z.ai/manage-apikey/apikey-list" },
		plans: [
			{ id: "lite", name: "GLM Coding Plan Lite", monthlyUSD: 18 },
			{ id: "pro", name: "GLM Coding Plan Pro", monthlyUSD: 80 },
			{ id: "max", name: "GLM Coding Plan Max", monthlyUSD: 168 },
		],
	},
	{ id: "nanogpt", name: "NanoGPT", vendor: "NanoGPT", hue: 150, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://nano-gpt.com/api/v1", keyHint: "…", getKeyUrl: "https://nano-gpt.com/api" } },
	{
		id: "alibaba-token-plan",
		name: "QwenCloud Token Plan",
		vendor: "Qwen",
		hue: 267,
		methods: ["apiKey"],
		apiKey: { style: "openai", baseUrl: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", keyHint: "sk-sp-… (International or China key)", getKeyUrl: "https://home.qwencloud.com/billing/subscription/token-plan-individual" },
		plans: [
			{ id: "lite", name: "Token Plan Lite", monthlyUSD: 6 },
			{ id: "essential", name: "Token Plan Essential", monthlyUSD: 10 },
			{ id: "standard", name: "Token Plan Standard", monthlyUSD: 18 },
			{ id: "pro", name: "Token Plan Pro", monthlyUSD: 68 },
		],
	},
	{
		id: "opencode-go",
		name: "OpenCode Go",
		vendor: "OpenCode",
		hue: 242,
		methods: ["apiKey"],
		apiKey: { style: "openai", baseUrl: "https://opencode.ai/zen/go/v1", keyHint: "oc_… (OpenCode Go key)", getKeyUrl: "https://opencode.ai/auth" },
		plans: [
			{ id: "go", name: "Go", monthlyUSD: 10 },
			{ id: "go-plus", name: "Go Plus", monthlyUSD: 40 },
			{ id: "go-teams", name: "Go Teams (per member)", monthlyUSD: 20 },
		],
	},
	{ id: "opencode-zen", name: "OpenCode Zen", vendor: "OpenCode", hue: 160, chroma: 0.045, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://opencode.ai/zen/v1", keyHint: "oc_… (OpenCode Zen key)", getKeyUrl: "https://opencode.ai/auth" } },
	{ id: "sakana", name: "Sakana AI", vendor: "Sakana AI", hue: 357, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.sakana.ai/v1", keyHint: "fish_…", getKeyUrl: "https://console.sakana.ai/api-keys" } },
	{ id: "abliteration", name: "Abliteration", vendor: "Abliteration", hue: 57, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.abliteration.ai/v1", keyHint: "ak_…", getKeyUrl: "https://abliteration.ai/console" } },
	{ id: "huggingface", name: "Hugging Face", vendor: "Hugging Face", hue: 85, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://router.huggingface.co/v1", keyHint: "hf_…", getKeyUrl: "https://huggingface.co/settings/tokens/new?ownUserPermissions=inference.serverless.write&tokenType=fineGrained" } },
	{ id: "nvidia", name: "NVIDIA NIM", vendor: "NVIDIA", hue: 135, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://integrate.api.nvidia.com/v1", keyHint: "nvapi-…", getKeyUrl: "https://build.nvidia.com/settings/api-keys" } },
	{ id: "vercel-ai-gateway", name: "Vercel AI Gateway", vendor: "Vercel", hue: 300, chroma: 0.045, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://ai-gateway.vercel.sh/v1", keyHint: "vck_…", getKeyUrl: "https://vercel.com/d?to=%2F%5Bteam%5D%2F%7E%2Fai-gateway%2Fapi-keys&title=AI+Gateway+API+Keys" } },
	{ id: "nous-portal", name: "Nous Portal", vendor: "Nous Research", hue: 320, methods: ["subscription", "apiKey"], subscription: { flow: "device", note: "Shows a code to confirm on the Nous Portal.", site: "Nous Portal" }, apiKey: { style: "openai", baseUrl: "https://inference-api.nousresearch.com/v1", keyHint: "sk-…", getKeyUrl: "https://portal.nousresearch.com/" } },
	{ id: "marineris", name: "Marineris", vendor: "Marineris gateway", hue: 35, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "http://192.168.50.50:8801/v1", editableBaseUrl: true, keyHint: "mrn_… (issued by the gateway's administrator)" } },
	{
		id: "cursor",
		name: "Cursor",
		vendor: "Anysphere",
		hue: 75,
		chroma: 0.045,
		methods: ["subscription"],
		subscription: { flow: "browser", note: "Opens Cursor's sign-in page in a new tab.", site: "Cursor" },
		plans: [
			{ id: "plus", name: "Cursor Plus", monthlyUSD: 8 },
			{ id: "premium", name: "Cursor Premium", monthlyUSD: 30 },
			{ id: "super", name: "Cursor Super", monthlyUSD: 100 },
			{ id: "ultra", name: "Cursor Ultra", monthlyUSD: 200 },
			{ id: "pro", name: "Cursor Pro (legacy)", monthlyUSD: 20 },
			{ id: "pro-plus", name: "Cursor Pro+ (legacy)", monthlyUSD: 60 },
		],
	},
	{ id: "custom", name: "Other OpenAI-compatible server", vendor: "Custom", hue: 0, neutral: true, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "", editableBaseUrl: true, keyHint: "Leave empty if the server needs none" } },
];

// Engine provider ids that appear in older vault responses.
const ALIASES: Record<string, string> = { anthropic: "claude", "gemini-cli": "gemini", "kimi-ai": "kimi" };

export function canonicalProvider(id: string) {
	return ALIASES[id] ?? id;
}

export function providerInfo(id: string, catalog: ProviderInfo[] = FALLBACK_PROVIDERS): ProviderInfo {
	const key = canonicalProvider(id);
	return (
		catalog.find((p) => p.id === key) ??
		FALLBACK_PROVIDERS.find((p) => p.id === key) ?? { id: key, name: key.charAt(0).toUpperCase() + key.slice(1), vendor: key, hue: 0, neutral: true, methods: [] }
	);
}

// The catalogue's full provider chroma; smaller values are tints of it.
const FULL_CHROMA = 0.13;

// Provider color follows the theme's lightness/chroma so data colors stay
// harmonious on both chassis. Neutral providers (xAI, custom) are near-grey;
// monochrome brands (Cursor, OpenCode Zen, Vercel) get a faint tint so they
// stay apart in one pool.
export function providerColor(id: string, hue?: number, alpha?: number) {
	const info = providerInfo(id);
	const h = hue ?? info.hue;
	const chroma = info.chroma;
	const c =
		info.neutral || chroma === 0
			? "0.02"
			: chroma !== undefined && chroma < FULL_CHROMA
				? `calc(var(--provider-c) * ${+(chroma / FULL_CHROMA).toFixed(3)})`
				: "var(--provider-c)";
	return `oklch(var(--provider-l) ${c} ${h}${alpha !== undefined ? ` / ${alpha}` : ""})`;
}

export function planFor(provider: string, planId: string | undefined, catalog?: ProviderInfo[]) {
	if (!planId) return undefined;
	return providerInfo(provider, catalog).plans?.find((p) => p.id === planId);
}
