import { h, fmtInt } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, seg, input, field, toggle, search, badge, table, empty, inspector, section, spinner, errorState, toast } from "../lib/components.js";

// Canonical channels in internal/registry/model_definitions.go:499-537.
const CHANNELS = ["claude", "gemini", "gemini-interactions", "vertex", "aistudio", "codex", "kimi", "antigravity", "xai", "devin", "meta"];

export async function render(ctx) {
	const { root } = ctx;
	root.append(pageHead({ title: "Routing", desc: "How the engine picks among its own credential files for the same provider. Virtual models and per-user allotments are decided on their own pages; this is the layer underneath." }));
	const host = h("div");
	const loading = spinner();
	root.append(host, loading);
	let config;
	try { config = await engine.config(); }
	catch (e) { loading.replaceWith(errorState(e, () => ctx.rerender())); return; }
	loading.remove();
	const draft = structuredClone(config.routing ?? {});
	draft.strategy ??= "round-robin";
	draft.retry ??= {};
	draft.cooldown ??= {};
	for (const key of ["request-retry", "max-retry-credentials", "max-retry-interval"]) draft.retry[key] ??= 0;
	draft.cooldown["transient-error-cooldown-seconds"] ??= 0;
	for (const key of ["session-affinity", "force-model-prefix"]) draft[key] ??= false;
	for (const key of ["disable-cooling", "save-cooldown-status"]) draft.cooldown[key] ??= false;
	let baseline = JSON.stringify(draft);
	let saving = false;
	const controls = [];
	const updateSave = () => { save.disabled = saving || JSON.stringify(draft) === baseline || controls.some((control) => !control.checkValidity()); };
	const save = btn("Save changes", { cls: "primary", disabled: true, onClick: async () => {
		saving = true;
		updateSave();
		const submitted = structuredClone(draft);
		try {
			await engine.patch("config", { routing: submitted });
			invalidate("config");
			baseline = JSON.stringify(submitted);
			toast("Saved");
		} catch (e) { toast(e.message, "bad"); }
		finally { saving = false; updateSave(); }
	} });
	const numberField = (label, group, key, hint, min = 0) => {
		const control = input({ type: "number", mono: true, min, step: 1, required: true, value: draft[group][key], "aria-label": label, onInput: (e) => { draft[group][key] = e.target.valueAsNumber; updateSave(); } });
		controls.push(control);
		return field(label, control, hint);
	};
	const setting = (label, checked, change) => toggle(checked, (value) => { change(value); updateSave(); }, label);
	host.append(card({
		title: "Strategy", sub: "Applies when a provider has more than one credential.", actions: [save],
		body: [
			field("Credential selection", seg([
				{ value: "round-robin", label: "Round robin" },
				{ value: "weighted-round-robin", label: "Weighted round robin" },
				{ value: "fill-first", label: "Fill first" },
			], draft.strategy, (value) => { draft.strategy = value; updateSave(); }), "Take turns, take turns by weight, or fill the first healthy credential before moving on."),
			h("div", { class: "grid c2" },
				numberField("Request retries", "retry", "request-retry", "Extra retry rounds after a failed call. 0 means no extra rounds."),
				numberField("Max credentials per request", "retry", "max-retry-credentials", "How many credentials a request may try. 0 means all available credentials."),
				numberField("Max retry interval in seconds", "retry", "max-retry-interval", "The longest cooldown a request may wait for before retrying. 0 means do not wait."),
				numberField("Transient error cooldown in seconds", "cooldown", "transient-error-cooldown-seconds", "Rest after temporary upstream failures. 0 uses the engine default; -1 disables this rest.", -1),
			),
			h("div", { class: "row wrap", style: { marginTop: "14px" } },
				setting("Keep sessions on one credential", Boolean(draft["session-affinity"]), (v) => { draft["session-affinity"] = v; }),
				setting("Require provider model prefix", Boolean(draft["force-model-prefix"]), (v) => { draft["force-model-prefix"] = v; }),
				setting("Cool down after failures", !draft.cooldown["disable-cooling"], (v) => { draft.cooldown["disable-cooling"] = !v; }),
				setting("Keep cooldowns across restarts", Boolean(draft.cooldown["save-cooldown-status"]), (v) => { draft.cooldown["save-cooldown-status"] = v; }),
			),
		],
	}));
	const reset = btn("Reset all cooldowns", { cls: "outline", onClick: async () => {
		reset.disabled = true;
		try {
			invalidate("credentials");
			const response = await engine.credentials();
			const credentials = Array.isArray(response) ? response : response?.files ?? response?.credentials ?? [];
			let count = 0;
			for (const credential of credentials) {
				const index = credential.auth_index ?? credential.authIndex;
				if (!index) continue;
				await engine.post("routing/cooldown/reset", { auth_index: index });
				count++;
			}
			invalidate("credentials");
			toast(`Reset ${fmtInt(count)} cooldowns`);
		} catch (e) { toast(e.message, "bad"); }
		finally { reset.disabled = false; }
	} });
	host.append(card({ title: "Cooldowns", sub: "Resetting cooldowns lets every credential try again immediately after a rate limit or upstream failure.", actions: [reset] }));
	const modelsBody = h("div", null, spinner());
	let definitions = [];
	let query = "";
	let selected = null;
	let disposed = false;
	const close = () => { selected = null; ctx.setInspector(null); paintModels(); };
	const open = (row) => {
		selected = row.channel;
		paintModels();
		ctx.setInspector(inspector({ title: row.channel, sub: `${fmtInt(row.models.length)} model definitions`, onClose: close, body: [section("Model ids", h("div", { class: "list" }, row.models.map((model) => h("div", { class: "li" }, h("code", null, typeof model === "string" ? model : model.id)))))] }));
	};
	function paintModels() {
		modelsBody.replaceChildren(table([
			{ key: "channel", label: "Channel", render: (row) => badge(row.channel), sortBy: (row) => row.channel },
			{ key: "count", label: "Models", num: true, render: (row) => fmtInt(row.models.length), sortBy: (row) => row.models.length },
		], definitions.filter((row) => row.channel.includes(query.toLowerCase())), { onRow: open, keyOf: (row) => row.channel, selectedKey: selected, emptyState: empty({ icon: "box", title: "No model definitions", hint: query ? "Try a different channel name." : "Models appear here when the engine publishes definitions for a channel." }) }));
	}
	const loadModels = async () => {
		try {
			const results = await Promise.all(CHANNELS.map(async (channel) => {
				try { const result = await engine.get(`routing/model-definitions/${channel}`); return { channel, models: result?.models ?? [] }; }
				catch (e) { if (e instanceof ApiError && e.status === 404) return null; throw e; }
			}));
			if (disposed) return;
			definitions = results.filter(Boolean);
			paintModels();
		} catch (e) { if (!disposed) modelsBody.replaceChildren(errorState(e, loadModels)); }
	};
	host.append(card({ title: "Model definitions", actions: [search("Filter channels", (value) => { query = value; paintModels(); })], flush: true, body: modelsBody }));
	const escape = (e) => { if (e.key === "Escape" && selected) close(); };
	document.addEventListener("keydown", escape);
	await loadModels();
	return () => { disposed = true; document.removeEventListener("keydown", escape); };
}
