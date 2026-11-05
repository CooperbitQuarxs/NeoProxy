// Runs NeoPi's OWN umans api-key login (auth rule umans, validate
// "anthropic-messages") through a fake fetch and records the probe it sends.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { validateAnthropicCompatibleApiKey } = await import(root + "/pi-ai/src/registry/api-key-validation.ts");

const apiKey = "sk-umans-key-capture-0000";
let request: any;
const fetch = (async (input: any, init: any) => {
	request = { url: String(input), method: init?.method, headers: Object.fromEntries(new Headers(init?.headers)), body: init?.body };
	return new Response("{}", { status: 200 });
}) as typeof globalThis.fetch;
// The rule's validate node: base-url and model as declared in rules/auth/umans.kdl.
await validateAnthropicCompatibleApiKey({ provider: "Umans AI Coding Plan", apiKey, baseUrl: "https://api.code.umans.ai", model: "umans-coder", fetch });
console.log(JSON.stringify({ source: "NeoPi OWN validateAnthropicCompatibleApiKey for auth rule umans", apiKey, request }, null, 2));
