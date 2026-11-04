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
		subscription: { flow: "device", note: "Shows a code to confirm on xAI's site." },
		apiKey: { style: "openai", baseUrl: "https://api.x.ai/v1", keyHint: "xai-…", getKeyUrl: "https://console.x.ai" },
		plans: [
			{ id: "supergrok", name: "SuperGrok", monthlyUSD: 30 },
			{ id: "heavy", name: "SuperGrok Heavy", monthlyUSD: 300 },
		],
	},
	{ id: "kimi", name: "Kimi", vendor: "Moonshot", hue: 340, methods: ["subscription"], subscription: { flow: "device", note: "Shows a code to confirm on Kimi's site." } },
	{ id: "devin", name: "Devin", vendor: "Cognition", hue: 205, methods: ["subscription"], subscription: { flow: "browser", note: "Opens Devin's sign-in page in a new tab." } },
	{ id: "meta", name: "Meta AI", vendor: "Meta", hue: 280, methods: ["subscription"], subscription: { flow: "device", note: "Shows a code to confirm on Meta's site." } },
	{ id: "openrouter", name: "OpenRouter", vendor: "OpenRouter", hue: 300, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://openrouter.ai/api/v1", keyHint: "sk-or-…", getKeyUrl: "https://openrouter.ai/keys" } },
	{ id: "deepseek", name: "DeepSeek", vendor: "DeepSeek", hue: 230, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.deepseek.com/v1", keyHint: "sk-…", getKeyUrl: "https://platform.deepseek.com/api_keys" } },
	{ id: "groq", name: "Groq", vendor: "Groq", hue: 25, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.groq.com/openai/v1", keyHint: "gsk_…", getKeyUrl: "https://console.groq.com/keys" } },
	{ id: "mistral", name: "Mistral", vendor: "Mistral AI", hue: 70, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.mistral.ai/v1", keyHint: "…", getKeyUrl: "https://console.mistral.ai/api-keys" } },
	{ id: "together", name: "Together", vendor: "Together AI", hue: 120, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.together.xyz/v1", keyHint: "…", getKeyUrl: "https://api.together.ai/settings/api-keys" } },
	{ id: "fireworks", name: "Fireworks", vendor: "Fireworks AI", hue: 15, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.fireworks.ai/inference/v1", keyHint: "fw_…", getKeyUrl: "https://fireworks.ai/account/api-keys" } },
	{ id: "cerebras", name: "Cerebras", vendor: "Cerebras", hue: 95, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.cerebras.ai/v1", keyHint: "csk-…", getKeyUrl: "https://cloud.cerebras.ai" } },
	{ id: "moonshot", name: "Moonshot API", vendor: "Moonshot", hue: 340, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.moonshot.ai/v1", keyHint: "sk-…", getKeyUrl: "https://platform.moonshot.ai/console/api-keys" } },
	{ id: "zai", name: "Z.ai", vendor: "Zhipu", hue: 190, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://api.z.ai/api/paas/v4", keyHint: "…", getKeyUrl: "https://z.ai/manage-apikey/apikey-list" } },
	{ id: "nanogpt", name: "NanoGPT", vendor: "NanoGPT", hue: 150, methods: ["apiKey"], apiKey: { style: "openai", baseUrl: "https://nano-gpt.com/api/v1", keyHint: "…", getKeyUrl: "https://nano-gpt.com/api" } },
	{
		id: "cursor",
		name: "Cursor",
		vendor: "Anysphere",
		hue: 0,
		neutral: true,
		methods: ["subscription"],
		subscription: { flow: "browser", note: "Opens Cursor's sign-in page in a new tab." },
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

// Provider color follows the theme's lightness/chroma so data colors stay
// harmonious on both chassis. Neutral providers (xAI, custom) are near-grey.
export function providerColor(id: string, hue?: number, alpha?: number) {
	const info = providerInfo(id);
	const h = hue ?? info.hue;
	const c = info.neutral || info.chroma === 0 ? "0.02" : "var(--provider-c)";
	return `oklch(var(--provider-l) ${c} ${h}${alpha !== undefined ? ` / ${alpha}` : ""})`;
}

export function planFor(provider: string, planId: string | undefined, catalog?: ProviderInfo[]) {
	if (!planId) return undefined;
	return providerInfo(provider, catalog).plans?.find((p) => p.id === planId);
}
