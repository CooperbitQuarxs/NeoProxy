// Exports NeoPi's compiled `login "api-key"` rules (catalog/src/compat/rules.json
// auth.providers) for the providers NeoProxy serves through NeoPi's OpenAI
// Chat Completions provider, as internal/auth/neopikeys/logins.json. A provider
// whose keys come only from the environment (no api-key login) is exported with
// its name alone.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks export_neopi_logins.ts > ../logins.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;

const served = [
	"cerebras", "deepseek", "fireworks", "groq", "huggingface", "mistral", "moonshot", "nanogpt", "nvidia", "together",
	"aiand", "baseten", "coralbricks", "deepinfra", "firepass", "gmi-cloud", "helmcode", "inception", "novita", "qianfan",
	"qwen-portal", "sambanova", "stepfun", "synthetic", "venice", "wafer-serverless", "xiaomi-token-plan-sgp",
	"xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "yolo-auto", "zhipu-coding-plan", "coreweave", "cline-pass", "aimlapi",
	"singularityapi-dev", "singularityapi-tech", "charm-hyper", "siliconflow", "siliconflow-cn", "vllm", "lm-studio",
];
const byId = new Map<string, any>(rules.auth.providers.map((p: any) => [p.id, p]));
const names: Record<string, string> = { groq: "Groq", mistral: "Mistral" };
const logins: Record<string, any> = {};
for (const id of served) {
	const rule = byId.get(id);
	if (!rule || rule.login?.kind !== "api-key") {
		if (!names[id] && !rule?.name) throw new Error(`no auth rule or name for ${id}`);
		logins[id] = { name: rule?.name ?? names[id] };
		continue;
	}
	const v = rule.login.validate;
	// neopikeys implements these hooks (registry/hooks/api-key.ts) itself.
	const portedHooks = ["coreweave-project", "cline-pass-client"];
	if (v?.headersHook && !portedHooks.includes(v.headersHook)) throw new Error(`${id}: validation headers hook ${v.headersHook} is not ported`);
	logins[id] = {
		name: rule.name,
		...(rule.login.normalize ? { normalize: rule.login.normalize } : {}),
		...(v ? { validate: v } : {}),
		...(rule.login.emptyFallback !== undefined ? { emptyFallback: rule.login.emptyFallback } : {}),
	};
}
console.log(JSON.stringify({ source: "NeoPi rules.json auth.providers (login api-key)", logins }, null, "\t"));
