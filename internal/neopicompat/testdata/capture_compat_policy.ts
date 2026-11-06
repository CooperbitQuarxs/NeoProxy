// Runs NeoPi's OWN resolveModelPolicy (pi-catalog compat/resolve.ts) over
// openai-completions, openai-responses, anthropic-messages and
// bedrock-converse-stream specs derived from every bundled row of those APIs: the
// row as authored (its configured compat, no thinking), the row reported as
// non-reasoning, the row with its ladder authored explicitly, and two ids the
// bundle lacks (a namespaced copy and a next revision). Records the compat,
// thinking and reasoning NeoPi resolves, or the error it throws.
//
// Writes policy_capture.json.gz beside this script. Run from the NeoPi env:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_compat_policy.ts
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels, getBundledProviders } = await import(root + "/pi-catalog/src/models.ts");
const { resolveModelPolicy } = await import(root + "/pi-catalog/src/compat/resolve.ts");

const bumpRevision = (id: string): string | undefined => {
	const match = /(\d+)(?!.*\d)/.exec(id);
	if (!match) return undefined;
	return id.slice(0, match.index) + String(Number(match[1]) + 1) + id.slice(match.index + match[1].length);
};

const compats: any[] = [];
const compatKeys = new Map<string, number>();
const compatIndex = (compat: any): number => {
	const key = JSON.stringify(compat ?? null);
	let index = compatKeys.get(key);
	if (index === undefined) {
		index = compats.length;
		compats.push(compat ?? null);
		compatKeys.set(key, index);
	}
	return index;
};

const cases: any[] = [];
const run = (label: string, spec: any) => {
	try {
		const policy = resolveModelPolicy(spec);
		cases.push({
			label,
			spec,
			compatIndex: compatIndex(policy.compat),
			thinking: policy.thinking ?? null,
			reasoning: Boolean(spec.reasoning) || policy.thinking !== undefined,
		});
	} catch (error) {
		cases.push({ label, spec, error: String((error as Error).message) });
	}
};

let rows = 0;
for (const provider of getBundledProviders()) {
	for (const model of getBundledModels(provider) as any[]) {
		if (model.api !== "openai-completions" && model.api !== "openai-responses" && model.api !== "anthropic-messages" && model.api !== "bedrock-converse-stream") continue;
		rows++;
		const base = {
			id: model.id,
			name: model.name,
			api: model.api,
			provider: model.provider,
			baseUrl: model.baseUrl,
			reasoning: model.reasoning,
			input: model.input,
			...(model.compatConfig ? { compat: model.compatConfig } : {}),
		};
		run("bundled", base);
		if (model.reasoning) run("non-reasoning", { ...base, reasoning: false });
		if (model.thinking?.efforts?.length) {
			const { effortRouting: _routing, ...thinking } = model.thinking;
			run("explicit-thinking", { ...base, thinking });
		}
		run("namespaced", { ...base, id: `zz-lab/${model.id}` });
		const bumped = bumpRevision(model.id);
		if (bumped) run("next-revision", { ...base, id: bumped });
		// A gateway path that names a host class (here Fireworks, whose effort
		// default is Chat Completions only) on every API.
		const gateway = { ...base, baseUrl: "http://127.0.0.1:8080/fireworks.ai/api/v1" };
		run("fireworks-path", gateway);
		if (model.thinking?.efforts?.length) {
			const { effortRouting: _routing, ...thinking } = model.thinking;
			run("fireworks-path-explicit-thinking", { ...gateway, thinking });
		}
	}
}

const out = join(dirname(new URL(import.meta.url).pathname), "policy_capture.json.gz");
writeFileSync(out, Bun.gzipSync(JSON.stringify({ source: "NeoPi OWN resolveModelPolicy", compats, cases })));
console.log(`${rows} rows, ${cases.length} cases (${cases.filter(c => c.error).length} errors), ${compats.length} compats -> ${out}`);
