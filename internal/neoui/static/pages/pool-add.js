// "Add provider access": one dialog for the five ways access reaches the pool.
// Step 1 picks a provider (or one of the ways that are not tied to a single
// provider); step 2 offers that provider's methods as tabs. Every way ends in
// the same pool, so the copy says what happens and what to do when it fails.

import { h, icon, replace, fmtInt, copyText } from "../lib/dom.js";
import { neo, ApiError } from "../lib/client.js";
import { btn, field, input, select, search, seg, dialog, copyLine, toast } from "../lib/components.js";

export const KIND_COPY = {
	subscription: {
		title: "Sign in with an account",
		summary: "Use a subscription like ChatGPT Pro or Claude Max.",
		detail: "You sign in on the provider's own page. NeoProxy keeps the sign-in and refreshes it automatically, so it keeps working.",
		goodFor: "Monthly subscriptions people already pay for.",
	},
	apiKey: {
		title: "Paste an API key",
		summary: "Add a pay-as-you-go key from OpenAI, Anthropic, OpenRouter and others.",
		detail: "NeoProxy checks the key by listing the provider's models, then stores it. Usage is billed by the provider to whoever owns the key.",
		goodFor: "Usage-billed accounts and OpenAI-compatible servers.",
	},
	neopi: {
		title: "Share from a NeoPi",
		summary: "Borrow the accounts already signed in to a running NeoPi.",
		detail: "Nothing is copied. NeoPi keeps the credentials and refreshes them; NeoProxy sends each request through NeoPi's gateway. If NeoPi is offline, these accounts pause.",
		goodFor: "A NeoPi you already use every day, on this machine or the network.",
	},
	imported: {
		title: "Import from NeoPi",
		summary: "Copy credentials out of a NeoPi vault on this computer.",
		detail: "API keys copy over completely. Signed-in accounts are copied with refresh locked: NeoProxy never uses their refresh token, so NeoPi's own sign-in keeps working. They stop when the copied sign-in expires; re-import or sign in separately.",
		goodFor: "Moving NeoPi's API keys over in one step.",
	},
	file: {
		title: "Upload a credential file",
		summary: "Add an auth file saved by Codex CLI, Claude Code or another proxy.",
		detail: "NeoProxy reads the file, recognizes the provider and plan, and keeps it refreshed like a signed-in account.",
		goodFor: "Accounts already signed in on another machine.",
	},
};

const METHOD_LABEL = { subscription: "Sign in with an account", apiKey: "Paste an API key" };
const PICKER_SEARCH_FROM = 9;

// A provider's own details (NeoPi's login prompts: Snowflake's account,
// Cloudflare's account and gateway ids), asked before its flow starts.
const providerFields = (p, values, onChange) => (p.fields ?? []).map((f) =>
	field(f.label, input({ mono: true, placeholder: f.placeholder, autocomplete: "off", value: values[f.id] ?? "", onInput: (e) => { values[f.id] = e.target.value; onChange(); } }), f.hint));
const fieldsFilled = (p, values) => (p.fields ?? []).every((f) => (values[f.id] ?? "").trim());
const trimmedFields = (p, values) => p.fields?.length ? Object.fromEntries(p.fields.map((f) => [f.id, (values[f.id] ?? "").trim()])) : undefined;

// Sets the provider hue as real custom properties (style objects can't).
export function hueVars(el, provider) {
	el.style.setProperty("--p-h", String(provider?.hue ?? 0));
	el.style.setProperty("--p-c", String(provider?.chroma ?? 0.13));
	return el;
}

export function providerMark(provider, size = 28) {
	const el = h("span", { class: "pa-mark", "aria-hidden": "true", style: { width: `${size}px`, height: `${size}px`, fontSize: `${Math.round(size * 0.46)}px` } }, (provider?.name || "?").trim()[0]?.toUpperCase());
	return hueVars(el, provider);
}

const notice = (kind, title, ...body) => h("div", { class: `pa-notice ${kind}`, role: kind === "bad" ? "alert" : "status" }, title ? h("strong", null, title) : null, h("div", null, body));

async function busy(button, fn) {
	button.disabled = true;
	try { return await fn(); } finally { button.disabled = false; }
}

// openAddAccess({ session, catalog, people, initial: { provider?, method? } })
export function openAddAccess({ session, catalog = [], people = [], initial = {} }) {
	const admin = session.role === "admin";
	const providers = [...catalog].sort((a, b) => a.name.localeCompare(b.name));
	const state = { provider: providers.find((p) => p.id === initial.provider) ?? null, method: initial.method ?? null, done: null, query: "" };
	const owner = { id: session.userId };
	let leave = [];
	const onLeave = (fn) => leave.push(fn);
	const runLeave = () => { const fns = leave; leave = []; for (const fn of fns) fn(); };

	const host = h("div", { class: "pa" });
	const dlg = dialog({ title: "Add provider access", sub: " ", body: host, onClose: runLeave });
	const frame = host.closest(".dialog");
	frame.classList.add("pa-dialog");
	const titleEl = frame.querySelector("h2");
	const subEl = frame.querySelector(".sub");

	// If a method is requested without a provider, it is one of the three provider-free ways.
	if (state.provider && !state.method) state.method = state.provider.methods[0] ?? null;

	const go = (patch) => { runLeave(); Object.assign(state, patch); paint(); };
	const finish = (title, ...text) => go({ done: { title, text } });
	// Back to the picker starts a fresh search, focused, rather than the last one.
	const toChoose = () => { go({ provider: null, method: null, done: null, query: "" }); host.querySelector('input[type="search"]')?.focus(); };

	function paint() {
		host.replaceChildren();
		frame.classList.toggle("is-wide", !state.provider && !state.method && !state.done);
		let copy;
		if (state.done) copy = { title: state.done.title, sub: "" };
		else if (state.provider) copy = { title: `Add ${state.provider.name}`, sub: state.provider.vendor && state.provider.vendor !== state.provider.name ? `Access from ${state.provider.vendor}. Every way ends up in the same pool.` : "Every way ends up in the same pool." };
		else if (state.method) copy = { title: KIND_COPY[state.method].title, sub: KIND_COPY[state.method].detail };
		else copy = { title: "Add provider access", sub: "Pick the provider. Every way of adding access ends up in the same pool, and you can mix them freely." };
		titleEl.textContent = copy.title;
		subEl.textContent = copy.sub;
		subEl.hidden = !copy.sub;
		host.append(state.done ? doneView() : state.provider ? providerView() : state.method ? methodView() : chooseView());
	}

	// ---- step 1: choose ----
	function chooseView() {
		const grid = h("div", { class: "pa-grid", role: "list" });
		const renderGrid = () => {
			const q = state.query.trim().toLowerCase();
			// Name matches rank before vendor or id matches, so Enter on "Moonshot"
			// picks Moonshot rather than Kimi (made by Moonshot AI).
			const rank = (p) => { const n = p.name.toLowerCase(); return n === q ? 0 : n.startsWith(q) ? 1 : n.includes(q) ? 2 : 3; };
			const shown = q ? providers.filter((p) => [p.name, p.vendor, p.id].some((s) => s?.toLowerCase().includes(q))).sort((a, b) => rank(a) - rank(b)) : providers;
			replace(grid, ...shown.map((p) => tile(p)), shown.length ? null : h("p", { class: "pa-empty" }, `No provider matches “${state.query.trim()}”. NeoPi sharing and file upload below work for any provider.`));
		};
		const tile = (p) => {
			const hints = (p.methods ?? []).map((m) => (m === "subscription" ? "sign in" : "API key"));
			const el = h("button", { type: "button", class: "pa-tile", role: "listitem", onClick: () => go({ provider: p, method: p.methods[0] ?? null }) },
				providerMark(p, 30),
				h("span", { class: "pa-tile-text" }, h("span", { class: "pa-tile-name" }, p.name), h("span", { class: "pa-tile-sub" }, [p.vendor !== p.name ? p.vendor : null, hints.join(" · ")].filter(Boolean).join(" · "))));
			return hueVars(el, p);
		};
		const box = search(`Find among ${providers.length} providers`, (v) => { state.query = v; renderGrid(); }, {
			"aria-label": "Find a provider",
			value: state.query,
			onKeydown: (e) => { if (e.key === "Enter") grid.querySelector(".pa-tile")?.click(); },
		});
		renderGrid();
		const ways = ["neopi", ...(admin ? ["imported"] : []), "file"].filter((k) => k !== "neopi" || admin);
		return h("div", { class: "pa-choose" },
			providers.length >= PICKER_SEARCH_FROM ? box : null,
			grid,
			h("div", { class: "pa-ways" },
				h("div", { class: "pa-ways-title" }, "Not tied to one provider"),
				h("div", { class: "pa-ways-row" }, ways.map((k) => h("button", { type: "button", class: "pa-way", onClick: () => go({ method: k }) },
					h("span", { class: "pa-way-title" }, KIND_COPY[k].title),
					h("span", { class: "pa-way-sub" }, KIND_COPY[k].summary))))),
		);
	}

	const back = (label, onClick) => h("button", { type: "button", class: "pa-back", onClick }, icon("arrowLeft", 14), label);

	// ---- step 2 (provider): tabs per method ----
	function providerView() {
		const p = state.provider;
		const methods = (p.methods ?? []).filter((m) => METHOD_LABEL[m]);
		const body = h("div", { class: "pa-body" });
		const show = () => {
			runLeave();
			replace(body, state.method === "subscription" ? signInFlow(p) : state.method === "apiKey" ? apiKeyFlow(p) : notice("info", "No way to add this provider here", "Share it from a NeoPi or upload its credential file instead."));
		};
		show();
		return h("div", { class: "pa-stack" },
			back("All providers", toChoose),
			h("div", { class: "pa-provider" }, providerMark(p, 34), h("div", null, h("div", { class: "pa-provider-name" }, p.name), h("div", { class: "pa-provider-vendor" }, p.vendor && p.vendor !== p.name ? p.vendor : ""))),
			methods.length > 1 ? seg(methods.map((m) => ({ value: m, label: METHOD_LABEL[m] })), state.method, (m) => { state.method = m; show(); }) : null,
			methods.length === 1 ? h("p", { class: "pa-detail" }, KIND_COPY[methods[0]].detail) : h("p", { class: "pa-detail" }, "You can add the same provider both ways."),
			body,
		);
	}

	// ---- step 2 (provider-free ways) ----
	function methodView() {
		const flow = state.method === "neopi" ? neopiFlow() : state.method === "imported" ? importFlow() : fileFlow();
		return h("div", { class: "pa-stack" }, back("All ways to add access", toChoose), flow);
	}

	function doneView() {
		return h("div", { class: "pa-done" },
			h("span", { class: "pa-done-mark" }, icon("check", 22)),
			h("div", { class: "pa-done-text" }, state.done.text),
			h("div", { class: "pa-actions" },
				btn("Add more access", { icon: "plus", onClick: toChoose }),
				btn("Back to the pool", { cls: "primary", onClick: () => dlg.close() })));
	}

	function contributor() {
		if (!admin || people.length < 2) return null;
		const sel = h("select", { class: "select", onChange: (e) => { owner.id = e.target.value; } },
			people.map((u) => h("option", { value: u.id, selected: u.id === owner.id }, `${u.name || u.handle}${u.id === session.userId ? " (you)" : ""}`)));
		return field("Contributor", sel, "Whose access this is. It's listed under their name and they can see its usage.");
	}

	// ---- sign in with an account ----
	function signInFlow(p) {
		const sub = p.subscription ?? {};
		const site = sub.site ?? p.vendor ?? "the provider";
		const flow = sub.flow ?? "browser";
		const box = h("div", { class: "pa-stack" });
		let popup = null, timer = 0, dead = false;
		const stop = () => { dead = true; clearTimeout(timer); };
		onLeave(stop);

		// Some providers (Codex) also offer a device code for headless machines or
		// browsers on another computer; the catalog says so with modes/deviceFlow.
		const offersDevice = flow !== "device" && (sub.deviceFlow === true || (Array.isArray(sub.modes) && sub.modes.includes("device")));
		let mode = flow === "device" ? "device" : "browser";
		// Per-account providers (Snowflake) sign in at the account named here.
		const values = {};
		const idle = (error) => {
			const asDevice = mode === "device";
			const go = btn(asDevice ? "Get a code" : `Continue to ${site}`, { icon: "arrowRight", cls: "primary", onClick: (e) => start(e.currentTarget) });
			go.disabled = !fieldsFilled(p, values);
			replace(box, 
				offersDevice ? seg([{ value: "browser", label: "In this browser" }, { value: "device", label: "With a code" }], mode, (v) => { mode = v; idle(); }) : null,
				...providerFields(p, values, () => { go.disabled = !fieldsFilled(p, values); }),
				h("ol", { class: "pa-steps" },
					h("li", null, asDevice ? `A short code appears here; open ${site}'s device page on any machine and type it in.` : `${site}'s sign-in page opens in a new tab.`),
					h("li", null, "Sign in with the account you want to share and approve access."),
					h("li", null, flow === "code" ? `${site} can't send you back here, so copy the address its page hands off to (or the code in it) and paste it in this tab.` : "Come back to this tab. It notices the sign-in by itself and adds the account to the pool under your name.")),
				// Browser sign-ins all come back the same way; the catalog's notes for them
				// repeat step 1 or describe the plumbing, so say the one thing a person needs.
				asDevice ? null : flow === "browser" ? h("p", { class: "pa-detail" }, "If this page doesn't continue after you approve, copy the address your browser ends up on and paste it on the next screen.") : sub.note ? h("p", { class: "pa-detail" }, sub.note) : null,
				error ? notice("bad", "Sign-in didn't complete", error) : null,
				h("div", { class: "pa-actions" }, go),
			);
		};

		function start(button) {
			// Open the tab inside the click so browsers don't block it (not for a
			// device code, which is typed on another machine).
			popup = mode === "device" ? null : window.open("about:blank", "_blank");
			button.disabled = true;
			button.lastChild.textContent = "Starting…";
			const query = mode === "device" ? { mode: "device" } : trimmedFields(p, values);
			neo.startLogin(p.id, query).then((r) => {
				if (dead) { popup?.close(); return; }
				if (r.url && popup) popup.location.href = r.url;
				else popup?.close();
				waiting(r);
			}).catch((e) => { popup?.close(); if (!dead) idle(e.message); });
		}

		function waiting(r) {
			const device = r.flow === "device" || (r.status === "ok" && !!r.user_code);
			// Most device-code starts (Kimi, xAI, Meta, Nous) send the provider's device page as `url`.
			const verify = r.verification_uri_complete || r.verification_uri || r.verification_url || (device ? r.url : null) || null;
			let code = "";
			const note = h("div");
			const submit = async (e) => {
				const raw = code.trim();
				let value = raw;
				try { value = new URL(raw).searchParams.get("code") ?? raw; } catch { /* not a URL: the code itself */ }
				await busy(e.currentTarget, async () => {
					try { await neo.loginCallback({ provider: p.id, state: r.state, code: value }); replace(note, notice("info", "", "Code sent. Waiting for the provider to confirm.")); }
					catch (err) { replace(note, notice("bad", "", err.message)); }
				});
			};
			const pasteRow = (placeholder, primary) => h("div", { class: "pa-inline" }, input({ mono: true, placeholder, autocomplete: "off", onInput: (e) => { code = e.target.value; } }), btn("Finish sign-in", { cls: primary ? "primary" : "", onClick: submit }));
			replace(box, 
				h("div", { class: "pa-waiting" }, h("span", { class: "pa-pulse" }), h("div", null, h("strong", null, `Waiting for ${site} sign-in`), h("p", { class: "pa-detail" }, device ? "Approve the code on the provider's page; this updates on its own." : flow === "code" ? "Finish on the tab that just opened, then paste the address it ends on below." : "Finish on the tab that just opened. This updates on its own."))),
				r.user_code ? h("div", { class: "pa-code" }, h("span", { class: "pa-detail" }, verify ? ["Open ", h("a", { href: verify, target: "_blank", rel: "noopener noreferrer" }, verify.replace(/^https?:\/\//, "")), " on any machine and enter this code"] : `Confirm this code on ${site}'s page`), h("span", { class: "pa-code-value mono" }, r.user_code), btn("Copy", { cls: "sm", onClick: async () => { if (await copyText(r.user_code)) toast("Code copied"); else toast("Could not copy", "bad"); } })) : null,
				r.url && !device ? h("p", { class: "pa-detail" }, "No tab opened? ", h("a", { href: r.url, target: "_blank", rel: "noopener noreferrer" }, "Open the sign-in page", icon("external", 12))) : null,
				flow === "code"
					? h("div", { class: "pa-stack" }, h("p", { class: "pa-detail" }, "After you approve, ", site, " tries to open its own app at an address starting with ", h("code", null, "zcode://"), ". Copy that address from the prompt or the address bar. If the browser hides it, open the developer console (F12): it is in the “Failed to launch” message."), pasteRow("zcode://zai-auth/callback?code=…", true))
					: device ? null : h("div", { class: "pa-stack pa-fallback" }, h("p", { class: "pa-detail" }, "If this page doesn't continue after you approve, copy the address your browser ends up on and paste it here."), pasteRow("The address your browser ends up on, or just the code", false)),
				note,
				h("div", { class: "pa-actions" }, btn("Start over", { onClick: () => { stop(); dead = false; idle(); } })),
			);
			const tick = async () => {
				try {
					const s = await neo.loginStatus(r.state);
					if (dead) return;
					if (s.status === "ok") {
						finish("Signed in", h("p", null, h("strong", null, p.name), " is now part of the pool, listed under your name. NeoProxy refreshes the sign-in automatically."), h("p", { class: "pa-detail" }, "Its plan is detected from the account. You can reassign it from the pool."));
						return;
					}
					if (s.status === "error") { idle(s.error || "The provider reported an error."); return; }
				} catch (e) {
					if (!dead && e instanceof ApiError && e.status !== 404) { idle(e.message); return; }
				}
				if (!dead) timer = setTimeout(tick, 1500);
			};
			timer = setTimeout(tick, 1200);
		}
		idle();
		return box;
	}

	// ---- paste an API key ----
	function apiKeyFlow(p) {
		const cfg = p.apiKey ?? {};
		let key = "", baseUrl = "", label = "", monthly = "";
		const values = {};
		// Regional providers (NeoPi's endpoint prompt): each key works at one
		// region; administrators may also name a custom endpoint.
		const regions = cfg.regions ?? [];
		const CUSTOM = "custom";
		let region = regions[0]?.baseUrl ?? "";
		const customField = field("Custom endpoint", input({ mono: true, placeholder: "https://your-proxy.com/v1", onInput: (e) => { baseUrl = e.target.value; sync(); } }), "The base URL to send this key's requests to, usually ending in /v1.");
		customField.style.display = "none";
		const regionOptions = [...regions.map((r) => ({ value: r.baseUrl, label: `${r.label} (${r.baseUrl})` })), ...(admin && cfg.customRegion ? [{ value: CUSTOM, label: "Custom endpoint" }] : [])];
		const regionField = regions.length ? field("Region", select(regionOptions, region, (v) => { region = v; customField.style.display = v === CUSTOM ? "" : "none"; sync(); }), "Keys work only in the region they were created in.") : null;
		const chosenBaseUrl = () => cfg.editableBaseUrl ? baseUrl.trim() || undefined : region === CUSTOM ? baseUrl.trim() : region || undefined;
		const errorHost = h("div");
		const keyInput = input({ mono: true, type: "password", autocomplete: "off", placeholder: cfg.keyHint, onInput: (e) => { key = e.target.value; sync(); } });
		const reveal = btn("Show", { onClick: () => { const show = keyInput.type === "password"; keyInput.type = show ? "text" : "password"; reveal.lastChild.textContent = show ? "Hide" : "Show"; } });
		const submit = btn("Check and add key", { cls: "primary", onClick: async () => {
			errorHost.replaceChildren();
			await busy(submit, async () => {
				try {
					await neo.addApiKey({ provider: p.id, apiKey: key.trim(), fields: trimmedFields(p, values), baseUrl: chosenBaseUrl(), customBaseUrl: region === CUSTOM || undefined, label: label.trim() || undefined, ownerId: owner.id, monthlyUSD: monthly ? Number(monthly) : undefined });
					finish("API key added", h("p", null, h("strong", null, p.name), " checked out and is now in the pool. Its models are available to virtual models and grants right away."));
				} catch (e) { replace(errorHost, notice("bad", "The key wasn't added", e.message)); }
			});
		} });
		const sync = () => { submit.disabled = (!key.trim() && !(cfg.keyOptional ?? cfg.editableBaseUrl)) || (region === CUSTOM && !baseUrl.trim()) || !fieldsFilled(p, values); };
		sync();
		return h("form", { class: "pa-stack", onSubmit: (e) => { e.preventDefault(); if (!submit.disabled) submit.click(); } },
			cfg.editableBaseUrl ? field("Server address", input({ mono: true, placeholder: p.id === "custom" ? "https://example.com/v1" : cfg.baseUrl, onInput: (e) => { baseUrl = e.target.value; } }), p.id === "custom" ? "The OpenAI-compatible base URL, usually ending in /v1. For a local Ollama: http://127.0.0.1:11434/v1" : `The gateway's base URL. Leave empty for the default, ${cfg.baseUrl}`) : null,
			...providerFields(p, values, sync),
			regionField,
			regionField ? customField : null,
			field("API key", h("div", { class: "pa-inline" }, keyInput, reveal), h("span", null, "Stored on this server only and never shown again. ", cfg.getKeyUrl ? h("a", { href: cfg.getKeyUrl, target: "_blank", rel: "noopener noreferrer" }, `Get a key from ${p.vendor}`) : null)),
			h("div", { class: "pa-two" },
				field("Name (optional)", input({ placeholder: "Team research", onInput: (e) => { label = e.target.value; } }), "Helps tell keys apart."),
				field("Monthly cost (optional)", input({ type: "number", min: 0, placeholder: "20", onInput: (e) => { monthly = e.target.value; } }), "US dollars. Used for the pool's monthly cost summary.")),
			contributor(),
			errorHost,
			h("div", { class: "pa-actions" }, h("button", { type: "submit", hidden: true }), submit),
		);
	}

	// ---- share from a NeoPi ----
	function neopiFlow() {
		let link = "", manual = false;
		const box = h("div", { class: "pa-stack" });
		const errorHost = h("div");
		let local = { loading: true };
		const connected = (name, count) => finish("NeoPi connected",
			h("p", null, h("strong", null, name), " is connected. ", count !== undefined ? `${fmtInt(count)} signed-in ${count === 1 ? "account" : "accounts"} will appear in the pool.` : "Its signed-in accounts appear in the pool within about 15 seconds."),
			h("p", { class: "pa-detail" }, "Accounts stay in NeoPi. If that NeoPi goes offline, they pause until it's back. Give each one a local meter if you want virtual models to ration it."));
		const result = (r, fallbackName) => {
			const src = r?.source ?? r ?? {};
			if (src.status && !["connected", "disabled"].includes(src.status)) { replace(errorHost, notice("warn", "Saved, but NeoProxy couldn't reach it yet", `Status: ${src.status}. Check that its gateway is running and reachable from this server; the connection retries on its own.`)); return; }
			connected(src.label || fallbackName, src.accountCount);
		};
		const looksRight = () => /^neopi-gateway:\/\//i.test(link.trim());
		const nameFromLink = () => { try { return new URL(link.trim().replace(/^neopi-gateway:/i, "http:")).searchParams.get("name") ?? ""; } catch { return ""; } };

		const draw = () => {
			const d = local;
			const localCard = d.loading ? h("p", { class: "pa-detail" }, "Looking for a NeoPi on this computer…")
				: d.found ? h("div", { class: `pa-local ${d.running && !d.alreadyConnected ? "ready" : ""}` },
					h("span", { class: "pa-local-mark" }, icon("share", 16)),
					h("div", { class: "pa-local-text" },
						h("strong", null, d.alreadyConnected ? "The NeoPi on this computer is already connected" : d.running ? `NeoPi on this computer${d.name ? `: ${d.name}` : ""}` : "NeoPi is installed here, but its gateway isn't running"),
						h("p", { class: "pa-detail" }, d.alreadyConnected ? "Its accounts are already in the pool." : d.running ? `${d.accountCount != null ? `${fmtInt(d.accountCount)} signed-in ${d.accountCount === 1 ? "account" : "accounts"} ready to share. ` : ""}NeoProxy reads the gateway's token itself; you don't need to copy anything.` : d.reason || "Start the gateway, then check again."),
						!d.running && d.startCommand ? copyLine(d.startCommand, "start command") : null),
					d.running && !d.alreadyConnected ? btn("Connect", { icon: "share", cls: "primary", onClick: (e) => busy(e.currentTarget, async () => {
						errorHost.replaceChildren();
						try { const r = await neo.connectLocalNeoPi({ ownerId: owner.id }); result(r, d.name || "NeoPi on this computer"); } catch (err) { replace(errorHost, notice("bad", "Couldn't connect the local NeoPi", err.message)); }
					}) }) : !d.running ? btn("Check again", { icon: "refresh", onClick: detect }) : null)
				: null;
			const linkInput = input({ mono: true, type: "password", autocomplete: "off", placeholder: "neopi-gateway://192.168.1.20:4000?token=…", value: link, onInput: (e) => { link = e.target.value; refreshLink(); } });
			const linkError = h("div", { class: "error" });
			const linkButton = btn("Connect with link", { icon: "share", cls: "primary", disabled: !looksRight(), onClick: (e) => busy(e.currentTarget, async () => {
				errorHost.replaceChildren();
				try { const r = await neo.saveSource({ connectionLink: link.trim(), ownerId: owner.id }); result(r, nameFromLink() || "NeoPi"); } catch (err) { replace(errorHost, notice("bad", "Couldn't connect to that NeoPi", err.message)); }
			}) });
			const refreshLink = () => { linkButton.disabled = !looksRight(); linkError.textContent = link.trim() && !looksRight() ? "That doesn't look like a connection link. It starts with neopi-gateway://" : ""; };
			replace(box, 
				localCard,
				h("div", { class: "pa-stack" },
					h("div", { class: "pa-label" }, d.found ? "NeoPi on another computer" : "Connect a NeoPi"),
					h("ol", { class: "pa-steps" },
						h("li", null, "On the computer running NeoPi, start its gateway so other machines can reach it:", copyLine("npi auth-gateway serve --bind=0.0.0.0:4000", "command")),
						h("li", null, "Print a connection link and paste it below:", copyLine("npi auth-gateway link", "command"))),
					field("Connection link", linkInput, "It contains the gateway token, so treat it like a password. NeoProxy keeps it on the server and never shows it again."),
					linkError),
				contributor(),
				errorHost,
				h("div", { class: "pa-actions" },
					h("button", { type: "button", class: "pa-linkbtn", onClick: () => { manual = !manual; draw(); } }, manual ? "Hide address and token" : "Enter an address and token instead"),
					h("span", { class: "spacer" }), linkButton),
				manual ? manualForm() : null,
			);
		};
		const detect = async () => {
			local = { loading: true }; draw();
			try { local = await neo.localNeoPi(); } catch { local = {}; }
			if (state.method === "neopi") draw();
		};
		function manualForm() {
			let url = "", token = "", name = "";
			const err = h("div");
			const go2 = btn("Connect with address and token", { onClick: (e) => busy(e.currentTarget, async () => {
				err.replaceChildren();
				let derived = name.trim();
				if (!derived) try { derived = `NeoPi on ${new URL(url).hostname}`; } catch { derived = "NeoPi"; }
				try { const r = await neo.saveSource({ label: derived, url: url.trim(), bearer: token.trim(), ownerId: owner.id }); result(r, derived); } catch (e2) { replace(err, notice("bad", "Couldn't connect to that NeoPi", e2.message)); }
			}) });
			const sync = () => { go2.disabled = !url.trim() || !token.trim(); };
			sync();
			return h("div", { class: "pa-stack pa-manual" },
				h("p", { class: "pa-detail" }, "For NeoPi versions without the link command: the address is where this server reaches the gateway, and ", h("code", null, "npi auth-gateway token"), " prints the token."),
				field("Gateway address", input({ mono: true, placeholder: "http://192.168.1.20:4000", onInput: (e) => { url = e.target.value; sync(); } })),
				field("Gateway token", input({ mono: true, type: "password", autocomplete: "off", onInput: (e) => { token = e.target.value; sync(); } }), "Kept on this server and never shown again. It is not a provider password."),
				field("Name (optional)", input({ onInput: (e) => { name = e.target.value; } }), "Shown next to its accounts in the pool."),
				err, h("div", { class: "pa-actions" }, go2));
		}
		detect();
		return box;
	}

	// ---- import from a NeoPi profile ----
	function importFlow() {
		let profile = "";
		const picked = new Set();
		const box = h("div", { class: "pa-stack" });
		const results = h("div", { class: "pa-stack" });
		const scan = btn("Find credentials", { icon: "search", onClick: () => find() });
		const providerOf = (c) => providers.find((p) => p.id === (c.neoproxyProvider ?? c.provider));
		const expiresIn = (ms) => { const hrs = Math.round((ms - Date.now()) / 3_600_000); return hrs < 1 ? "expires within the hour" : hrs < 48 ? `expires in ${hrs} h` : `expires in ${Math.round(hrs / 24)} days`; };

		async function find() {
			replace(results, h("p", { class: "pa-detail" }, "Reading the profile…"));
			picked.clear();
			await busy(scan, async () => {
				try {
					const r = await neo.importCandidates(profile.trim() || undefined);
					draw(r.credentials ?? []);
				} catch (e) {
					if (e instanceof ApiError && e.status === 404 && typeof e.body === "string") replace(results, notice("info", "This server can't import yet", "Importing arrives with the next NeoProxy update. Until then, share the same accounts with “Share from a NeoPi”, which copies nothing."));
					else replace(results, notice("bad", "Couldn't read that profile", e.message));
				}
			});
		}
		function draw(list) {
			if (!list.length) { replace(results, notice("info", "No credentials found", "This NeoPi profile has no stored credentials.")); return; }
			const locked = () => list.filter((c) => picked.has(c.credentialId) && c.kind === "subscription").length;
			const warn = h("div"); const importBtn = btn("Import", { icon: "download", cls: "primary", disabled: true, onClick: () => doImport() });
			const sync = () => {
				importBtn.lastChild.textContent = picked.size ? `Import ${picked.size} ${picked.size === 1 ? "credential" : "credentials"}` : "Import";
				importBtn.disabled = picked.size === 0;
				const n = locked();
				replace(warn, n ? notice("warn", `${n} signed-in ${n === 1 ? "account" : "accounts"} will be imported with refresh locked`, "NeoProxy uses the copied sign-in but never refreshes it, so NeoPi's own sign-in keeps working. When the copy expires, re-import it or sign in separately.") : null);
			};
			const errHost = h("div");
			async function doImport() {
				errHost.replaceChildren();
				await busy(importBtn, async () => {
					try {
						const r = await neo.importFromNeoPi({ profile: profile.trim() || undefined, credentialIds: [...picked], ownerId: owner.id });
						const count = r.items?.length ?? picked.size;
						finish("Imported from NeoPi",
							h("p", null, `${fmtInt(count)} ${count === 1 ? "credential is" : "credentials are"} now in the pool.`),
							r.failed?.length ? notice("warn", `${r.failed.length} couldn't be imported`, h("ul", { class: "pa-failed" }, r.failed.map((f) => h("li", null, `${list.find((c) => c.credentialId === f.credentialId)?.label ?? f.credentialId}: ${f.error}`)))) : null,
							locked() ? h("p", { class: "pa-detail" }, `${locked()} signed-in ${locked() === 1 ? "account was" : "accounts were"} imported with refresh locked. Re-import them after NeoPi refreshes, or sign in separately to give NeoProxy its own.`) : null);
					} catch (e) { replace(errHost, notice("bad", "Import failed", e.message)); }
				});
			}
			replace(results, 
				h("ul", { class: "pa-picks", "aria-label": "Credentials in this NeoPi profile" }, list.map((c) => {
					const p = providerOf(c);
					const off = !c.importable || !!c.alreadyImported;
					const box2 = h("input", { type: "checkbox", disabled: off, onChange: (e) => { e.target.checked ? picked.add(c.credentialId) : picked.delete(c.credentialId); sync(); } });
					return h("li", null, h("label", { class: `pa-pick ${off ? "off" : ""}` }, box2, providerMark(p ?? { name: c.provider }, 24),
						h("span", { class: "pa-pick-text" }, h("strong", null, p?.name ?? c.provider), h("span", { class: "mono pa-pick-label" }, c.label)),
						h("span", { class: "pa-pick-kind" }, c.alreadyImported ? "Already in the pool" : !c.importable ? c.reason ?? "Not supported" : c.kind === "subscription" ? `Refresh locked${c.expiresAt ? `, ${expiresIn(c.expiresAt)}` : ""}` : "API key, copies fully")));
				})),
				warn, contributor(), errHost, h("div", { class: "pa-actions" }, importBtn));
			sync();
		}
		box.append(
			field("NeoPi profile (optional)", h("div", { class: "pa-inline" }, input({ mono: true, placeholder: "~/.omp/agent", onInput: (e) => { profile = e.target.value; }, onKeydown: (e) => { if (e.key === "Enter") { e.preventDefault(); find(); } } }), scan), "The NeoPi profile folder on the computer running NeoProxy. Leave empty to use the default, ~/.omp/agent."),
			results);
		return box;
	}

	// ---- upload a credential file ----
	function fileFlow() {
		let file = null;
		const guessFile = (json) => {
			if (!json || typeof json !== "object") return undefined;
			if (typeof json.type === "string") return json.type;
			if (json.tokens && typeof json.tokens === "object" && "id_token" in json.tokens) return "codex";
			if (json.claudeAiOauth) return "claude";
			if (typeof json.OPENAI_API_KEY === "string" || json.auth_mode) return "codex";
			return undefined;
		};
		const zone = h("label", { class: "pa-drop" });
		const fileInput = h("input", { type: "file", accept: ".json,application/json", class: "pa-sr", onChange: (e) => pick(e.target.files?.[0]) });
		const errHost = h("div");
		const submit = btn("Add to pool", { icon: "upload", cls: "primary", disabled: true, onClick: () => busy(submit, async () => {
			errHost.replaceChildren();
			try {
				const form = new FormData();
				form.append("file", file, file.name);
				form.append("ownerId", owner.id);
				await neo.uploadAccessFile(form);
				finish("Credential added", h("p", null, "The account in ", h("code", null, file.name), " is now in the pool. NeoProxy refreshes it automatically."));
			} catch (e) { replace(errHost, notice("bad", "The file wasn't added", e.message)); }
		}) });
		const drawZone = (guess) => replace(zone, icon("upload", 20), file
			? h("span", null, h("strong", null, file.name), h("span", { class: "pa-detail" }, guess ? ` looks like a ${providers.find((p) => p.id === guess)?.name ?? guess} credential.` : " ready to upload."))
			: h("span", null, h("strong", null, "Drop a credential file here"), h("span", { class: "pa-detail" }, " or click to choose one")), fileInput);
		async function pick(f) {
			file = f ?? null; errHost.replaceChildren(); submit.disabled = true; drawZone();
			if (!f) return;
			if (f.size > 256 * 1024) { replace(errHost, notice("bad", "", "That file is larger than any credential file. Check that you picked the right one.")); return; }
			try { drawZone(guessFile(JSON.parse(await f.text()))); submit.disabled = false; }
			catch { replace(errHost, notice("bad", "", "This isn't a JSON file. Credential files are JSON, like ~/.codex/auth.json.")); }
		}
		zone.addEventListener("dragover", (e) => { e.preventDefault(); zone.classList.add("over"); });
		zone.addEventListener("dragleave", () => zone.classList.remove("over"));
		zone.addEventListener("drop", (e) => { e.preventDefault(); zone.classList.remove("over"); pick(e.dataTransfer.files[0]); });
		drawZone();
		return h("div", { class: "pa-stack" }, zone,
			h("div", { class: "pa-accepted" }, h("div", { class: "pa-label" }, "Files this accepts"),
				h("ul", null,
					h("li", null, h("strong", null, "Codex CLI "), h("code", null, "~/.codex/auth.json"), " after ", h("code", null, "codex login")),
					h("li", null, h("strong", null, "Claude Code "), h("code", null, "~/.claude/.credentials.json")),
					h("li", null, h("strong", null, "CLIProxyAPI or another NeoProxy "), "any file from its ", h("code", null, "auths"), " folder")),
				h("p", { class: "pa-detail" }, "The file holds a sign-in. Treat it like a password and delete local copies you no longer need.")),
			contributor(), errHost, h("div", { class: "pa-actions" }, submit));
	}

	paint();
	return dlg;
}

