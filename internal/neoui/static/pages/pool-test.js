// Test payload: a single-turn conversation with one account, on the endpoint,
// model and body you choose, with each hop of the request shown as it
// happens. The recipe can be saved so the row's plain Test button uses it.

import { h, icon, fmtAgo } from "../lib/dom.js";
import { neo, engine, ApiError } from "../lib/client.js";
import { btn, field, input, seg, select, toast, dialog, confirm } from "../lib/components.js";
import { jsonView } from "../lib/json.js";

const ENDPOINTS = [
	{ value: "chat", label: "Chat completions", path: "/v1/chat/completions" },
	{ value: "responses", label: "Responses", path: "/v1/responses" },
	{ value: "messages", label: "Messages", path: "/v1/messages" },
];
const STAGES = [
	{ id: "credential", label: "Credential", pending: "checking the key or sign-in" },
	{ id: "request", label: "Request", pending: "building the body" },
	{ id: "provider", label: "Provider", pending: "waiting for the provider" },
	{ id: "response", label: "Response", pending: "reading the reply" },
];
const DEFAULT_PROMPT = "Reply with the single word OK.";

function defaultBody(endpoint, model, prompt) {
	if (endpoint === "responses") return { model, input: prompt, max_output_tokens: 16 };
	if (endpoint === "messages") return { model, max_tokens: 8, messages: [{ role: "user", content: prompt }] };
	return { model, messages: [{ role: "user", content: prompt }], max_tokens: 8 };
}

// Strip the pool prefix a model may carry so the datalist and the input agree.
const bare = (id, providerId) => id.replace(new RegExp(`^${providerId.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/`), "");

export function openTestDialog(a, { provider, onChange } = {}) {
	const providerId = a.providerId;
	const saved = a.face?.probeConfig ?? null;
	const models = (a.face?.models ?? a.modelIds ?? []).map((m) => bare(m, providerId));
	const state = {
		endpoint: saved?.endpoint ?? "chat",
		model: saved?.model ? bare(saved.model, providerId) : "",
		prompt: saved?.prompt || DEFAULT_PROMPT,
		payloadText: saved?.payload ? JSON.stringify(saved.payload, null, 2) : "",
		payloadMode: !!saved?.payload,
		running: false,
		result: null,
	};

	// ---- left: the recipe ----
	// Model: a plain select of what the account lists, "Automatic" first, and
	// "Another model…" for an id the account does not list.
	const OTHER = "\u0000other";
	const pickedOther = !!state.model && !models.includes(state.model);
	const otherInput = input({ mono: true, placeholder: "model id as the provider names it", value: pickedOther ? state.model : "", autocomplete: "off", spellcheck: "false", "aria-label": "Model id", onInput: (e) => { state.model = e.target.value.trim(); syncPayload(); } });
	const otherRow = h("div", { class: `test-other ${pickedOther || !models.length ? "" : "hidden"}` }, otherInput);
	const modelSelect = models.length ? select(
		[{ value: "", label: "Automatic: a cheap model this account lists" }, ...models.slice(0, 400).map((m) => ({ value: m, label: m })), { value: OTHER, label: "Another model…" }],
		pickedOther ? OTHER : state.model,
		(v) => {
			const other = v === OTHER;
			otherRow.classList.toggle("hidden", !other);
			state.model = other ? otherInput.value.trim() : v;
			syncPayload();
			if (other) otherInput.focus();
		},
		{ "aria-label": "Model" }) : null;
	const modelInput = modelSelect ?? otherInput;
	const promptInput = h("textarea", { class: "input", rows: 3, onInput: (e) => { state.prompt = e.target.value; syncPayload(); } }, state.prompt);
	const payloadInput = h("textarea", { class: "input mono pool-test-payload", rows: 12, spellcheck: "false", onInput: (e) => { state.payloadText = e.target.value; state.payloadMode = true; payloadNote(); } });
	const payloadError = h("div", { class: "error", style: { display: "none" } });
	const payloadDetails = h("details", { class: "pool-test-details", open: state.payloadMode || null },
		h("summary", null, "Edit the full payload"),
		h("p", { class: "hint" }, "Exactly what is sent, as JSON. Editing here stops the prompt field from rewriting it; the model field still fills in ", h("code", null, "model"), "."),
		payloadInput, payloadError);
	const endpointSeg = h("div");
	const endpointPath = h("code", { class: "ink-3" });
	// seg() marks its own pressed button; rebuilding it would drop the focused
	// button and send focus to the page behind the dialog.
	const showEndpointPath = () => { endpointPath.textContent = ENDPOINTS.find((e) => e.value === state.endpoint).path; };
	endpointSeg.append(seg(ENDPOINTS.map((e) => ({ value: e.value, label: e.label })), state.endpoint, (v) => { state.endpoint = v; showEndpointPath(); if (!state.payloadMode) syncPayload(true); }));
	function syncPayload(force = false) {
		if (state.payloadMode && !force) return;
		state.payloadText = JSON.stringify(defaultBody(state.endpoint, state.model || (models[0] ?? ""), state.prompt), null, 2);
		payloadInput.value = state.payloadText;
		state.payloadMode = false;
		payloadNote();
	}
	function payloadNote() {
		if (!state.payloadMode) { payloadError.style.display = "none"; return; }
		try { JSON.parse(state.payloadText); payloadError.style.display = "none"; }
		catch (e) { payloadError.textContent = `Not valid JSON: ${e.message}`; payloadError.style.display = ""; }
	}
	showEndpointPath();
	if (!state.payloadMode) syncPayload(true);
	else payloadInput.value = state.payloadText;

	// ---- right: the request's steps, once there is something to show ----
	const track = h("ol", { class: "test-track", "aria-live": "polite" });
	const outcome = h("div", { class: "test-outcome" });
	const resultCol = h("div", { class: "test-result idle" }, track, outcome);
	function renderTrack(result) {
		track.replaceChildren();
		resultCol.classList.toggle("idle", !state.running && !result);
		if (!state.running && !result) return;
		const byId = new Map((result?.stages ?? []).map((s) => [s.id, s]));
		let failed = false;
		STAGES.forEach((st, i) => {
			const r = byId.get(st.id);
			let cls = "idle", detail = "";
			if (state.running) { cls = "pending"; detail = st.pending; }
			else if (result) {
				if (failed) { cls = "skipped"; detail = "not reached"; }
				else if (r) { cls = r.ok ? "ok" : "bad"; detail = r.detail ?? ""; if (!r.ok) failed = true; }
				else { cls = "skipped"; detail = "not reached"; }
			}
			track.append(h("li", { class: `test-step ${cls}`, style: { "--i": String(i) } },
				h("span", { class: "test-dot" }, cls === "ok" ? icon("check", 11) : cls === "bad" ? icon("x", 11) : null),
				h("div", { class: "test-step-text" }, h("div", { class: "test-step-label" }, st.label), detail ? h("div", { class: "test-step-detail" }, detail) : null)));
		});
		if (state.running) {
			// Light the steps one after another while the request is out; the real
			// result replaces this the moment it arrives.
			let i = 0;
			const tick = () => { if (!state.running) return; track.children[i]?.classList.add("live"); i = (i + 1) % STAGES.length; state.timer = setTimeout(tick, 650); };
			tick();
		}
	}
	function renderOutcome(result) {
		outcome.replaceChildren();
		if (!result) {
			if (!state.running) outcome.append(h("div", { class: "test-empty" }, icon("play", 16), h("div", null, "Send the test to follow it here: the key or sign-in check, the request, the provider and its reply.")));
			return;
		}
		if (result.unavailable) {
			outcome.append(h("div", { class: "pool-testbox warn" }, "This server does not support tests yet. Deploy the current build."));
			return;
		}
		const ms = result.latencyMs !== undefined ? `${result.latencyMs} ms` : "";
		if (result.ok) {
			outcome.append(
				h("div", { class: "test-chat" },
					h("div", { class: "bubble you" }, state.payloadMode ? h("em", null, "your payload") : state.prompt),
					h("div", { class: "bubble them" }, result.reply || h("em", null, "no assistant text in the reply"))),
				h("div", { class: "test-meta" }, `${result.model} · ${ENDPOINTS.find((e) => e.value === result.endpoint)?.label ?? result.endpoint} · HTTP ${result.status} · ${ms}`),
			);
		} else {
			outcome.append(
				h("div", { class: "pool-testbox bad test-fail" },
					h("div", { class: "test-fail-title" }, result.error || "The test failed."),
					result.providerMessage ? h("div", { class: "test-fail-provider" }, "The provider said: ", h("q", null, result.providerMessage)) : null,
					h("div", { class: "test-meta" }, [result.model, result.status ? `HTTP ${result.status}` : null, ms].filter(Boolean).join(" · "))),
			);
		}
		if (result.raw) {
			let parsed = null;
			try { parsed = JSON.parse(result.raw); } catch { /* text */ }
			outcome.append(h("details", { class: "pool-test-details" }, h("summary", null, "Exact answer from the provider"), parsed ? jsonView(parsed, { collapsedDepth: 2 }) : h("pre", { class: "code", style: { whiteSpace: "pre-wrap" } }, result.raw)));
		}
		if (result.request) outcome.append(h("details", { class: "pool-test-details" }, h("summary", null, "Exact request that was sent"), jsonView(result.request, { collapsedDepth: 3 })));
	}
	renderTrack(null);
	renderOutcome(null);

	// ---- actions ----
	function recipe() {
		const body = { endpoint: state.endpoint, model: state.model || undefined, prompt: state.prompt || undefined };
		if (state.payloadMode) {
			try { body.payload = JSON.parse(state.payloadText); } catch { throw new Error("The payload is not valid JSON."); }
		}
		return body;
	}
	const sendBtn = btn("Send test", { icon: "play", cls: "primary", onClick: send });
	const saveBtn = btn(saved ? "Update the account's Test" : "Use as the account's Test", { icon: "check", cls: "ghost sm", onClick: save });
	const resetBtn = btn("Forget the saved Test", { cls: "ghost sm", onClick: forget, title: "Go back to the automatic choice of model" });
	const savedNote = h("div", { class: "hint test-save-note" });
	state.saved = !!saved;
	state.savedAt = saved?.updatedAt ?? null;
	// Save is offered once a send worked; the note says what saving does.
	function paintSave() {
		const worked = !!state.result?.ok;
		saveBtn.style.display = worked ? "" : "none";
		resetBtn.style.display = state.saved ? "" : "none";
		savedNote.textContent = state.saved
			? `This is the account's Test${state.savedAt ? `, saved ${fmtAgo(state.savedAt)}` : ""}: the row's Test button sends exactly this.`
			: worked ? "Save makes this the account's Test: the row's Test button will send exactly this." : "";
	}
	paintSave();

	async function send() {
		let body;
		try { body = recipe(); } catch (e) { toast(e.message, "bad"); return; }
		state.running = true;
		state.result = null;
		sendBtn.disabled = true;
		renderTrack(null);
		renderOutcome(null);
		try {
			const r = await neo.testAccount(a.id, body);
			state.result = r;
		} catch (e) {
			state.result = e instanceof ApiError && (e.status === 404 || e.status === 405) ? { unavailable: true } : { ok: false, error: e.message, stages: [{ id: "credential", label: "Credential", ok: true }, { id: "request", label: "Request", ok: false, detail: e.message }] };
		}
		state.running = false;
		clearTimeout(state.timer);
		sendBtn.disabled = false;
		renderTrack(state.result);
		renderOutcome(state.result);
		paintSave();
		onChange?.();
	}
	async function save() {
		let body;
		try { body = recipe(); } catch (e) { toast(e.message, "bad"); return; }
		if (!body.model) { toast("Give the test a model to use.", "warn"); modelInput.focus(); return; }
		try {
			await engine.put(`/v8/neo/access/${encodeURIComponent(a.id)}/test/config`, { endpoint: body.endpoint, model: body.model, prompt: body.prompt ?? "", payload: body.payload });
			toast("Saved. Test on this account now sends this.");
			state.saved = true;
			state.savedAt = Date.now();
			paintSave();
			onChange?.();
		} catch (e) { toast(e.message, "bad"); }
	}
	async function forget() {
		if (!(await confirm({ title: "Forget the saved test?", text: "Test goes back to picking a cheap-looking model automatically.", confirmLabel: "Forget" }))) return;
		try {
			await engine.del(`/v8/neo/access/${encodeURIComponent(a.id)}/test/config`);
			state.saved = false;
			paintSave();
			toast("Saved test forgotten");
			onChange?.();
		} catch (e) { toast(e.message, "bad"); }
	}

	const dlg = dialog({
		title: `Test ${a.label}`,
		sub: `${provider?.name ?? providerId} · one request straight to this account. It does not count against anyone's limits.`,
		body: h("div", { class: "test-grid" },
			h("div", { class: "test-recipe" },
				field("Endpoint", h("div", null, endpointSeg, h("div", { class: "hint" }, "Sent to ", endpointPath, " in that protocol."))),
				field("Model", h("div", { class: "test-model" }, modelSelect, otherRow), models.length ? "Automatic picks a cheap-looking model. Pick one to test a specific model." : "Type the model id as the provider names it."),
				field("Prompt", promptInput),
				payloadDetails,
				h("div", { class: "test-actions" }, sendBtn, saveBtn, h("span", { class: "spacer" }), resetBtn),
				savedNote),
			resultCol),
		actions: (close) => [btn("Close", { onClick: close })],
		onClose: () => { state.running = false; clearTimeout(state.timer); },
	});
	const el = [...document.querySelectorAll(".dialog-backdrop")].at(-1)?.querySelector(".dialog");
	el?.classList.add("is-wide", "test-dialog");
	setTimeout(() => modelInput.focus(), 50);
	return dlg;
}
