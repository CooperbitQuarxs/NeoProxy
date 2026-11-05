// Exports the data NeoPi's catalog compat engine resolves model policy from
// (pi-catalog compat/rules.json cascade, hosts.ts KNOWN_HOSTS, and the wire
// axis keys axes.ts applies to an openai-completions compat record) as
// internal/neopicompat/compat_rules.json.gz.
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks export_compat_rules.ts
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json", { with: { type: "json" } })).default as any;
const { AXES, API_COMPAT_RECORDS } = await import(root + "/pi-catalog/src/compat/axes.ts");
const { KNOWN_HOSTS } = await import(root + "/pi-catalog/src/hosts.ts");

const completionsRecords: string[] = API_COMPAT_RECORDS["openai-completions"];
const completionsWireKeys = Object.values(AXES as Record<string, any>)
	.filter(axis => axis.set === "wire" && axis.records?.some((record: string) => completionsRecords.includes(record)))
	.map(axis => axis.key);

const out = join(dirname(new URL(import.meta.url).pathname), "..", "compat_rules.json.gz");
writeFileSync(
	out,
	Bun.gzipSync(
		JSON.stringify({
			source: "NeoPi OWN pi-catalog compat/rules.json cascade, hosts.ts KNOWN_HOSTS, axes.ts openai-completions wire keys",
			cascade: rules.cascade.rules,
			hosts: KNOWN_HOSTS,
			completionsWireKeys: [...new Set(completionsWireKeys)],
		}),
	),
);
console.log(`wrote ${rules.cascade.rules.length} rules and ${completionsWireKeys.length} completions wire keys to ${out}`);
