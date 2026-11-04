import { canonicalProvider } from "../../lib/providers";
import type { Decision, PoolModel, RequestRecord, TrailStep, User, Vault, VirtualModel } from "../people/api";
import { displayName } from "../people/copy";
import { fmtDuration } from "../people/widgets";

// Turns ledger records (internal/neoroute Decision + trail) into plain sentences.

export type Tone = "good" | "warn" | "bad" | "accent" | "idle";
export const OUTCOME: Record<string, { word: string; tone: Tone; filter: string }> = {
	ok: { word: "Served", tone: "good", filter: "Served" },
	limited: { word: "Limited", tone: "warn", filter: "Limited" },
	queued: { word: "Queued", tone: "accent", filter: "Queued" },
	error: { word: "Failed", tone: "bad", filter: "Failed" },
};
export const outcomeOf = (o: string) => OUTCOME[o] ?? { word: o, tone: "idle" as Tone, filter: o };

const REASON: Record<string, string> = {
	"unknown-model": "No model by that name",
	"not-granted": "Not given access to this model",
	"no-provider-access": "No direct access to this provider",
	"no-healthy-account": "No working account for this model",
	"upstream-error": "The provider returned an error",
	"routing-error": "Every account it tried failed",
	cancelled: "The app cancelled the request",
	"reservation-error": "NeoProxy couldn't reserve budget",
	"settlement-error": "NeoProxy couldn't record the usage",
	"script-error": "The model's routing script failed",
	"invalid-script-proposal": "The routing script picked an account that wasn't available",
	unauthorized: "The key wasn't accepted",
	"user-limit": "Hit their personal limit",
	limit: "The model's budget is used up for now",
	queue: "Waiting for budget to free up",
};
export const reasonText = (d: Decision) => (d.reason ? REASON[d.reason] ?? d.reason.replace(/-/g, " ") : "");

const SKIPPED: Record<string, string> = {
	cap: "Its budget for this window is used up",
	"per-account": "Every account has used its share for this window",
	unhealthy: "No account was ready: cooling down, signed out or full",
	"upstream-error": "The provider returned an error",
	script: "The routing script chose a different stage",
	"lua-policy": "The routing script ruled it out",
	"lua-cap": "The routing script's limit is reached",
};

export type Lookup = {
	person: (id: string) => string;
	personUser: (id: string) => User | undefined;
	keyLabel: (userId: string, keyId: string | null) => string | undefined;
	vm: (id: string | undefined) => VirtualModel | undefined;
	model: (id: string | undefined) => string;
	account: (id: string | undefined) => string;
	provider: (modelOrProvider: string | undefined) => string;
};

export function makeLookup(users: User[], vms: VirtualModel[], models: PoolModel[], vault: Vault | undefined): Lookup {
	return {
		person: (id) => displayName(users.find((u) => u.id === id)),
		personUser: (id) => users.find((u) => u.id === id),
		keyLabel: (userId, keyId) => (keyId ? users.find((u) => u.id === userId)?.keys?.find((k) => k.id === keyId)?.label : undefined),
		vm: (id) => (id ? vms.find((v) => v.id === id) : undefined),
		model: (id) => (id ? models.find((m) => m.id === id)?.display ?? (id.split("/").slice(1).join("/") || id) : ""),
		account: (id) => (id ? vault?.accounts.find((a) => a.id === id)?.label ?? "an account" : "an account"),
		provider: (m) => canonicalProvider((m ?? "").split("/")[0] ?? ""),
	};
}

export const totalTokens = (r: RequestRecord) => (r.tokens?.in ?? 0) + (r.tokens?.out ?? 0);

/** Short "answered by" summary for a list row. */
export function answered(r: RequestRecord, L: Lookup): { provider?: string; model?: string; detail?: string; text?: string } {
	const d = r.decision;
	if (r.outcome === "ok" && d.model) {
		const stage = d.stageIndex !== undefined && d.stageIndex !== null ? `stage ${d.stageIndex + 1}` : d.direct ? "direct" : d.virtualModel ? "fallback" : undefined;
		return { provider: L.provider(d.model), model: L.model(d.model), detail: [stage, d.accountId ? L.account(d.accountId) : undefined].filter(Boolean).join(", ") };
	}
	return { text: d.message && r.outcome !== "error" ? d.message : reasonText(d) };
}

export type StoryStep = { tone: Tone; title: string; detail?: string; provider?: string; mono?: string };

const pct = (n: number | undefined) => {
	const v = (n ?? 0) * 100;
	return `${v > 0 && v < 10 ? v.toFixed(1) : Math.round(v)}%`;
};

function stageStep(t: TrailStep, L: Lookup, finalOk: boolean, chosenAccount?: string): StoryStep {
	const n = (t.index ?? 0) + 1;
	const model = L.model(t.model);
	const base = { provider: L.provider(t.model), title: `Stage ${n}: ${model}` };
	if (t.skipped === "upstream-error" && t.chosen)
		return { ...base, tone: "bad", detail: `Tried ${L.account(t.chosen)}; the provider answered ${t.failureStatus ? `with error ${t.failureStatus}` : "with an error"}, so NeoProxy moved on.` };
	if (t.skipped === "upstream-error") return { ...base, tone: "bad", detail: "Skipped after a provider error on an earlier try." };
	if (t.skipped) {
		if (t.skipped === "cap" && !t.cap) return { ...base, tone: "warn", detail: "Skipped. This stage gives them no budget: their share of this model is 0%, or the stage's cap is 0." };
		const why = SKIPPED[t.skipped] ?? t.skipped;
		const budget = t.skipped === "cap" ? ` (${pct(t.total)} of the window used, cap ${pct(t.cap)})` : "";
		return { ...base, tone: "warn", detail: `Skipped. ${why}${budget}.` };
	}
	if (t.chosen) {
		const ready = t.candidates?.length ?? 0;
		return {
			...base,
			tone: finalOk && t.chosen === chosenAccount ? "good" : "accent",
			detail: `Picked ${L.account(t.chosen)}${ready > 1 ? ` out of ${ready} accounts with budget left` : ""}.`,
		};
	}
	return { ...base, tone: "idle", detail: "Considered." };
}

export function story(r: RequestRecord, L: Lookup, admin: boolean): StoryStep[] {
	const d = r.decision;
	const who = L.person(r.userId);
	const key = L.keyLabel(r.userId, r.keyId);
	const vm = L.vm(d.virtualModel);
	const keyText = key ? `the key “${key}”` : r.keyId ? "a key that has since been revoked" : undefined;
	const sent = r.client && keyText ? `Sent by ${r.client} with ${keyText}.` : r.client ? `Sent by ${r.client}.` : keyText ? `Sent with ${keyText}.` : undefined;
	const steps: StoryStep[] = [{ tone: "idle", title: `${admin ? who : "You"} asked for ${vm ? vm.name : r.asked}`, mono: vm ? r.asked : undefined, detail: sent }];
	for (const t of d.trail ?? []) {
		switch (t.kind) {
			case "stage":
				steps.push(stageStep(t, L, r.outcome === "ok", d.accountId));
				break;
			case "direct":
				steps.push({ tone: "good", provider: L.provider(t.model), title: `Called ${L.model(t.model)} directly`, detail: `On ${L.account(t.accountId)}, without a model's stages.` });
				break;
			case "denied":
				steps.push(
					t.virtualModel
						? { tone: "bad", title: "Not allowed", detail: `${admin ? who : "You"} ${admin ? "hasn't" : "haven't"} been given ${L.vm(t.virtualModel)?.name ?? "this model"}.${admin ? " Give access on their People page." : ""}` }
						: { tone: "bad", title: "Not allowed", detail: `${admin ? who : "You"} ${admin ? "has" : "have"} no direct access to ${t.providerId ?? "this provider"}'s models.` },
				);
				break;
			case "terminal": {
				const term = t.terminal;
				if (term?.kind === "passthrough") steps.push({ tone: "accent", provider: L.provider(term.model), title: "Every stage was used up", detail: `So it went to the fallback, ${L.model(term.model)}.` });
				else if (term?.kind === "queue") steps.push({ tone: "warn", title: "Every stage was used up", detail: `So the request waits for budget to free up.${term.message ? ` The app was told: “${term.message}”` : ""}` });
				else steps.push({ tone: "warn", title: "Every stage was used up", detail: `So the request was refused.${term?.message ? ` The app was told: “${term.message}”` : ""}` });
				break;
			}
			case "no-model":
				steps.push({ tone: "bad", title: "No model by that name", mono: t.asked, detail: "Check the model name in the app. People see their models on the Models page." });
				break;
			default:
				steps.push({ tone: "idle", title: t.kind });
		}
	}
	steps.push(resultStep(r, L));
	return steps;
}

function resultStep(r: RequestRecord, L: Lookup): StoryStep {
	const d = r.decision;
	if (r.outcome === "ok") {
		const t = r.tokens ?? { in: 0, out: 0, cached: 0 };
		const tokens = t.in || t.out ? `, ${fmt(t.in)} tokens in and ${fmt(t.out)} out` : "";
		return { tone: "good", provider: L.provider(d.model), title: `Served by ${L.model(d.model)}`, detail: `Answered in ${fmtDuration(r.latencyMs)}${tokens}${r.stream ? ", streamed" : ""}.` };
	}
	const resets = d.resetsAt ? ` Budget frees up ${new Date(d.resetsAt).toLocaleString(undefined, { weekday: "short", hour: "numeric", minute: "2-digit" })}.` : "";
	const msg = d.message ? ` ${d.message.replace(/\.?$/, ".")}` : "";
	const o = outcomeOf(r.outcome);
	return { tone: o.tone, title: `${o.word}: ${reasonText(d) || "no reason recorded"}`, detail: `${msg}${resets}`.trim() || undefined };
}

const nf = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });
export const fmt = (n: number) => nf.format(n);
