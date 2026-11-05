// Runs NeoPi's OWN parseFactoryDroidUsage over a corpus of
// GET /api/billing/limits payloads at a fixed fetch time and writes
// testdata/neopi_factory_usage.json: [{name, payload, report}].
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_neopi_factory_usage.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { parseFactoryDroidUsage } = await import(root + "/pi-ai/src/usage/factory-droid.ts");

const fetchedAt = Date.parse("2026-11-05T12:00:00.000Z");
const future = (hours: number) => new Date(fetchedAt + hours * 3600_000).toISOString();
const past = (hours: number) => new Date(fetchedAt - hours * 3600_000).toISOString();
const corpus: Record<string, unknown> = {
	"both-pools": {
		limits: {
			standard: { fiveHour: { usedPercent: 12.5, windowEnd: future(2) }, weekly: { usedPercent: 40, windowEnd: future(100) }, monthly: { usedPercent: 3, windowEnd: future(400) } },
			core: { fiveHour: { usedPercent: 0, windowEnd: future(4) }, weekly: { usedPercent: "77", windowEnd: future(50) } },
		},
		extraUsageBalanceCents: 1250,
	},
	"lapsed-window": { limits: { standard: { fiveHour: { usedPercent: 100, windowEnd: past(1) }, weekly: { usedPercent: 140, windowEnd: future(10) } } } },
	"no-window-end": { limits: { core: { fiveHour: { usedPercent: 50 }, weekly: { usedPercent: -5, windowEnd: future(1) } } } },
	"bad-percent": { limits: { standard: { fiveHour: { usedPercent: "x", windowEnd: future(1) }, weekly: { usedPercent: null } } } },
	"no-token-billing": { usesTokenRateLimitsBilling: false, limits: { standard: { fiveHour: { usedPercent: 1, windowEnd: future(1) } } } },
	"balance-only": { limits: {}, extraUsageBalanceCents: 99 },
	"zero-balance": { limits: {}, extraUsageBalanceCents: 0 },
	"no-limits": { extraUsageBalanceCents: 500 },
	"not-object": [1, 2],
};

const cases = Object.entries(corpus).map(([name, payload]) => ({ name, payload, report: parseFactoryDroidUsage(payload, fetchedAt) }));
await Bun.write(join(import.meta.dir, "neopi_factory_usage.json"), JSON.stringify({ source: "NeoPi parseFactoryDroidUsage", fetchedAt, cases }, null, "\t") + "\n");
console.log(`${cases.length} payloads`);
