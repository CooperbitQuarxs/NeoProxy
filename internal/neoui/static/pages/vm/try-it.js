// "Try it": where would a request from this person land right now, and with which
// properties? It runs the saved model against today's meters and the person's
// grants; nothing is sent upstream or recorded.

import { h, fmtPct, clear } from "../../lib/dom.js";
import { neo } from "../../lib/client.js";
import { card, btn, select, field, trail, empty, spinner, errorState } from "../../lib/components.js";
import { propChip } from "../../lib/vm-props.js";
import { describeSetting, formatValue, humanize } from "../../lib/param-model.js";
import { isScriptError } from "./lua-policy.js";

// env: { vm, draft, models, vault, users, isAdmin, isNew, isDirty(), specsNow(id), openLua(), scriptMessage(msg), onResult(result | null) }
export function tryIt(env) {
	const { draft, models, vault, users } = env;
	// Ask as someone who can actually reach this model, so the first answer is a real one.
	let user = (users.find((u) => u.state === "active" && u.grants.some((g) => g.virtualModel === draft.id)) ?? users.find((u) => u.role === "member" && u.state === "active") ?? users[0])?.id;
	let protocol = "openai";
	let payload = "";
	let generation = 0;
	const host = h("div", { class: "vm-sim-out" });

	const protocolSel = select([
		{ value: "openai", label: "OpenAI Chat Completions" },
		{ value: "openai-response", label: "OpenAI Responses" },
		{ value: "claude", label: "Anthropic Messages" },
	], protocol, (value) => { protocol = value; run(); }, { "aria-label": "Simulation protocol" });
	const payloadInput = h("textarea", {
		class: "textarea mono", rows: 6, spellcheck: "false", "aria-label": "Simulation request JSON",
		placeholder: 'Optional client JSON, e.g. {"messages":[{"role":"user","content":"Example request"}],"service_tier":"flex"}',
		onInput: (e) => {
			payload = e.target.value;
			++generation;
			env.onResult(null);
			host.replaceChildren(h("div", { class: "ink-3" }, "Request changed. Run the simulation to evaluate this payload."));
		},
	});
	const userSel = select(users.map((u) => ({ value: u.id, label: `${u.handle} · ${u.role}` })), user, (v) => { user = v; run(); }, { "aria-label": "Simulate as user" });

	function explain(result) {
		const steps = (result.trail ?? []).filter((t) => t.kind === "stage");
		const label = (t) => `Model ${t.index + 1} (${models.find((m) => m.id === t.model)?.display ?? t.model})`;
		const accountName = (accountId) => vault.accounts.find((a) => a.id === accountId)?.label ?? "an account in the pool";
		const skipped = steps.filter((t) => t.skipped).map((t) => {
			if (t.skipped === "cap") return `${label(t)} is skipped: its pool averages ${fmtPct(t.total, null)} and it stops at ${fmtPct(t.cap, null)}`;
			if (t.skipped === "per-account") return `${label(t)} is skipped: every account in it is at its threshold`;
			if (t.skipped === "unhealthy") return `${label(t)} is skipped: no healthy account in its pool can serve`;
			if (t.skipped === "condition") { const n = (t.candidates ?? []).filter((c) => c.reason === "condition").length; return `${label(t)} is skipped: ${n ? `its rules ruled out ${n === 1 ? "the only account" : `all ${n} accounts`}` : "its rules did not match this request"}`; }
			if (t.skipped === "context") return `${label(t)} is skipped: the prompt is larger than its window`;
			if (t.skipped === "upstream-error") return `${label(t)} failed upstream, so the chain moved on`;
			return `${label(t)} is skipped by the Lua policy`;
		});
		const landed = steps.find((t) => t.chosen && !t.skipped);
		let ending;
		if (landed) {
			const c = landed.candidates?.find((x) => x.accountId === landed.chosen);
			const meter = !c ? "" : c.metered ? `, which has used ${fmtPct(c.used, null)} of its ${fmtPct(c.budget, null)} threshold` : ", which has no meter and is always available";
			ending = `It lands on ${label(landed).replace(/^Model/, "model")} using ${accountName(landed.chosen)}${meter}`;
		} else {
			const end = (result.trail ?? []).find((t) => t.kind === "terminal");
			ending = end ? `Every model is spent, so the chain ends: ${end.terminal.kind === "queue" ? "the request waits for a window to reset" : end.terminal.kind === "passthrough" ? "it falls through to a direct model" : "the caller gets a limit error"}` : result.message ?? "The request does not reach a model";
		}
		const who = users.find((u) => u.id === user);
		const allot = who?.grants.find((g) => g.virtualModel === draft.id)?.allotment;
		const prefix = who && allot !== undefined && allot !== 1 ? `${who.handle} may use up to ${fmtPct(allot, null)} of every threshold. ` : "";
		return `${prefix}${[...skipped, ending].join(". ")}.`;
	}

	// The properties the decision applied, each with what it did on the wire.
	function applied(result) {
		const list = result.params ?? [];
		if (!list.length) return null;
		const specs = env.specsNow(result.model) ?? [];
		return h("div", { class: "vm-applied" },
			h("div", { class: "vm-applied-title" }, "Applied to this request"),
			h("ul", null, list.map((p) => {
				const spec = specs.find((s) => s.id === p.id);
				const text = spec ? formatValue(spec, p.value) : String(Array.isArray(p.value) ? p.value.join(", ") : p.value);
				return h("li", null, propChip({ ...describeSetting(spec, p.id, { mode: "force", rules: [{ value: p.value }] }), text, label: spec?.label ?? humanize(p.id) }), p.realize ? h("span", { class: "ink-3" }, p.realize) : null);
			})),
		);
	}

	async function run() {
		const mine = ++generation;
		env.onResult(null);
		clear(host);
		if (env.isDirty() || env.isNew || !user) {
			host.append(h("div", { class: "ink-3" }, !user ? "No users available." : "Save changes before running a real simulation."));
			return;
		}
		host.append(spinner());
		try {
			let request;
			if (payload.trim()) {
				try { request = JSON.parse(payload); }
				catch (e) { throw new Error(`Invalid simulation request JSON: ${e.message}`); }
				if (!request || Array.isArray(request) || typeof request !== "object") throw new Error("Simulation request JSON must be an object.");
			}
			const result = await neo.simulate({ asked: env.vm.served, userId: user, protocol, request });
			if (mine !== generation || !host.isConnected) return;
			env.onResult(result);
			host.replaceChildren(...[h("p", { class: "vm-why" }, explain(result)), applied(result), trail(result, { models, accounts: vault.accounts, users, admin: env.isAdmin })].filter(Boolean));
		} catch (e) {
			if (mine !== generation || !host.isConnected) return;
			const failure = isScriptError(e)
				? empty({ icon: "info", title: "Routing script result rejected", hint: env.scriptMessage(e.message), action: env.isAdmin ? btn("Edit script", { cls: "outline", onClick: () => env.openLua() }) : null })
				: errorState(e, run);
			failure.title = e.message;
			host.replaceChildren(failure);
		}
	}

	// An edit makes the last answer stale. Keep it on screen, marked, instead of blanking it on every keystroke.
	function stale() {
		++generation;
		env.onResult(null);
		const prev = host.querySelector(".vm-why");
		if (prev && !host.querySelector(".vm-stale")) host.prepend(h("div", { class: "vm-stale ink-3" }, "Unsaved edits: this is the answer for the saved model. Save to run it again."));
		else if (!prev) host.replaceChildren(h("div", { class: "ink-3" }, "Save changes, then run the simulation."));
	}

	return {
		el: card({
			title: "Try it",
			sub: "Where a request from this person lands right now, with which properties. Nothing is sent or recorded.",
			actions: [btn("Run", { icon: "refresh", cls: "outline sm", title: "Run the simulation again", onClick: run })],
			body: [
				h("div", { class: "vm-sim-who" }, field("Ask as", userSel)),
				h("details", { style: { marginBottom: "14px" } },
					h("summary", null, "Protocol & request payload"),
					field("Protocol", protocolSel),
					field("Client request JSON · optional", payloadInput, "Read-only script input.request. Use messages for Chat/Anthropic or input for Responses. Properties set to “unless the caller chooses” step aside for values you put here. Script request bodies are capped at 256 KiB."),
				),
				host,
			],
			cls: "vm-try",
		}),
		run,
		stale,
	};
}
