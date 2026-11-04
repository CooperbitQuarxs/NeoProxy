// Connect: everything a teammate needs to point a tool at this proxy, as three
// steps on one screen: pick a model, choose the key the tool will use, copy the
// values into the tool. `#/connect?model=<served name>` opens on that model
// (My models' "Use it"). Each key says what it reaches and whether it can reach
// the chosen model; a key's secret is only known when it is minted here or
// pasted into this page (it never leaves the page). Admins see it too, so they
// can walk someone through it.

import { h, icon, copyText, plural, replace } from "../lib/dom.js";
import { neo } from "../lib/client.js";
import { pageHead, card, btn, servedName, copyLine, kv, seg, empty, spinner, errorState, toast, search, providerLabel } from "../lib/components.js";
import { mintKeyDialog, drawableAccounts, accountLabel } from "../lib/person-keys.js";
import { directModelsFor, matchModel } from "../lib/access.js";
import { vmLine, loadVmSpecs } from "../lib/vm-line.js";
import { propDetails, propNotes } from "../lib/vm-props.js";

const TOOL_KEY = "neo.connect.tool";
const KEY_HOLE = "<your key>";
// A key's secret, by key id, while this tab lives: minted here or pasted by the
// person. Module state, so a re-render keeps it; never stored, never sent.
const secrets = new Map();
const mintedHere = new Set();

// Tools that take a configuration file or code get a snippet; NeoCode asks for
// three values in its own screens, so it gets steps and three copyable fields.
function snippets(base, model, key) {
	const k = key || KEY_HOLE;
	return {
		neocode: { label: "NeoCode" },
		neopi: {
			label: "NeoPi",
			text: `# ~/.config/npi/providers.yaml\nneoproxy:\n  base_url: ${base}\n  api_key: ${k}\n\n# then pick a model with its full name, e.g.\nnpi --model ${model}`,
		},
		curl: {
			label: "curl",
			text: `curl ${base}/v1/chat/completions \\\n  -H "Authorization: Bearer ${k}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${model}","messages":[{"role":"user","content":"Say hello."}]}'`,
		},
		openai: {
			label: "OpenAI SDK",
			text: `from openai import OpenAI\n\nclient = OpenAI(base_url="${base}/v1", api_key="${k}")\nreply = client.chat.completions.create(\n    model="${model}",\n    messages=[{"role": "user", "content": "Say hello."}],\n)\nprint(reply.choices[0].message.content)`,
		},
		anthropic: {
			label: "Anthropic SDK",
			text: `import anthropic\n\nclient = anthropic.Anthropic(base_url="${base}", api_key="${k}")\nreply = client.messages.create(\n    model="${model}",\n    max_tokens=256,\n    messages=[{"role": "user", "content": "Say hello."}],\n)\nprint(reply.content[0].text)`,
		},
	};
}

const step = (n, title) => h("span", { class: "cx-step" }, h("span", { class: "cx-num", "aria-hidden": "true" }, String(n)), h("span", null, title));
const short = (name) => name.replace(/^neoproxy\//, "");

// Arrow keys move between the options of one radio group, like native radios.
function radioKeys(e, host, sel) {
	if (!["ArrowDown", "ArrowUp", "ArrowLeft", "ArrowRight"].includes(e.key)) return;
	e.preventDefault();
	const all = [...host.querySelectorAll(sel)].filter((el) => el.getAttribute("aria-disabled") !== "true");
	const i = all.indexOf(e.currentTarget);
	all[(i + (["ArrowDown", "ArrowRight"].includes(e.key) ? 1 : -1) + all.length) % all.length]?.focus();
}

export async function render(ctx) {
	const { root, session } = ctx;
	const head = pageHead({ title: "Connect", desc: "Point any OpenAI- or Anthropic-compatible tool at this proxy in three steps: pick a model, choose the key the tool will use, then copy the values into the tool." });
	head.classList.add("ax-head");
	root.append(head);
	const host = h("div", { class: "cx" });
	root.append(host, spinner());
	let me, vms, models, vault, specs;
	try {
		[me, vms, models, vault] = await Promise.all([neo.user(session.userId), neo.virtualModels(), neo.models().catch(() => []), neo.vault().catch(() => ({ accounts: [], providers: [] }))]);
		const granted0 = vms.filter((vm) => session.role === "admin" || me.grants.some((g) => g.virtualModel === vm.id));
		specs = await loadVmSpecs(granted0);
	} catch (e) {
		root.lastChild.replaceWith(errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();

	const granted = vms.filter((vm) => session.role === "admin" || me.grants.some((g) => g.virtualModel === vm.id));
	const direct = directModelsFor(me, models);
	const directName = (m) => m.served ?? `neoproxy/${m.id}`;
	const available = (name) => granted.some((v) => v.served === name) || direct.some((m) => directName(m) === name);
	// A model asked for in the address wins over the default, when this person may use it.
	const asked = ctx.query?.get("model") ?? "";
	let model = asked && available(asked) ? asked : granted[0]?.served ?? (direct[0] ? directName(direct[0]) : "neoproxy/default");
	const askedMissing = asked && !available(asked) ? asked : "";
	let tool = localStorage.getItem(TOOL_KEY) ?? "neocode";
	const base = location.origin;
	// Secrets live in `secrets` / `mintedHere` above, so a re-render keeps a freshly minted key filled in.
	let keyId = null;
	const phone = matchMedia("(max-width: 640px)");

	// ---------- what a key reaches ----------
	const accountOf = (id) => vault.accounts.find((a) => a.id === id);
	const providerOf = (pid) => vault.providers.find((p) => p.id === pid);
	const provName = (pid) => (providerOf(pid) ? providerLabel(providerOf(pid)).main : pid);
	const stageProvider = (st) => models.find((m) => m.id === st.model)?.providerId ?? st.model.split("/")[0];
	// "Claude · MoonBnuuy@protonmail.com", the same label the mint dialog and key tables use.
	function scopeWords(k) {
		if (!k.accountIds?.length) return k.models?.length ? `any account · ${plural(k.models.length, "direct model")} only` : "any account";
		const known = k.accountIds.map(accountOf).filter(Boolean);
		const named = known.length === 1 ? accountLabel(known[0], vault.providers, vault.accounts) : known.length ? plural(known.length, "account") : "";
		const hidden = k.accountIds.length - known.length;
		return [named, hidden ? `${plural(hidden, "account")} you can't see` : ""].filter(Boolean).join(" + ");
	}
	// { ok, text }: ok is true, false, "partial" (only some fallbacks) or null (can't tell).
	function reach(k, name) {
		const vm = granted.find((v) => v.served === name);
		const dm = direct.find((m) => directName(m) === name);
		const accts = k.accountIds?.length ? k.accountIds.map(accountOf) : null;
		if (accts && accts.some((a) => !a)) return { ok: null, text: "can't be checked from here" };
		if (dm) {
			if (k.models?.length && !matchModel(k.models, dm.id)) return { ok: false, text: "held to other direct models" };
			if (accts && !accts.some((a) => a.providerId === dm.providerId)) return { ok: false, text: `${[...new Set(accts.map((a) => provName(a.providerId)))].join(", ")} only` };
			return { ok: true, text: "works" };
		}
		if (vm) {
			if (!accts) return { ok: true, text: "works" };
			const hits = vm.stages.map((st, i) => (accts.some((a) => a.providerId === stageProvider(st) && (st.accounts === "all" || (Array.isArray(st.accounts) && st.accounts.includes(a.id)))) ? i : -1)).filter((i) => i >= 0);
			if (!hits.length) return { ok: false, text: `${[...new Set(accts.map((a) => provName(a.providerId)))].join(", ")} only` };
			if (hits[0] > 0) return { ok: "partial", text: `reaches only ${hits.length === 1 ? "fallback" : "fallbacks"} ${hits.join(", ")}, not the first choice` };
			return { ok: true, text: "works" };
		}
		return { ok: true, text: "" };
	}
	const usable = (k) => reach(k, model).ok !== false;
	function chooseDefaultKey() {
		const list = me.keys ?? [];
		if (keyId && list.some((k) => k.id === keyId && usable(k))) return;
		keyId = (list.find((k) => secrets.has(k.id) && usable(k)) ?? list.find((k) => reach(k, model).ok === true) ?? list.find(usable))?.id ?? null;
	}

	// ---------- 1. model ----------
	const modelHost = h("div", { class: "cx-models", role: "radiogroup", "aria-label": "Model to ask for" });
	const modelMore = h("div", { class: "cx-model-more" });
	let directOpen = false, directQuery = "";
	// On a phone the lists fold to the choice plus "Change", so the three steps stay close.
	let listOpen = false;
	function pick(name) {
		model = name;
		if (phone.matches) listOpen = false;
		chooseDefaultKey();
		paintModels();
		paintKeys();
		paintSnippet();
		modelHost.querySelector(`[data-name="${CSS.escape(name)}"]`)?.focus({ preventScroll: true });
	}
	const option = (name, body, { virtual, label }) => h("button", {
		type: "button", role: "radio", class: `cx-opt ${model === name ? "on" : ""}`, "aria-checked": String(model === name), "data-name": name,
		tabindex: model === name ? "0" : "-1",
		onClick: () => pick(name),
		onKeydown: (e) => radioKeys(e, modelHost, ".cx-opt"),
	}, h("span", { class: "cx-radio", "aria-hidden": "true" }), label
		? h("span", { class: "cx-opt-body cx-opt-line" }, h("span", { class: "cx-opt-name" }, label), servedName(name, { virtual }))
		: h("span", { class: "cx-opt-body" }, servedName(name, { virtual }), body));
	const virtualOption = (vm) => option(vm.served, h("span", { class: "cx-opt-sub" },
		vmLine(vm, { models, providers: vault.providers, specs }),
		vm.stages[0] ? propNotes(vm.stages[0], specs.get(vm.stages[0].model)) : null,
		vm.description ? h("span", { class: "cx-desc" }, vm.description) : null), { virtual: true });
	function paintModels() {
		const total = granted.length + direct.length;
		const vmPicked = granted.find((v) => v.served === model);
		// The chosen virtual model's settings, spelled out, under the list (never inside a radio).
		replace(modelMore,
			askedMissing ? h("p", { class: "cx-note warn" }, icon("info", 14), h("span", null, h("code", null, askedMissing), " isn't available to you, so the first model you may use is picked instead.")) : null,
			vmPicked ? propDetails(vmPicked.stages.map((s) => ({ name: models.find((m) => m.id === s.model)?.display ?? s.model, stage: s, specs: specs.get(s.model) })), { summary: "What these settings mean" }) : null);
		if (phone.matches && !listOpen && total > 1) {
			const dm = direct.find((m) => directName(m) === model);
			modelHost.replaceChildren(
				vmPicked ? virtualOption(vmPicked) : dm ? option(directName(dm), null, { virtual: false, label: dm.display }) : "",
				btn(`Change model · ${total} available`, { icon: "chevronDown", cls: "outline sm cx-change", onClick: () => { listOpen = true; paintModels(); modelHost.querySelector(".cx-opt.on")?.focus(); } }));
			return;
		}
		const virtual = granted.map(virtualOption);
		const q = directQuery.trim().toLowerCase();
		const directRows = direct.filter((m) => !q || `${m.id}\n${m.display}`.toLowerCase().includes(q));
		const pickedDirect = direct.find((m) => directName(m) === model);
		const directBlock = direct.length ? h("div", { class: "cx-direct" },
			h("button", { type: "button", class: "cx-direct-toggle", "aria-expanded": String(directOpen || !!pickedDirect), onClick: () => { directOpen = !(directOpen || pickedDirect); if (!directOpen && pickedDirect) model = granted[0]?.served ?? model; chooseDefaultKey(); paintModels(); paintKeys(); paintSnippet(); } },
				icon(directOpen || pickedDirect ? "chevronDown" : "chevronRight", 14),
				h("span", null, `Or call a provider model directly`), h("span", { class: "ax-fact" }, `${plural(direct.length, "model")}, no fallbacks`)),
			directOpen || pickedDirect ? h("div", { class: "cx-direct-list" },
				direct.length > 8 ? search("Find a model", (v) => { directQuery = v; paintModels(); modelHost.querySelector(".cx-direct input")?.focus(); }, { value: directQuery, "aria-label": "Find a direct model" }) : null,
				directRows.slice(0, 60).map((m) => option(directName(m), null, { virtual: false, label: m.display })),
				directRows.length > 60 ? h("div", { class: "cx-more" }, `${directRows.length - 60} more; search to narrow`) : null,
				directRows.length ? null : h("div", { class: "cx-more" }, "No direct model matches.")) : null) : null;
		modelHost.replaceChildren(
			...(virtual.length ? virtual : [h("div", { class: "cx-none" }, empty({ icon: "route", title: "No virtual model granted yet", hint: session.role === "admin" ? "Create one under Virtual models, or grant yourself one under Users." : "Ask an admin to grant you a virtual model." }))]),
			directBlock ?? "");
	}
	const onPhone = () => { paintModels(); paintKeys(); };
	phone.addEventListener("change", onPhone);

	// ---------- 2. key ----------
	// The chosen key on one line. Other keys that work, and keys that can't reach
	// this model, each stay behind one toggle. The secret field appears only when
	// asked for ("I have this key's secret").
	const keyBody = h("div", { class: "cx-keys" });
	let othersOpen = false, blockedOpen = false, secretOpen = false;
	const secretOf = () => (keyId ? secrets.get(keyId) ?? "" : "");
	function pickKey(id) {
		keyId = id;
		othersOpen = false;
		secretOpen = false;
		paintKeys();
		paintSnippet();
		keyBody.querySelector(`.cx-key[data-key="${CSS.escape(id)}"]`)?.focus({ preventScroll: true });
	}
	function keyLine(k, group) {
		const r = reach(k, model);
		const on = keyId === k.id;
		const blocked = r.ok === false;
		const status = blocked ? (r.text === "held to other direct models" ? r.text : "can't reach this model") : r.text;
		const mark = blocked ? "x" : r.ok === true ? "check" : "info";
		return h("button", {
			type: "button", role: "radio", class: `cx-key ${on ? "on" : ""} ${blocked ? "blocked" : ""}`, "data-key": k.id,
			"aria-checked": String(on), "aria-disabled": String(blocked), tabindex: on || !keyId ? "0" : "-1",
			onClick: () => { if (!blocked) pickKey(k.id); },
			onKeydown: (e) => radioKeys(e, group, ".cx-key"),
		},
		h("span", { class: "cx-radio", "aria-hidden": "true" }),
		h("span", { class: "cx-key-line" },
			h("b", null, k.label || "Unnamed key"), " · ", h("span", null, scopeWords(k)),
			status ? [" · ", h("span", { class: `cx-key-fit ${blocked ? "bad" : r.ok === true ? "ok" : "warn"}` }, icon(mark, 12), status)] : null));
	}
	// The secret of an existing key, typed by the person. It stays on this page.
	function secretEntry(k) {
		const [start, end] = (k.prefix ?? "").split("…");
		const note = h("div", { class: "cx-secret-note", "aria-live": "polite" });
		const show = (v) => {
			const fits = !v || ((!start || v.startsWith(start)) && (!end || v.endsWith(end)));
			note.className = `cx-secret-note ${fits ? "ok" : "bad"}`;
			note.textContent = !v ? "" : fits ? "Filled in below. It stays on this page." : `That isn't this key's secret: it starts ${start} and ends ${end}.`;
			return fits;
		};
		const input = h("input", {
			class: "input mono", type: "password", autocomplete: "off", spellcheck: "false", placeholder: `${k.prefix}`, id: "cx-secret",
			"aria-label": `Secret of ${k.label || "this key"}`, value: secrets.get(k.id) ?? "",
			onInput: (e) => {
				const v = e.target.value.trim();
				if (show(v) && v) secrets.set(k.id, v); else secrets.delete(k.id);
				paintSnippet();
			},
		});
		show(input.value);
		return h("div", { class: "cx-secret" }, input, note);
	}
	function paintKeys() {
		chooseDefaultKey();
		const list = me.keys ?? [];
		const chosen = list.find((k) => k.id === keyId);
		const others = list.filter((k) => k !== chosen && reach(k, model).ok !== false);
		const blocked = list.filter((k) => reach(k, model).ok === false);
		const group = h("div", { class: "cx-keylist", role: "radiogroup", "aria-label": "Key this tool will use" });
		group.append(...(chosen ? [keyLine(chosen, group)] : []), ...(othersOpen ? others.map((k) => keyLine(k, group)) : []));
		const blockedList = h("div", { class: "cx-keylist", role: "radiogroup", "aria-label": "Keys that can't reach this model" });
		if (blockedOpen) blockedList.append(...blocked.map((k) => keyLine(k, blockedList)));
		const toggle = (text, open, flip) => h("button", { type: "button", class: "cx-toggle", "aria-expanded": String(open), onClick: flip }, icon(open ? "chevronDown" : "chevronRight", 13), text);
		const minted = chosen && mintedHere.has(chosen.id);
		replace(keyBody,
			chosen ? group : h("p", { class: "cx-none-fit" }, list.length ? `None of your keys reaches ${model}.` : "You have no key yet."),
			minted ? h("p", { class: "cx-fresh" }, icon("check", 13), "New key filled in below. Copy it now: it can't be shown again once this tab closes.") : null,
			chosen && !minted ? (secretOpen || secrets.has(chosen.id)
				? secretEntry(chosen)
				: toggle("I have this key's secret", false, () => { secretOpen = true; paintKeys(); keyBody.querySelector("#cx-secret")?.focus(); })) : null,
			others.length ? toggle(othersOpen ? "Hide other keys" : `Use another key (${others.length})`, othersOpen, () => { othersOpen = !othersOpen; paintKeys(); }) : null,
			blocked.length ? toggle(`${plural(blocked.length, "key")} can't be used here`, blockedOpen, () => { blockedOpen = !blockedOpen; paintKeys(); }) : null,
			blockedOpen ? blockedList : null,
			h("div", { class: "cx-key-acts" }, btn(`Mint a key for ${model}`, { icon: "key", cls: `${chosen ? "outline" : "primary"} sm`, onClick: mintKey })));
	}
	async function mintKey() {
		mintKeyDialog({
			user: me,
			accounts: me.role === "owner" ? drawableAccounts(me, vault.accounts) : [],
			providers: vault.providers,
			models: direct,
			label: `${snippets(base, model)[tool]?.label ?? "NeoCode"} for ${short(model)}`,
			onMinted: (k, secret) => {
				me.keys = [...(me.keys ?? []), k];
				mintedHere.add(k.id);
				secrets.set(k.id, secret);
				keyId = k.id;
				paintKeys();
				paintSnippet();
			},
		});
	}

	// ---------- 3. the values ----------
	const snippetHost = h("div", { class: "cx-snippet" });
	const valueRow = (label, value, { copyLabel, placeholder, note } = {}) => h("div", { class: "cx-val" },
		h("div", { class: "cx-val-k" }, label),
		h("div", { class: "cx-val-v" }, value ? h("code", null, value) : h("span", { class: "cx-val-hole" }, placeholder)),
		value ? btn(copyLabel, { icon: "copy", cls: "outline sm", onClick: async () => { if (await copyText(value)) toast(`Copied the ${label.toLowerCase()}`); else toast("Couldn't copy; select the text instead", "bad"); } }) : h("span"),
		note ? h("div", { class: "cx-val-note ax-fact" }, note) : null);
	function paintSnippet() {
		const secret = secretOf();
		const chosen = (me.keys ?? []).find((k) => k.id === keyId);
		const all = snippets(base, model, secret);
		if (!all[tool]) tool = "neocode";
		const tools = h("div", { class: "cx-tools" }, seg(Object.entries(all).map(([id, s]) => ({ value: id, label: s.label })), tool, (v) => { tool = v; localStorage.setItem(TOOL_KEY, v); paintSnippet(); snippetHost.querySelector(".seg [aria-pressed=true]")?.focus(); }));
		const dm = direct.find((m) => directName(m) === model);
		if (tool === "neocode") {
			snippetHost.replaceChildren(tools,
				h("ol", { class: "cx-howto" },
					h("li", null, "In NeoCode, open ", h("b", null, "Models → NeoProxy → Connect"), "."),
					h("li", null, "Paste the address, then the key when NeoCode asks for it."),
					h("li", null, "Pick ", dm ? [h("b", null, dm.display), " (", h("code", null, model), ")"] : h("code", null, model), " from the model list.")),
				h("div", { class: "cx-vals" },
					valueRow("Address", base, { copyLabel: "Copy address", note: "No /v1 at the end; NeoCode adds it." }),
					valueRow("Key", secret, { copyLabel: "Copy key", placeholder: chosen ? "paste its secret in step 2" : "mint a key in step 2" }),
					valueRow("Model", model, { copyLabel: "Copy model" })));
			return;
		}
		const code = all[tool].text;
		snippetHost.replaceChildren(tools,
			h("div", { class: "snippet-wrap" },
				h("div", { class: "snippet-head" }, h("span", null, h("span", { class: "ax-fact" }, "for "), servedName(model, { virtual: granted.some((v) => v.served === model) })),
					btn("Copy snippet", { icon: "copy", cls: "sm outline", onClick: async () => { if (await copyText(code)) toast(`Copied the ${all[tool].label} snippet`); else toast("Couldn't copy; select the text instead", "bad"); } })),
				h("pre", { class: "code snippet", tabindex: 0, "aria-label": `${all[tool].label} snippet` }, code)),
			secret ? "" : h("p", { class: "ax-fact cx-hint" }, "Replace ", h("code", null, KEY_HOLE), " with your key."));
	}

	chooseDefaultKey();
	paintModels();
	paintKeys();
	paintSnippet();
	// Reference addresses for other clients, folded away below the steps.
	const addresses = h("details", { class: "card cx-addr-card" },
		h("summary", { class: "cx-addr-sum" }, icon("chevronRight", 13), h("span", null, "Other API addresses")),
		h("div", { class: "cx-addr-body" },
			h("p", { class: "ax-fact" }, "Keys go in Authorization: Bearer <key>, or x-api-key for Anthropic clients. A dashboard session is not a key."),
			h("div", { class: "ak-addr" }, kv([
				["Base", copyLine(base, "base address")],
				["OpenAI", copyLine(`${base}/v1`, "OpenAI address")],
				["Anthropic", copyLine(`${base}/v1/messages`, "Anthropic messages address")],
				["Models", copyLine(`${base}/v1/models`, "model list address")],
			]))));
	host.append(
		h("div", { class: "cx-left" },
			card({ title: step(1, "Pick a model"), sub: "Ask for it by this name. A virtual model falls back for you when its first choice is busy.", body: [modelHost, modelMore] })),
		h("div", { class: "cx-right" },
			card({ title: step(2, "Choose the key this tool uses"), body: keyBody }),
			card({ title: step(3, "Put it into your tool"), body: snippetHost }),
			addresses),
	);
	return () => phone.removeEventListener("change", onPhone);
}
