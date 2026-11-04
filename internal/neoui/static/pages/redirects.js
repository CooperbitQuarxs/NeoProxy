// Redirects: requests for one model go to another, for everyone, some people
// or some of their keys. The caller keeps asking for the old name; the
// resolver swaps it before anything else and records the swap on the request.
// A redirect never grants access unless an admin turns on Bypass access.

import { h, icon, plural, clear } from "../lib/dom.js";
import { neo, onChange, ApiError } from "../lib/client.js";
import { explainDecision } from "../lib/explain.js";
import { pageHead, card, btn, iconBtn, avatar, servedName, field, input, seg, toggle, table, empty, spinner, errorState, picker, providerDot, dialog, confirm, toast, trail, tag, badge, redirectRule } from "../lib/components.js";

const PREFIX = "neoproxy/";
const canonical = (s) => {
	const v = s.trim();
	return !v || v.startsWith(PREFIX) ? v : PREFIX + v;
};
const isVirtual = (served) => !served.slice(PREFIX.length).includes("/");
const isPrefix = (from) => from.endsWith("*");
const matches = (from, asked) => (isPrefix(from) ? asked.startsWith(from.slice(0, -1)) : asked === from);

// A served name as people read it: the model's display name (or what a pattern or virtual
// model means) on top, the exact id underneath.
function modelLabel(served, { models, vms }) {
	const id = served.slice(PREFIX.length);
	let name;
	if (isPrefix(served)) name = `Every model starting with ${id.slice(0, -1)}`;
	else if (isVirtual(served)) name = vms.find((v) => v.name === id)?.description || "Virtual model";
	else name = models.find((m) => m.id === id)?.display ?? "Not served today";
	return h("span", { class: "rd-label" }, h("span", { class: "rd-model-name" }, name), servedName(served, { virtual: isVirtual(served) }));
}

export async function render(ctx) {
	const { root } = ctx;
	const add = btn("Add redirect", { icon: "plus", cls: "primary" });
	root.append(pageHead({
		title: "Redirects",
		desc: "Send requests for one model to another, for everyone, some people, or one of their keys. Clients keep asking for the old name; every request a redirect changes says so in Activity.",
		actions: [add],
	}));
	const host = h("div");
	root.append(host, spinner());
	let redirects, users, vms, models, vault;
	try {
		[redirects, users, vms, models, vault] = await Promise.all([neo.redirects(), neo.users(), neo.virtualModels(), neo.models(), neo.vault()]);
	} catch (e) {
		add.disabled = true;
		root.lastChild.replaceWith(e instanceof ApiError && e.status === 404
			? card({ body: empty({ icon: "redirect", title: "This server has no redirects yet", hint: "Redirects need a newer NeoProxy build. Update the server, then reload this page." }) })
			: errorState(e, () => ctx.rerender()));
		return;
	}
	root.lastChild.remove();
	const world = { users, vms, models, vault, options: modelOptions(vms, models, vault) };
	add.addEventListener("click", () => editRedirect(null, world));

	const paint = () => {
		clear(host);
		if (!redirects.length) {
			host.append(card({ body: empty({
				icon: "redirect",
				title: "No redirects",
				hint: "Move everyone, one person or one key from a model to another, for example from a retired model to its replacement.",
				action: btn("Add redirect", { icon: "plus", cls: "outline", onClick: () => editRedirect(null, world) }),
			}) }));
			return;
		}
		const keep = (node) => h("span", { class: "rd-keep", onClick: (e) => e.stopPropagation(), onKeydown: (e) => e.stopPropagation() }, node);
		host.append(card({
			flush: true,
			cls: "rd-card",
			body: table([
				{ key: "from", label: "Requests for", wrap: true, render: (r) => h("span", { class: "rd-model" }, modelLabel(r.from, world)) },
				{ key: "to", label: "Go to", wrap: true, render: (r) => h("span", { class: "rd-to" }, icon("arrowRight", 14), modelLabel(r.to, world)) },
				{ key: "who", label: "Who", wrap: true, render: (r) => whoChips(r.scope, users) },
				{ key: "note", label: "Note", wrap: true, render: (r) => h("span", { class: "rd-note" }, r.note ? r.note : h("span", { class: "ink-3" }, "No note"), r.bypassAccess ? badge("Bypasses access", "warn") : null) },
				{ key: "enabled", label: "On", width: "64px", render: (r) => keep(toggle(r.enabled, async (on) => {
					try {
						await neo.saveRedirect({ ...r, enabled: on });
						toast(on ? "Redirect turned on" : "Redirect turned off");
					} catch (e) {
						toast(e.message, "bad", 6000);
						throw e;
					}
				}, `Redirect ${r.from} ${r.enabled ? "on" : "off"}`, { showLabel: false })) },
				{ key: "actions", label: "", width: "84px", render: (r) => keep(h("span", { class: "row rd-actions" },
					iconBtn("settings", "Edit redirect", () => editRedirect(r, world), "sm"),
					iconBtn("trash", "Delete redirect", () => removeRedirect(r), "sm"),
				)) },
			], redirects, { onRow: (r) => editRedirect(r, world), stack: true }),
			foot: h("span", { class: "rd-precedence" }, `${plural(redirects.length, "redirect")}. When several match one request, a key's redirect wins over a person's, which wins over everyone's; then an exact model over a prefix ending in *; then the older one. A redirect never leads to another redirect.`),
		}));
	};
	paint();
	const off = onChange(async (what) => {
		if (what !== "redirects") return;
		try { redirects = await neo.redirects(); paint(); } catch (e) { toast(e.message, "bad"); }
	});
	return () => off();
}

async function removeRedirect(r) {
	if (!await confirm({ title: "Delete this redirect?", text: `Requests for ${r.from} will go to ${r.from} itself again.`, confirmLabel: "Delete redirect", danger: true })) return false;
	try {
		await neo.deleteRedirect(r.id);
		toast("Redirect deleted");
		return true;
	} catch (e) {
		toast(e.message, "bad", 6000);
		return false;
	}
}

// Everything a caller can ask for: virtual models, then exposed provider models.
function modelOptions(vms, models, vault) {
	const providers = new Map((vault.providers ?? []).map((p) => [p.id, p]));
	const out = vms.map((vm) => ({ value: vm.served, label: vm.served, sub: vm.description || "Virtual model", lead: h("span", { class: "rd-lead vm" }, icon("route", 13)) }));
	for (const m of models) {
		const p = providers.get(m.providerId);
		if (!m.exposed || m.virtualOnly || (p && p.exposed === false)) continue;
		out.push({ value: m.served, label: m.served, sub: `${m.display ?? m.name}${p ? ` from ${p.name}` : ""}`, lead: providerDot(p, 8) });
	}
	return out;
}

function keyIndex(users) {
	const keys = new Map();
	for (const u of users) for (const k of u.keys ?? []) keys.set(k.id, { key: k, user: u });
	return keys;
}

function whoChips(scope = {}, users) {
	const byId = new Map(users.map((u) => [u.id, u]));
	if (scope.keys?.length) {
		const keys = keyIndex(users);
		return h("span", { class: "rd-chips" }, scope.keys.map((id) => {
			const k = keys.get(id);
			return h("span", { class: "chip rd-chip key", title: k ? `${k.user.handle}'s key ${k.key.prefix}` : "Revoked key" }, icon("key", 12), k ? [h("span", { class: "who" }, `${k.user.handle}'s`), ` ${k.key.label || k.key.prefix}`] : h("span", { class: "ink-3" }, "revoked key"));
		}));
	}
	if (scope.users?.length) {
		return h("span", { class: "rd-chips" }, scope.users.map((id) => {
			const u = byId.get(id);
			return h("span", { class: "chip rd-chip" }, u ? [avatar(u, "sm"), h("span", { class: "who" }, u.handle)] : h("span", { class: "ink-3" }, "removed user"));
		}));
	}
	return tag("Everyone");
}

// "a, b and c" with nodes kept as nodes.
const listWords = (items) => items.flatMap((item, i) => [i === 0 ? "" : i === items.length - 1 ? " and " : ", ", item]);

// "Requests for X from santiago's laptop key go to Y instead."
function sentence(draft, users) {
	const from = canonical(draft.from);
	const to = canonical(draft.to);
	if (!from || !to) return h("p", { class: "rd-say muted" }, "Choose the model people ask for and where it should go.");
	const mono = (s) => h("span", { class: `rd-name ${isVirtual(s) ? "vm" : ""}` }, s);
	const byId = new Map(users.map((u) => [u.id, u]));
	const keys = keyIndex(users);
	let who;
	const person = (handle) => h("span", { class: "rd-person-name" }, handle);
	if (draft.mode === "keys") who = draft.keys.length ? ["from ", listWords(draft.keys.map((id) => keys.get(id)).filter(Boolean).map(({ key, user }) => [person(`${user.handle}'s`), ` ${key.label || key.prefix} key`]))] : "from no key yet";
	else if (draft.mode === "users") who = draft.users.length ? ["from ", listWords(draft.users.map((id) => person(byId.get(id)?.handle ?? "a removed user")))] : "from nobody yet";
	else who = "from anyone";
	const subject = isPrefix(from) ? ["Requests for any model starting with ", mono(from.slice(0, -1))] : ["Requests for ", mono(from)];
	return h("div", { class: "rd-say" },
		h("p", null, subject, " ", who, " go to ", mono(to), " instead."),
		h("p", { class: "rd-say-sub" }, !draft.enabled ? "This redirect is off, so nothing changes until you turn it on."
			: draft.bypassAccess ? `They are served even if they have no access to ${to}.`
			: `They still need access to ${to}. A redirect does not grant it.`),
	);
}

// A text field for a served name with a searchable list beside it.
function modelField({ value, placeholder, options, onInput, allowPrefix }) {
	const box = input({ mono: true, value, placeholder, autocomplete: "off", spellcheck: "false", "aria-label": placeholder });
	box.addEventListener("input", () => onInput(box.value));
	const choose = btn("Choose", { icon: "search", cls: "outline sm" });
	choose.setAttribute("aria-haspopup", "dialog");
	choose.addEventListener("click", () => picker(choose, {
		placeholder: allowPrefix ? "Find a model, or type a prefix in the field" : "Find a model",
		items: options,
		onPick: (served) => { box.value = served; onInput(served); box.focus(); },
	}));
	return h("div", { class: "rd-field" }, box, choose);
}

function editRedirect(saved, world) {
	const { users, options } = world;
	const scope = saved?.scope ?? {};
	const draft = {
		from: saved?.from ?? "",
		to: saved?.to ?? "",
		mode: scope.keys?.length ? "keys" : scope.users?.length ? "users" : "everyone",
		users: [...(scope.users ?? [])],
		keys: [...(scope.keys ?? [])],
		note: saved?.note ?? "",
		enabled: saved?.enabled ?? true,
		bypassAccess: saved?.bypassAccess ?? false,
	};
	const known = new Set(options.map((o) => o.value));
	const payload = () => ({
		id: saved?.id,
		from: canonical(draft.from),
		to: canonical(draft.to),
		scope: draft.mode === "keys" ? { keys: draft.keys } : draft.mode === "users" ? { users: draft.users } : {},
		note: draft.note.trim(),
		enabled: draft.enabled,
		bypassAccess: draft.bypassAccess,
	});
	const savedJSON = saved ? JSON.stringify(payload()) : null;

	// Live checks; the server repeats them and also rejects chains.
	const fromNote = h("div", { class: "hint" });
	const toNote = h("div", { class: "hint" });
	const say = h("div");
	const whoHost = h("div", { class: "rd-who" });
	const whoError = h("div", { class: "error" });
	const bypassWarn = h("p", { class: "rd-warn" });
	const save = btn(saved ? "Save changes" : "Add redirect", { cls: "primary" });
	const problems = () => {
		const from = canonical(draft.from), to = canonical(draft.to);
		const out = {};
		if (!from || from === PREFIX || from === `${PREFIX}*`) out.from = "Enter the model people ask for.";
		else if (/[*\s]/.test(from.slice(0, -1)) || /\s/.test(from)) out.from = "Only one * is allowed, at the end, and no spaces.";
		if (!to) out.to = "Choose where these requests go.";
		else if (/[*\s]/.test(to)) out.to = "Choose one model; * and spaces are not allowed here.";
		else if (!known.has(to)) out.to = "Nothing on this proxy is served under that name.";
		else if (from && matches(from, to)) out.to = "That would send the model to itself.";
		if (draft.mode === "users" && !draft.users.length) out.who = "Pick at least one person.";
		if (draft.mode === "keys" && !draft.keys.length) out.who = "Pick at least one key.";
		return out;
	};
	const refresh = () => {
		const p = problems();
		const from = canonical(draft.from);
		let fromHint = "End with * to match every model whose name starts with it.";
		if (isPrefix(from) && !p.from) {
			const hits = options.filter((o) => matches(from, o.value));
			fromHint = hits.length ? `Matches ${plural(hits.length, "model")} today: ${hits.slice(0, 3).map((o) => o.value.slice(PREFIX.length)).join(", ")}${hits.length > 3 ? ", …" : ""}.` : "Matches no model today; it applies as soon as one appears.";
		} else if (from && !p.from && !known.has(from)) fromHint = "Not served today. Requests for it still redirect, which is how you retire a name.";
		fromNote.className = p.from && draft.from.trim() ? "error" : "hint";
		fromNote.textContent = p.from ?? fromHint;
		toNote.className = p.to && draft.to.trim() ? "error" : "hint";
		toNote.textContent = p.to ?? (isVirtual(canonical(draft.to)) ? "A virtual model: its stages and thresholds then apply." : "A direct provider model: the engine's routing strategy picks the account.");
		say.replaceChildren(sentence(draft, users));
		bypassWarn.hidden = !draft.bypassAccess;
		bypassWarn.textContent = `Everyone this redirect covers can use ${canonical(draft.to) || "the target"} through it, even with no grant or provider access of their own. Their key's account limits still apply.`;
		save.disabled = Object.keys(p).length > 0;
		// An empty "some people" list is the next step, not a mistake: say it as guidance (Save stays disabled until it is done).
		whoError.className = "hint";
		whoError.textContent = p.who ?? "";
		whoError.hidden = !p.who;
		tryNote.hidden = savedJSON !== null && JSON.stringify(payload()) === savedJSON;
	};

	const paintWho = () => {
		whoHost.replaceChildren(
			seg([{ value: "everyone", label: "Everyone" }, { value: "users", label: "Some people" }, { value: "keys", label: "Some keys" }], draft.mode, (mode) => { draft.mode = mode; paintWho(); refresh(); }),
			draft.mode === "users" ? peopleChooser() : draft.mode === "keys" ? keyChooser() : h("p", { class: "rd-who-note" }, "Every person, whichever key they use."),
			whoError,
		);
	};
	const peopleChooser = () => {
		const addPerson = btn("Add person", { icon: "plus", cls: "outline sm" });
		addPerson.setAttribute("aria-haspopup", "dialog");
		addPerson.addEventListener("click", () => picker(addPerson, {
			placeholder: "Find a person",
			items: users.filter((u) => !draft.users.includes(u.id)).map((u) => ({ value: u.id, label: u.handle, sub: [u.name, u.role].filter(Boolean).join(", "), lead: avatar(u, "sm") })),
			empty: "Everyone is already picked",
			onPick: (id) => { draft.users.push(id); paintWho(); refresh(); },
		}));
		return h("div", { class: "rd-chips edit" },
			draft.users.map((id) => {
				const u = users.find((x) => x.id === id);
				return h("span", { class: "chip rd-chip" }, u ? avatar(u, "sm") : null, h("span", { class: "who" }, u?.handle ?? "removed user"),
					iconBtn("x", `Remove ${u?.handle ?? "user"}`, () => { draft.users = draft.users.filter((x) => x !== id); paintWho(); refresh(); }, "sm"));
			}),
			addPerson,
		);
	};
	// Keys are listed under their owner; a key-scoped redirect covers only
	// requests made with that key, not the person's other keys.
	const keyChooser = () => {
		const owners = users.filter((u) => u.keys?.length);
		if (!owners.length) return h("p", { class: "rd-who-note" }, "Nobody has an inference key yet.");
		return h("div", { class: "rd-keys" }, owners.map((u) => h("div", { class: "rd-owner" },
			h("div", { class: "rd-owner-head" }, avatar(u, "sm"), h("span", { class: "who" }, u.handle), h("span", { class: "ink-3" }, plural(u.keys.length, "key"))),
			h("div", { class: "rd-owner-keys" }, u.keys.map((k) => {
				const box = h("input", { type: "checkbox", checked: draft.keys.includes(k.id) });
				box.addEventListener("change", () => {
					draft.keys = box.checked ? [...draft.keys, k.id] : draft.keys.filter((x) => x !== k.id);
					refresh();
				});
				return h("label", { class: "rd-key" }, box, h("span", null, k.label || "Unnamed key"), h("span", { class: "mono ink-3" }, k.prefix));
			})),
		)));
	};

	// Try it: simulate a real request for a person (and key) against saved redirects.
	const tryNote = h("p", { class: "rd-try-note" }, "Try it uses saved redirects. Save first to include your changes.");
	const tryOut = h("div", { class: "rd-try-out" });
	let tryUser = users.find((u) => scope.users?.includes(u.id)) ?? users[0];
	let tryKey = "";
	const personBtn = btn("", { cls: "outline sm rd-person" });
	const keySelect = h("select", { class: "select sm", "aria-label": "Key" });
	const askBox = input({ class: "input mono sm", placeholder: "Model to ask for (optional)", "aria-label": "Model to ask for" });
	const paintTryPerson = () => {
		personBtn.replaceChildren(tryUser ? avatar(tryUser, "sm") : null, tryUser?.handle ?? "Pick a person", icon("chevronDown", 13));
		keySelect.replaceChildren(h("option", { value: "" }, "Dashboard session"), ...(tryUser?.keys ?? []).map((k) => h("option", { value: k.id, selected: k.id === tryKey }, `${k.label || "Key"} ${k.prefix}`)));
	};
	personBtn.addEventListener("click", () => picker(personBtn, {
		placeholder: "Find a person",
		items: users.map((u) => ({ value: u.id, label: u.handle, sub: u.name, lead: avatar(u, "sm") })),
		onPick: (id) => { tryUser = users.find((u) => u.id === id); tryKey = ""; paintTryPerson(); },
	}));
	keySelect.addEventListener("change", () => { tryKey = keySelect.value; });
	if (scope.keys?.length) {
		const owner = keyIndex(users).get(scope.keys[0]);
		if (owner) { tryUser = owner.user; tryKey = owner.key.id; }
	}
	const run = btn("Try it", { icon: "play", cls: "outline sm" });
	run.addEventListener("click", async () => {
		const from = canonical(draft.from);
		const asked = canonical(askBox.value) || (isPrefix(from) ? options.find((o) => matches(from, o.value))?.value ?? from.slice(0, -1) : from);
		if (!tryUser || !asked) return;
		run.disabled = true;
		tryOut.replaceChildren(h("p", { class: "rd-try-note" }, "Asking the router…"));
		try {
			const decision = await neo.simulate({ asked, userId: tryUser.id, keyId: tryKey || undefined });
			const record = { decision, asked, userId: tryUser.id, outcome: decision.outcome };
			const words = explainDecision(record, { models: world.models, accounts: world.vault.accounts, users, vms: world.vms });
			tryOut.replaceChildren(
				h("p", { class: "rd-try-head" }, decision.redirect ? icon("redirect", 14) : null,
					decision.redirect ? (decision.redirect.id === saved?.id ? "This redirect applies." : `A different redirect applies: ${redirectRule(decision.redirect)}.`) : `No redirect applies to ${tryUser.handle}${tryKey ? "'s key" : ""} asking for ${asked}.`),
				h("p", { class: "rd-try-words" }, words.sentences.join(" ")),
				words.hint ? h("p", { class: "rd-try-hint" }, words.hint) : "",
				trail(decision, { models: world.models, accounts: world.vault.accounts, users, admin: true, who: tryUser.handle }),
			);
		} catch (e) {
			tryOut.replaceChildren(h("p", { class: "error" }, e.message));
		} finally {
			run.disabled = false;
		}
	});
	paintTryPerson();

	const body = h("div", { class: "rd-form" },
		h("div", { class: "rd-pair" },
			field("Requests for", h("div", { class: "rd-col" }, modelField({ value: draft.from, placeholder: "neoproxy/codex/gpt-6-astra", options, allowPrefix: true, onInput: (v) => { draft.from = v; refresh(); } }), fromNote)),
			h("span", { class: "rd-pair-arrow", "aria-hidden": "true" }, icon("arrowRight", 16)),
			field("Go to", h("div", { class: "rd-col" }, modelField({ value: draft.to, placeholder: "neoproxy/codex/gpt-6.1-sol", options, onInput: (v) => { draft.to = v; refresh(); } }), toNote)),
		),
		h("div", { class: "field" }, h("label", null, "Who it applies to"), whoHost),
		field("Note", input({ value: draft.note, maxlength: 280, placeholder: "Why this redirect exists, e.g. Astra retired on Friday", onInput: (e) => { draft.note = e.target.value; refresh(); } })),
		h("div", { class: "rd-switches" },
			toggle(draft.enabled, (on) => { draft.enabled = on; refresh(); }, "On"),
			toggle(draft.bypassAccess, (on) => { draft.bypassAccess = on; refresh(); }, "Bypass access"),
		),
		bypassWarn,
		say,
		// Try it asks the router, which only knows saved redirects: it belongs to an existing rule, not to a new one.
		saved ? h("div", { class: "rd-try" },
			h("div", { class: "rd-try-bar" }, h("span", { class: "rd-try-label" }, "Try it as"), personBtn, keySelect, askBox, run),
			tryNote,
			tryOut,
		) : null,
	);
	const dlg = dialog({
		title: saved ? "Edit redirect" : "Add redirect",
		sub: "The person keeps asking for the old name. Activity records every request this redirect changes.",
		body,
		actions: (close) => [
			saved ? btn("Delete", { icon: "trash", cls: "danger outline rd-delete", onClick: async () => { if (await removeRedirect(saved)) close(); } }) : null,
			btn("Cancel", { onClick: close }),
			(save.addEventListener("click", async () => {
				save.disabled = true;
				try {
					await neo.saveRedirect(payload());
					toast(saved ? "Redirect saved" : "Redirect added");
					close();
				} catch (e) {
					toast(e.message, "bad", 8000);
					save.disabled = false;
				}
			}), save),
		],
	});
	[...document.querySelectorAll(".dialog-backdrop")].at(-1)?.querySelector(".dialog")?.classList.add("is-wide", "rd-dialog");
	paintWho();
	refresh();
	return dlg;
}
