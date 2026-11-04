import { canonicalProvider, providerColor, providerInfo } from "../../lib/providers";
import type { BudgetWindow, Draw, NeoAccount, NeoModel, NeoProvider, Stage, Terminal, TerminalKind, VirtualModel, VirtualModelDraft } from "./data";

export const NAME_RE = /^[a-z][a-z0-9-]{0,31}$/;
export const LUA_NAME_RE = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;

// NeoPi reports its own provider ids for shared models; map them onto ours so
// shared stages wear the same color as the provider they come from.
const NEOPI_VENDORS: Record<string, string> = {
	"openai-codex": "codex",
	anthropic: "claude",
	"google-antigravity": "antigravity",
	"google-gemini-cli": "gemini",
	google: "gemini",
	"kimi-code": "kimi",
};

export type ProviderLabel = { name: string; source?: string; colorKey: string; shared: boolean };

export function labelProvider(id: string, providers: NeoProvider[] | undefined): ProviderLabel {
	const p = providers?.find((x) => x.id === id);
	if (p?.kind === "neopi-gateway") {
		const vendor = canonicalProvider(NEOPI_VENDORS[p.vendor] ?? p.vendor);
		return { name: providerInfo(vendor).name, source: p.sourceLabel || "a NeoPi", colorKey: vendor, shared: true };
	}
	const key = canonicalProvider(id);
	return { name: providerInfo(key).name, colorKey: key, shared: false };
}

export const colorOf = (label: ProviderLabel, alpha?: number) => providerColor(label.colorKey, undefined, alpha);

export const providerOfModel = (modelId: string, models?: NeoModel[]) => models?.find((m) => m.id === modelId)?.providerId ?? modelId.split("/")[0];
export const modelName = (modelId: string | undefined, models?: NeoModel[]) => (modelId ? models?.find((m) => m.id === modelId)?.display ?? modelId.split("/").slice(1).join("/") ?? modelId : "");

/** Generated 1:1 routes for a model shared from a NeoPi (neoroute/source_models.go). */
export function isGenerated(vm: VirtualModel, models?: NeoModel[]) {
	if (vm.stages.length !== 1 || vm.stages[0].id !== "source") return false;
	const m = models?.find((x) => x.id === vm.stages[0].model);
	return m ? !!m.virtualOnly : true;
}

export function stagePool(stage: Stage, accounts: NeoAccount[] | undefined, models?: NeoModel[]) {
	const provider = providerOfModel(stage.model, models);
	const all = (accounts ?? []).filter((a) => a.providerId === provider);
	return { all, chosen: stage.accounts === "all" ? all : all.filter((a) => (stage.accounts as string[]).includes(a.id)) };
}

/** Accounts a stage can measure: a stage only serves from accounts with a configured window of its kind. */
export function windowGap(stage: Stage, chosen: NeoAccount[]) {
	const measured = chosen.filter((a) => a.windows?.some((w) => w.kind === stage.window && w.known));
	const otherKinds = [...new Set(chosen.flatMap((a) => (a.windows ?? []).filter((w) => w.known).map((w) => w.kind)))].filter((k) => k !== stage.window) as BudgetWindow[];
	return { measured: measured.length, missing: chosen.length - measured.length, otherKinds };
}

export const pct = (fraction: number) => {
	const v = fraction * 100;
	return `${Number.isInteger(v) ? v : Number(v.toPrecision(3))}%`;
};

// Period names match the Pool page's "Budget for virtual models".
export const WINDOW_COPY: Record<BudgetWindow, { noun: string }> = {
	"5h": { noun: "5 hours" },
	day: { noun: "day" },
	week: { noun: "week" },
	month: { noun: "month" },
};

export const DRAW_COPY: Record<Draw, { title: string; explain: string; verb: (n: number) => string }> = {
	"least-used": {
		title: "Most room left",
		explain: "Each request goes to the account with the most budget left. Good default: wear stays even.",
		verb: (n) => (n > 1 ? `the account with the most room of ${n}` : "its one account"),
	},
	alternate: {
		title: "Take turns",
		explain: "Each request goes to the next account in line, round and round.",
		verb: (n) => (n > 1 ? `${n} accounts in turn` : "its one account"),
	},
	fill: {
		title: "One at a time",
		explain: "Uses the first account until its share runs out, then moves to the next. Keeps the others fresh.",
		verb: (n) => (n > 1 ? `${n} accounts one after another` : "its one account"),
	},
};

export const TERMINAL_COPY: Record<TerminalKind, { title: string; short: string; explain: string }> = {
	limit: {
		title: "Tell the caller to try later",
		short: "Answer with a limit message",
		explain: "The request fails right away with a rate-limit error (HTTP 429) carrying your message. Honest and immediate; clients usually retry later.",
	},
	queue: {
		title: "Hold the request until there's room",
		short: "Wait until a budget resets",
		explain: "The caller waits while NeoProxy holds the request until a stage has room again. Suits batch jobs, not people waiting at a prompt.",
	},
	passthrough: {
		title: "Answer from a fallback model, without a budget",
		short: "Fall back without a budget",
		explain: "Keeps answering from one provider model you choose, with no budget at all. Use it when running out is worse than spending more.",
	},
};

export function terminalLine(t: Terminal, models?: NeoModel[]) {
	if (t.kind === "passthrough") return t.model ? `Fall back to ${modelName(t.model, models)}, without a budget` : "Fall back to a model you choose";
	return TERMINAL_COPY[t.kind].short;
}

/** One plain sentence describing a stage's budget. */
export function stageSentence(stage: Stage, poolSize: number) {
	const who = stage.per === "user" ? "for each person" : "shared by everyone";
	return `Uses ${DRAW_COPY[stage.draw].verb(poolSize)}, up to ${pct(stage.perAccount)} of each account's budget per ${WINDOW_COPY[stage.window].noun} and ${pct(stage.cap)} in total, ${who}.`;
}

export function newStageId(existing: Stage[]) {
	let id = "";
	do id = `s${Math.random().toString(36).slice(2, 7)}`;
	while (existing.some((s) => s.id === id));
	return id;
}

export const newStage = (model: string, existing: Stage[]): Stage => ({
	id: newStageId(existing),
	model,
	accounts: "all",
	draw: "least-used",
	perAccount: 0.05,
	cap: 0.2,
	window: "week",
	per: "user",
	effort: null,
});

export const blankDraft = (): VirtualModelDraft => ({ id: null, name: "", served: "neoproxy/", description: "", stages: [], terminal: { kind: "limit", message: "" }, script: "" });

export function searchText(vm: VirtualModel, models?: NeoModel[], providers?: NeoProvider[]) {
	const parts = [vm.name, vm.served, vm.description];
	for (const s of vm.stages) {
		const label = labelProvider(providerOfModel(s.model, models), providers);
		parts.push(s.model, modelName(s.model, models), label.name, label.source ?? "");
	}
	return parts.filter(Boolean).join(" ").toLowerCase();
}

export const compactCtx = (n: number) => (n >= 1_000_000 ? `${Number((n / 1_000_000).toPrecision(3))}M` : n >= 1000 ? `${Math.round(n / 1000)}k` : String(n));
