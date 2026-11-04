// Provider marks from LobeHub's MIT-licensed icon set (@lobehub/icons-static-svg).
// Monochrome SVGs, inlined by Vite and drawn as a mask so they take the ink color.
import anthropic from "@lobehub/icons-static-svg/icons/anthropic.svg";
import antigravity from "@lobehub/icons-static-svg/icons/antigravity.svg";
import cerebras from "@lobehub/icons-static-svg/icons/cerebras.svg";
import claude from "@lobehub/icons-static-svg/icons/claude.svg";
import deepseek from "@lobehub/icons-static-svg/icons/deepseek.svg";
import devin from "@lobehub/icons-static-svg/icons/devin.svg";
import fireworks from "@lobehub/icons-static-svg/icons/fireworks.svg";
import gemini from "@lobehub/icons-static-svg/icons/gemini.svg";
import google from "@lobehub/icons-static-svg/icons/google.svg";
import grok from "@lobehub/icons-static-svg/icons/grok.svg";
import groq from "@lobehub/icons-static-svg/icons/groq.svg";
import kimi from "@lobehub/icons-static-svg/icons/kimi.svg";
import meta from "@lobehub/icons-static-svg/icons/meta.svg";
import mistral from "@lobehub/icons-static-svg/icons/mistral.svg";
import moonshot from "@lobehub/icons-static-svg/icons/moonshot.svg";
import ollama from "@lobehub/icons-static-svg/icons/ollama.svg";
import openai from "@lobehub/icons-static-svg/icons/openai.svg";
import openrouter from "@lobehub/icons-static-svg/icons/openrouter.svg";
import qwen from "@lobehub/icons-static-svg/icons/qwen.svg";
import together from "@lobehub/icons-static-svg/icons/together.svg";
import zai from "@lobehub/icons-static-svg/icons/zai.svg";

const LOGOS: Record<string, string> = {
	codex: openai,
	openai,
	"openai-codex": openai,
	claude,
	anthropic: claude,
	"anthropic-api": anthropic,
	gemini,
	google,
	"google-antigravity": antigravity,
	antigravity,
	xai: grok,
	"xai-oauth": grok,
	grok,
	kimi,
	"kimi-code": kimi,
	moonshot,
	devin,
	meta,
	openrouter,
	deepseek,
	groq,
	mistral,
	together,
	fireworks,
	cerebras,
	zai,
	qwen,
	"alibaba-token-plan": qwen,
	zhipu: zai,
	ollama,
};

export function providerLogo(id: string): string | undefined {
	return LOGOS[id] ?? LOGOS[id.toLowerCase()];
}
