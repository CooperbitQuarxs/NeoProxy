// The engine's error reports: one file per failed request, named
// `error-<endpoint>-<local time>-<id>.log`, holding `=== SECTION ===` blocks of
// `Key: value` lines, `Headers:` / `Body:` labels and JSON bodies. Logs reads
// them in full; Activity uses the facts to say what the provider answered.

import { engine } from "./client.js";

// `error-v1-chat-completions-2026-11-04T035144-7c1d6e8e.log` → the endpoint and when.
// The name carries the engine's local time of the failure; the file time can be a later copy.
export function reportInfo(name, modified) {
	const m = /^error-(.+?)-(\d{4})-(\d{2})-(\d{2})T(\d{2})(\d{2})(\d{2})-[0-9a-f]+\.log$/i.exec(name);
	if (!m) return { route: name, at: modified ?? 0 };
	const [, route, y, mo, d, hh, mm, ss] = m;
	return { route: `/${route.replace(/-/g, "/")}`, at: new Date(+y, +mo - 1, +d, +hh, +mm, +ss).getTime() };
}

function parseJson(text) {
	if (!/^\s*[{[]/.test(text)) return undefined;
	try { return JSON.parse(text); } catch { return undefined; }
}

// Sections of parts: { kind: "pairs" | "json" | "label" | "text", … }.
export function parseReport(text) {
	const sections = [];
	let current = null;
	for (const raw of text.split("\n")) {
		const head = /^=== (.+) ===$/.exec(raw.trim());
		if (head) { current = { title: head[1], parts: [] }; sections.push(current); continue; }
		if (!raw.trim()) continue;
		if (!current) { current = { title: "", parts: [] }; sections.push(current); }
		const parts = current.parts;
		const label = /^([A-Z][\w -]{0,30}):$/.exec(raw.trim());
		const pair = /^([A-Za-z][\w-]*(?: [A-Za-z][\w-]*){0,3}): (.*)$/.exec(raw);
		const json = parseJson(raw);
		if (label) parts.push({ kind: "label", text: label[1] });
		else if (json !== undefined) parts.push({ kind: "json", value: json });
		else if (pair) {
			const last = parts[parts.length - 1];
			if (last?.kind === "pairs") last.pairs.push([pair[1], pair[2]]);
			else parts.push({ kind: "pairs", pairs: [[pair[1], pair[2]]] });
		} else parts.push({ kind: "text", text: raw });
	}
	return sections;
}

// The few facts worth a sentence: what the provider said, the status the client got, where it went.
export function reportFacts(sections) {
	const find = (title) => sections.find((s) => s.title === title);
	const pairOf = (s, key) => s?.parts.flatMap((p) => (p.kind === "pairs" ? p.pairs : [])).find(([k]) => k === key)?.[1];
	const jsonOf = (s) => s?.parts.find((p) => p.kind === "json")?.value;
	const answer = jsonOf(find("API RESPONSE")) ?? jsonOf(find("RESPONSE"));
	const tries = sections.filter((s) => /^API REQUEST \d+$/.test(s.title));
	const last = tries[tries.length - 1];
	const auth = pairOf(last, "Auth") ?? "";
	return {
		message: answer?.error?.message ?? answer?.detail ?? answer?.message ?? (typeof answer?.error === "string" ? answer.error : null),
		status: pairOf(find("RESPONSE"), "Status") ?? null,
		asked: jsonOf(find("REQUEST BODY"))?.model ?? null,
		sentModel: jsonOf(last)?.model ?? null,
		provider: /provider=([^,]+)/.exec(auth)?.[1] ?? null,
		account: /label=([^,]+)/.exec(auth)?.[1] ?? null,
		upstream: pairOf(last, "Upstream URL") ?? null,
		tries: tries.length,
	};
}

export async function fetchReport(name) {
	const response = await engine.raw("GET", `observability/logs/errors/${encodeURIComponent(name)}`);
	if (!response.ok) throw new Error(`Could not read ${name}`);
	return response.blob();
}

// Router reason codes the engine sometimes writes as the message, in words.
const CODE_WORDS = {
	"no-healthy-account": "No account was available for this model",
	"unknown-model": "No model on this proxy has that name",
	"not-granted": "This person has no grant on that virtual model",
	"no-provider-access": "This person has no direct access to that provider",
	"routing-error": "The router failed before picking an account",
	"neoproxy_unauthorized": "The inference key was rejected",
	"neoproxy_suspended": "This person is suspended",
};
// One line for a report: what went wrong, in words.
export function reportHeadline(facts) {
	const m = facts?.message?.trim();
	if (!m) return "The report does not say why the request failed";
	return CODE_WORDS[m] ?? m;
}

// Facts per report file, read once per name; the list shows them without opening each.
const factCache = new Map();
export function reportFactsOf(name) {
	if (!factCache.has(name)) factCache.set(name, fetchReport(name).then((b) => b.text()).then((t) => reportFacts(parseReport(t))).catch((e) => { factCache.delete(name); throw e; }));
	return factCache.get(name);
}

// The report written for a request at `at` (ms) that asked for `asked`, if the
// engine kept one: same second (give or take), same model in the request body.
// With `upstream`, a report that reached the provider wins over the router's own.
export async function findReport(at, asked, { within = 3000, upstream = false } = {}) {
	const files = ((await engine.errorLogs())?.files ?? []).map((f) => (typeof f === "string" ? { name: f } : f));
	const near = files.filter((f) => Math.abs(reportInfo(f.name, f.modified == null ? null : f.modified * 1000).at - at) <= within);
	let found = null;
	for (const file of near) {
		const facts = await reportFactsOf(file.name);
		if (asked && facts.asked && facts.asked !== asked && facts.asked !== asked.replace(/^neoproxy\//, "")) continue;
		if (!upstream || facts.tries > 0) return { file, facts };
		found ??= { file, facts };
	}
	return found;
}
