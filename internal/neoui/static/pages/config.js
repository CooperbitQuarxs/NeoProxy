// Configuration: the engine's settings as an editable form first, the raw
// file second. Every field saves on its own through the v8 management API
// (`PUT /v8/management/config/<key path>`), guarded by the ETag of the file
// version the page last read, so two editors never silently overwrite each other.

import { h, icon, fmtInt, plural, replace } from "../lib/dom.js";
import { engine, invalidate, ApiError } from "../lib/client.js";
import { pageHead, card, btn, iconBtn, spinner, errorState, confirm } from "../lib/components.js";
import { codeEditor, findKeyLine } from "../lib/code-editor.js";

// ---------- what the sections and fields mean ----------

const SECTIONS = [
	["server", "Server", "Where NeoProxy listens, HTTPS, and whether it announces itself on your local network."],
	["access", "Access", "The keys your apps and teammates use to call NeoProxy."],
	["api-keys", "Provider API keys", "Provider endpoints and keys NeoProxy calls for you, with the models each one serves."],
	["routing", "Routing", "How NeoProxy picks an account for direct provider models, and when it retries or rests an account. Requests for virtual models follow the virtual model's own stages instead."],
	["requests", "Outgoing requests", "Proxy, timeouts, streaming keep-alives and payload rules for requests sent to providers."],
	["credentials", "Credential handling", "How long NeoProxy holds, retries and releases an account while requests are running. The defaults suit most setups."],
	["oauth", "Signed-in accounts", "Accounts added with a provider sign-in: where their tokens are kept, model aliases and per-provider options."],
	["upstream", "Provider behavior", "Provider-wide request behavior for Claude, Codex and xAI."],
	["client", "Client compatibility", "Switches that help specific client apps work through NeoProxy."],
	["multimedia", "Images and video", "Image and video generation."],
	["observability", "Logs and usage", "What NeoProxy logs, for how long, and whether it counts usage."],
	["management", "Management", "The management API and the bundled control panel: its key, and whether the panel updates itself."],
	["plugins", "Plugins", "Plugin loading, plugin stores and settings for each plugin."],
	["models", "Model catalogs", "Where NeoProxy gets the model lists it shows to clients."],
	["quota-exceeded", "When a quota runs out", "What NeoProxy does when a provider says a quota is used up."],
];
const SECTION = new Map(SECTIONS.map(([key, title, intro]) => [key, { title, intro }]));

// Hints, keyed by the dotted key path (array positions dropped) or by `*.leaf`.
// Written from the comments on the Go config structs.
const HINTS = {
	"server.host": "Leave empty to listen on every network interface; 127.0.0.1 keeps NeoProxy private to this machine.",
	"server.port": "The port apps connect to.",
	"server.trusted-proxies": "Addresses allowed to pass on the caller's real IP in forwarded headers. Takes effect after a restart.",
	"server.github-token": "Used for GitHub requests, ahead of the GITHUB_TOKEN environment variable.",
	"server.commercial-mode": "Turns off heavier request logging to keep memory use per request low.",
	"server.discovery.enabled": "Announce NeoProxy on the local network so nearby apps can find it.",
	"server.discovery.service-type": "The service type it announces. Default _ai-gateway._tcp.",
	"server.discovery.subtypes": "The API styles it says it speaks, such as _responses or _messages.",
	"server.discovery.service-name": "The name it announces. Empty uses an automatic name.",
	"server.discovery.auth-required": "Tell clients they need a key.",
	"server.tls.enable": "Serve HTTPS instead of HTTP.",
	"server.tls.cert": "Path to the certificate file.",
	"server.tls.key": "Path to the private key file.",
	"access.api-keys": "Anyone holding one of these keys can call NeoProxy.",
	"management.secret-key": "The key for the management API, stored as plain text or a bcrypt hash.",
	"management.allow-remote": "Allow the management API from other machines, not only this one.",
	"management.disable-control-panel": "Stop serving the bundled control panel.",
	"management.disable-auto-update-panel": "Stop checking GitHub for control panel updates in the background.",
	"management.panel-github-repository": "Where control panel updates come from.",
	"routing.strategy": "Applies only to direct provider models (neoproxy/<provider>/<model>). Fill-first uses one account until it runs out; round-robin takes turns; weighted follows each key's weight.",
	"routing.session-affinity": "Keep a conversation on the same account so provider-side caching keeps working.",
	"routing.session-affinity-ttl": "How long a conversation stays tied to its account.",
	"routing.cooldown.disable-cooling": "Never rest an account or model after errors, unless a provider or key overrides it.",
	"routing.cooldown.save-cooldown-status": "Remember rested accounts across restarts.",
	"routing.cooldown.transient-error-cooldown-seconds": "Rest after temporary provider errors (5xx, timeouts). 0 keeps the built-in default; negative turns it off.",
	"routing.retry.request-retry": "Extra rounds to try after every eligible account has failed once.",
	"routing.retry.max-retry-credentials": "Accounts to try in each round. 0 tries them all.",
	"routing.retry.max-retry-interval": "The longest wait, in seconds, for a resting account before another round.",
	"oauth.auth-dir": "Folder where signed-in account tokens are saved.",
	"oauth.auth-auto-refresh-workers": "How many accounts refresh their tokens at once. 0 uses the default.",
	"oauth.providers.aistudio.ws-auth": "Require a key on the AI Studio WebSocket API.",
	"observability.logs.debug": "Log extra detail for troubleshooting.",
	"observability.logs.logging-to-file": "Write logs to rotating files instead of the console.",
	"observability.logs.logs-max-total-size-mb": "Delete the oldest log files beyond this total size. 0 keeps everything.",
	"observability.logs.request-log": "Keep a detailed log of every request.",
	"observability.logs.error-logs-max-files": "Error log files to keep when request logging is off. Default 10; 0 keeps all.",
	"observability.usage.usage-statistics-enabled": "Count usage in memory; when off, usage is discarded.",
	"observability.usage.redis-usage-queue-retention-seconds": "How long usage events wait for the management API to read them. Default 60, at most 3600.",
	"plugins.enabled": "Load plugins at all.",
	"plugins.dir": "Folder NeoProxy looks in for plugins.",
	"plugins.store-sources": "Extra plugin stores, on top of the official one.",
	"credentials.in-flight.snapshot-interval": "How often running requests are recorded.",
	"credentials.in-flight.stale-after": "When a request that stopped reporting counts as gone.",
	"*.api-key": "Kept secret; use Change… to replace it.",
	"*.base-url": "The provider's API address.",
	"*.proxy-url": "Send this key's requests through a proxy, instead of the global one.",
	"*.prefix": "Put this name in front of the models it serves, for example team/kimi-k2.",
	"*.priority": "Higher is preferred when several match.",
	"*.weight": "Share of requests under weighted routing. Default 1; 0 or less leaves it out.",
	"*.disabled": "Keep it configured, but never route to it.",
	"*.excluded-models": "Models this entry never serves.",
	"*.disable-cooling": "Override the global rule for resting this after errors.",
	"*.request-retry": "Override the global retry rounds. Negative uses the global value; 0 means no extra rounds.",
	"*.request-scoped-errors": "Rules for provider errors that should only fail the one request (stop) or keep going (continue), instead of resting the account.",
	"*.sensitive-words": "Words disguised with invisible characters before they are sent.",
};

const LABELS = {
	"secret-key": "Management key",
	"openai-compatibility": "OpenAI-compatible providers",
	"auth-dir": "Token folder",
	"ws-auth": "Require a key for WebSocket",
	"api-key": "API key",
	"base-url": "Base URL",
	"request-scoped-errors": "Request-only errors",
	"in-flight": "Running requests",
};

const ENUMS = {
	"routing.strategy": ["round-robin", "weighted-round-robin", "fill-first"],
	"*.action": ["stop", "stop-and-cooldown", "continue", "continue-and-cooldown"],
	"*.client-request-id": ["on", "off", "passthrough"],
	"*.dialect": ["chat-completions", "responses", "messages", "generate-content"],
};

// Values NeoProxy owns; the API rejects changes to them.
const READ_ONLY = new Set(["config-version", "credentials.concurrency.lifecycle-config-revision", "credentials.concurrency.observation-barrier-revision", "plugins.auth-revision"]);
const READ_ONLY_LEAF = new Set(["auth_index"]);

const SECRET = /secret|api-?keys?$|bearer|token|password|passphrase|credential$|private-key/i;
const DURATION = /^(?:0|(?:\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h))+)$/;
const WORDS = { url: "URL", urls: "URLs", api: "API", tls: "TLS", ttl: "TTL", id: "ID", ids: "IDs", ip: "IP", ips: "IPs", mb: "MB", http: "HTTP", https: "HTTPS", oauth: "OAuth", dns: "DNS", cpa: "CPA", ws: "WebSocket", ui: "UI", json: "JSON", udp: "UDP", ice: "ICE", xai: "xAI", github: "GitHub", openai: "OpenAI", aistudio: "AI Studio", codex: "Codex", claude: "Claude", gemini: "Gemini", vertex: "Vertex", antigravity: "Antigravity", devin: "Devin", gpt: "GPT", redis: "Redis", pprof: "pprof" };
const ITEMS_SHOWN = 20;
const CHIPS_SHOWN = 30;

const plainPath = (path) => path.filter((s) => typeof s !== "number").join(".");
const pathId = (path) => path.join("\u0001");
const lookup = (table, path) => table[plainPath(path)] ?? table[`*.${path.filter((s) => typeof s !== "number").at(-1)}`];

function human(key) {
	if (LABELS[key]) return LABELS[key];
	const words = String(key).split(/[-_ ]+/).filter(Boolean);
	return words.map((w, i) => WORDS[w.toLowerCase()] ?? (i === 0 ? w[0].toUpperCase() + w.slice(1) : w)).join(" ") || String(key);
}

// The raw key, shown only when the label doesn't already spell it out.
const keyCode = (key) => (typeof key === "string" && human(key).toLowerCase() !== key.replace(/[-_]/g, " ").toLowerCase() ? h("code", { class: "set-key" }, key) : null);

const isScalar = (v) => v === null || ["string", "number", "boolean"].includes(typeof v);
const isSecretKey = (path) => {
	const leaf = path.filter((s) => typeof s !== "number").at(-1);
	return typeof leaf === "string" && SECRET.test(leaf);
};
const isReadOnly = (path) => READ_ONLY.has(plainPath(path)) || READ_ONLY_LEAF.has(path.at(-1));

function getIn(obj, path) {
	let v = obj;
	for (const s of path) v = v?.[s];
	return v;
}
function setIn(obj, path, value) {
	let v = obj;
	for (const s of path.slice(0, -1)) v = v[s];
	v[path.at(-1)] = value;
}

// Order a mapping for reading: identity first, then plain values, then lists
// and groups. The API returns keys alphabetically; this keeps that within each tier.
const FIRST = ["name", "alias", "id", "enabled", "enable", "base-url", "host", "port"];
function orderedEntries(obj) {
	const tier = ([k, v]) => {
		const f = FIRST.indexOf(k);
		if (f !== -1) return f - 100;
		if (isScalar(v)) return 0;
		if (Array.isArray(v) && v.every(isScalar)) return 1;
		if (Array.isArray(v)) return 3;
		return 2;
	};
	return Object.entries(obj).sort((a, b) => tier(a) - tier(b));
}

function errorText(e) {
	const body = e instanceof ApiError && e.body && typeof e.body === "object" ? e.body : null;
	const code = typeof body?.error === "string" ? body.error : "";
	if (code === "read_only_field") return "NeoProxy manages this value itself; it can't be changed here.";
	if (code === "precondition_failed") return "The config file changed after this page loaded it.";
	if (body?.message) return body.message;
	const meaning = {
		invalid_body: "The engine could not parse this. Look for bad indentation, an unclosed bracket or quote, or a stray colon.",
		invalid_json: "The engine could not read this value.",
		config_must_be_object: "The top level must be a mapping of settings, not a list or a single value.",
		invalid_path: "This key sits inside a list or value that can't hold it.",
		write_failed: "The engine could not write the config file. Check that it is writable.",
	}[code];
	return meaning ? `${meaning} (${code})` : e?.message || "Could not save";
}

function maskSecret(value) {
	if (!value) return "";
	const tail = value.length >= 16 ? value.slice(-4) : "";
	return `${"•".repeat(10)}${tail}`;
}

// ---------- the page ----------

export async function render(ctx) {
	const { root } = ctx;
	let data = null;
	let etag = null;
	let alive = true;
	let chain = Promise.resolve();
	const open = new Set(); // ids of expanded entries and lists
	const queries = new Map(); // id -> search text for long lists
	let fields = new Map(); // id -> field controller, rebuilt each render
	let uid = 0;

	const reloadButton = btn("Reload", { icon: "refresh", cls: "outline", onClick: () => reloadAll() });
	root.append(pageHead({
		title: "Configuration",
		desc: "Everything NeoProxy reads from its config file, grouped by what it controls. Each setting saves as soon as you change it.",
		actions: [reloadButton],
	}));
	const index = h("nav", { class: "set-index", "aria-label": "Sections" });
	const formHost = h("div", { class: "set-sections" });
	const fileHost = h("div");
	root.append(index, formHost, fileHost);
	formHost.append(spinner());

	async function fetchConfig() {
		const res = await engine.raw("GET", "config");
		etag = res.headers.get("ETag");
		data = await res.json();
		invalidate("config");
	}

	// ---------- saving ----------

	// Saves one value. Paths address YAML keys only, so a change inside a list
	// rewrites that whole list, as the API expects. Saves run one at a time so
	// each carries the version the previous one produced.
	function commit(path, value) {
		const run = async () => {
			const next = structuredClone(data);
			setIn(next, path, value);
			const cut = path.findIndex((s) => typeof s === "number");
			const keyPath = cut === -1 ? path : path.slice(0, cut);
			const headers = etag ? { "If-Match": etag } : {};
			const res = await engine.raw("PUT", `config/${keyPath.map(encodeURIComponent).join("/")}`, { json: getIn(next, keyPath), headers });
			data = next;
			etag = res.headers.get("ETag");
			if (!etag) await fetchConfig();
			invalidate("config");
			file.formSaved();
		};
		const p = chain.then(run);
		chain = p.catch(() => {});
		return p;
	}

	// A field controller: owns the inline "Saved" tick and the error line.
	function shell(path) {
		const state = h("span", { class: "set-state", "aria-live": "polite" });
		const err = h("div", { class: "set-err", role: "alert", hidden: true });
		let timer = 0;
		const ctl = {
			path,
			state,
			err,
			busy() { clearTimeout(timer); state.className = "set-state busy"; state.replaceChildren("Saving…"); },
			ok() {
				clearTimeout(timer);
				err.hidden = true;
				state.className = "set-state ok";
				state.replaceChildren(icon("check", 13), "Saved");
				timer = setTimeout(() => { state.className = "set-state"; state.replaceChildren(); }, 2400);
			},
			fail(message) {
				clearTimeout(timer);
				state.className = "set-state";
				state.replaceChildren();
				err.replaceChildren(icon("info", 13), h("span", null, message));
				err.hidden = false;
			},
			clear() { err.hidden = true; },
			async save(value) {
				ctl.busy();
				try {
					await commit(path, value);
					ctl.ok();
					return true;
				} catch (e) {
					if (e instanceof ApiError && e.status === 412) {
						// Someone else saved first: show their version, keep the explanation on this field.
						await refreshForm();
						const again = fields.get(pathId(path));
						again?.fail("The config file changed after this page loaded it. This field now shows the saved value; make your change again.");
						return false;
					}
					ctl.fail(errorText(e));
					return false;
				}
			},
		};
		fields.set(pathId(path), ctl);
		return ctl;
	}

	// ---------- controls ----------

	// Text-like inputs: Enter (or leaving the field) saves, Esc puts back the saved value.
	function wireText(el, ctl, { saved, parse }) {
		let last = saved;
		el.value = saved;
		const save = async () => {
			if (el.value === last) return;
			const parsed = parse(el.value);
			if (parsed.error) return ctl.fail(parsed.error);
			const before = last;
			last = el.value;
			if (!(await ctl.save(parsed.value))) last = before;
		};
		el.addEventListener("keydown", (e) => {
			if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); save(); }
			else if (e.key === "Escape") { el.value = last; ctl.clear(); el.blur(); }
		});
		el.addEventListener("change", save);
		el.addEventListener("input", () => ctl.clear());
		return el;
	}

	function switchControl(path, value, ctl, labelId) {
		const el = h("button", { type: "button", class: "switch", role: "switch", "aria-checked": String(value), "aria-labelledby": labelId }, h("span", { class: "track" }));
		el.addEventListener("click", async () => {
			const next = el.getAttribute("aria-checked") !== "true";
			el.setAttribute("aria-checked", String(next));
			el.disabled = true;
			const ok = await ctl.save(next);
			el.disabled = false;
			if (!ok) el.setAttribute("aria-checked", String(!next));
		});
		return el;
	}

	function secretControl(value, ctl, labelId) {
		const wrap = h("div", { class: "set-secret" });
		let shown = false;
		const view = () => {
			const text = h("code", { class: `set-secret-value ${value ? "" : "unset"}`, "aria-labelledby": labelId }, value ? (shown ? value : maskSecret(value)) : "Not set");
			const actions = [
				value ? btn(shown ? "Hide" : "Show", { icon: shown ? "eyeOff" : "eye", cls: "ghost sm", onClick: () => { shown = !shown; view(); } }) : null,
				btn(value ? "Change…" : "Set…", { cls: "outline sm", onClick: edit }),
			];
			replace(wrap, text, ...actions);
		};
		const edit = () => {
			const field = h("input", { class: "input mono", type: "password", autocomplete: "off", spellcheck: "false", placeholder: "New value", "aria-labelledby": labelId });
			const save = async () => {
				if (!field.value) return ctl.fail("Enter the new value, or press Esc to keep the current one.");
				if (await ctl.save(field.value)) { value = field.value; shown = false; view(); }
			};
			field.addEventListener("keydown", (e) => {
				if (e.key === "Enter") { e.preventDefault(); save(); }
				else if (e.key === "Escape") { ctl.clear(); view(); }
			});
			wrap.replaceChildren(field, btn("Save", { cls: "primary sm", onClick: save }), btn("Cancel", { cls: "ghost sm", onClick: () => { ctl.clear(); view(); } }));
			field.focus();
		};
		view();
		return wrap;
	}

	function parseLoose(text) {
		const t = text.trim();
		if (t === "") return { value: null };
		if (/^(true|false)$/.test(t)) return { value: t === "true" };
		if (/^-?\d+(\.\d+)?$/.test(t)) return { value: Number(t) };
		return { value: text };
	}

	// One editable scalar. Returns { control, hint } for the row.
	function scalarControl(path, value, ctl, labelId) {
		if (isReadOnly(path)) return { control: h("code", { class: "set-ro", "aria-labelledby": labelId }, value === null ? "null" : String(value)), hint: "Managed by NeoProxy." };
		if (typeof value === "boolean") return { control: switchControl(path, value, ctl, labelId) };
		if (typeof value === "number") {
			const el = h("input", { class: "input mono set-num", type: "number", step: "any", "aria-labelledby": labelId });
			return { control: wireText(el, ctl, { saved: String(value), parse: (t) => (t.trim() === "" || !Number.isFinite(Number(t)) ? { error: "Enter a number." } : { value: Number(t) }) }) };
		}
		if (value === null) {
			const el = h("input", { class: "input mono", placeholder: "Not set", "aria-labelledby": labelId });
			return { control: wireText(el, ctl, { saved: "", parse: parseLoose }), hint: "Not set: the built-in default applies." };
		}
		if (isSecretKey(path)) return { control: secretControl(value, ctl, labelId) };
		const choices = lookup(ENUMS, path);
		if (choices) {
			const options = choices.includes(value) ? choices : [value, ...choices];
			const el = h("select", { class: "select set-select", "aria-labelledby": labelId }, options.map((o) => h("option", { value: o, selected: o === value }, o || "Default")));
			let last = value;
			el.addEventListener("change", async () => {
				const next = el.value;
				if (!(await ctl.save(next))) el.value = last;
				else last = next;
			});
			el.addEventListener("keydown", (e) => { if (e.key === "Escape") { el.value = last; ctl.clear(); } });
			return { control: el };
		}
		if (DURATION.test(value) && value !== "") {
			const el = h("input", { class: "input mono set-dur", spellcheck: "false", "aria-labelledby": labelId });
			return {
				control: h("div", { class: "set-unit" }, wireText(el, ctl, { saved: value, parse: (t) => (DURATION.test(t.trim()) ? { value: t.trim() } : { error: "Use a duration such as 250ms, 5s, 2m or 1h30m." }) }), h("span", { class: "set-unit-hint" }, "duration")),
			};
		}
		if (value.length > 80 || value.includes("\n")) {
			const el = h("textarea", { class: "textarea mono set-long", spellcheck: "false", rows: String(Math.min(8, Math.max(2, Math.ceil(value.length / 70) + value.split("\n").length - 1))), "aria-labelledby": labelId });
			return { control: wireText(el, ctl, { saved: value, parse: (t) => ({ value: t }) }), hint: "Enter saves; Shift+Enter starts a new line." };
		}
		const el = h("input", { class: `input set-text ${/^[\w./:@$~-]*$/.test(value) ? "mono" : ""}`, spellcheck: "false", "aria-labelledby": labelId });
		return { control: wireText(el, ctl, { saved: value, parse: (t) => ({ value: t }) }) };
	}

	// A list of short values as chips you can remove, plus an input to add one.
	function chipList(path, list, ctl, labelId) {
		const secret = isSecretKey(path);
		const numeric = list.length > 0 && list.every((v) => typeof v === "number");
		const id = pathId(path);
		let shown = false;
		const wrap = h("div", { class: "set-chips", role: "group", "aria-labelledby": labelId });
		const draw = () => {
			const all = open.has(id) || list.length <= CHIPS_SHOWN;
			const visible = all ? list : list.slice(0, CHIPS_SHOWN);
			const chips = visible.map((v, i) => h("span", { class: "set-chip" },
				h("span", { class: "mono" }, secret && !shown ? maskSecret(String(v)) : String(v)),
				h("button", { type: "button", class: "set-chip-x", title: `Remove ${secret ? "this key" : v}`, "aria-label": `Remove ${secret ? `key ${i + 1}` : v}`, onClick: () => change(list.filter((_, j) => j !== i)) }, icon("x", 12)),
			));
			const add = h("input", { class: "input mono set-chip-add", type: secret ? "password" : "text", autocomplete: "off", spellcheck: "false", placeholder: list.length ? "Add…" : "Add the first one…", "aria-label": `Add to ${path.at(-1)}` });
			add.addEventListener("keydown", (e) => {
				if (e.key === "Enter") {
					e.preventDefault();
					const v = add.value.trim();
					if (!v) return;
					if (list.some((x) => String(x) === v)) return ctl.fail("That value is already in the list.");
					change([...list, numeric && Number.isFinite(Number(v)) ? Number(v) : v]);
				} else if (e.key === "Escape") { add.value = ""; ctl.clear(); }
			});
			replace(wrap,
				...chips,
				!all ? btn(`Show all ${fmtInt(list.length)}`, { cls: "ghost sm", onClick: () => { open.add(id); draw(); } }) : null,
				secret && list.length ? btn(shown ? "Hide" : "Show", { icon: shown ? "eyeOff" : "eye", cls: "ghost sm", onClick: () => { shown = !shown; draw(); } }) : null,
				add,
			);
			if (focusAdd) { add.focus(); focusAdd = false; }
		};
		let focusAdd = false;
		const change = async (next) => {
			if (await ctl.save(next)) { list = next; focusAdd = true; draw(); }
		};
		draw();
		return wrap;
	}

	// ---------- rows ----------

	function labelBlock(path, key, labelId, extraHint) {
		const hint = lookup(HINTS, path) ?? extraHint;
		return h("div", { class: "set-label" },
			h("span", { class: "set-name", id: labelId }, typeof key === "number" ? `Entry ${key + 1}` : human(key)),
			keyCode(key),
			hint ? h("p", { class: "set-hint" }, hint) : null,
		);
	}

	function scalarRow(path, key, value) {
		const ctl = shell(path);
		const labelId = `set-l-${++uid}`;
		const { control, hint } = scalarControl(path, value, ctl, labelId);
		return h("div", { class: "set-row", dataset: { path: plainPath(path) } },
			labelBlock(path, key, labelId, hint),
			h("div", { class: "set-control" }, h("div", { class: "set-line" }, control, ctl.state), ctl.err),
		);
	}

	function chipRow(path, key, list) {
		const ctl = shell(path);
		const labelId = `set-l-${++uid}`;
		return h("div", { class: "set-row", dataset: { path: plainPath(path) } },
			labelBlock(path, key, labelId),
			h("div", { class: "set-control" }, h("div", { class: "set-line" }, chipList(path, list, ctl, labelId), ctl.state), ctl.err),
		);
	}

	const editLink = (path, label = "Edit in file") => btn(label, { icon: "scroll", cls: "ghost sm set-file-link", onClick: () => file.openAt(path) });

	function fieldsFor(obj, path) {
		const out = [];
		for (const [key, value] of orderedEntries(obj)) out.push(entry([...path, key], key, value));
		if (!out.length) out.push(h("p", { class: "set-empty" }, "Nothing set here yet. ", editLink(path, "Add in the file")));
		return out;
	}

	function entry(path, key, value) {
		if (isScalar(value)) return scalarRow(path, key, value);
		if (Array.isArray(value)) {
			if (value.every(isScalar)) return chipRow(path, key, value);
			return listBlock(path, key, value);
		}
		return groupBlock(path, key, value);
	}

	function groupBlock(path, key, obj) {
		const hint = lookup(HINTS, path);
		return h("fieldset", { class: "set-group", dataset: { path: plainPath(path) } },
			h("legend", { class: "set-legend" }, h("span", { class: "set-name" }, human(key)), keyCode(key)),
			hint ? h("p", { class: "set-hint set-group-hint" }, hint) : null,
			fieldsFor(obj, path),
		);
	}

	// A flat list of small objects (models, keys, error rules) reads best as a table.
	function flatColumns(list) {
		const cols = [];
		for (const item of list) {
			if (!item || typeof item !== "object" || Array.isArray(item)) return null;
			for (const [k, v] of Object.entries(item)) {
				if (!(isScalar(v) || (Array.isArray(v) && v.every((x) => typeof x === "string")))) return null;
				if (!cols.includes(k)) cols.push(k);
			}
		}
		if (cols.length > 6) return null;
		return cols.sort((a, b) => {
			const fa = FIRST.indexOf(a), fb = FIRST.indexOf(b);
			return (fa === -1 ? 50 : fa) - (fb === -1 ? 50 : fb) || (READ_ONLY_LEAF.has(a) ? 1 : 0) - (READ_ONLY_LEAF.has(b) ? 1 : 0);
		});
	}

	function sample(list, cols, key) {
		for (const item of list) if (item[key] !== undefined && item[key] !== null) return item[key];
		return "";
	}

	function cellControl(path, value, kind, ctl, labelId) {
		if (Array.isArray(value) || (value === undefined && Array.isArray(kind))) return chipList(path, value ?? [], ctl, labelId);
		if (value === undefined) {
			const el = h("input", { class: "input mono sm", placeholder: "—", "aria-labelledby": labelId });
			return wireText(el, ctl, { saved: "", parse: (t) => (t.trim() === "" ? { error: "Enter a value." } : typeof kind === "number" ? { value: Number(t) } : { value: t }) });
		}
		return scalarControl(path, value, ctl, labelId).control;
	}

	function tableRows(path, list, cols, indices) {
		const head = h("div", { class: "set-trow set-thead", style: { "--cols": cols.length } }, cols.map((c) => h("span", { class: "set-th" }, human(c))), h("span"));
		const rows = indices.map((i) => {
			const item = list[i];
			const errs = h("div", { class: "set-terrs" });
			const states = h("span", { class: "set-tstate" });
			const cells = cols.map((c) => {
				const p = [...path, i, c];
				const ctl = shell(p);
				const labelId = `set-l-${++uid}`;
				errs.append(ctl.err);
				states.append(ctl.state);
				return h("div", { class: "set-td", dataset: { path: plainPath(p) } },
					h("span", { class: "set-td-label", id: labelId }, `${human(c)}, entry ${i + 1}`),
					cellControl(p, item[c], sample(list, cols, c), ctl, labelId),
				);
			});
			return h("div", { class: "set-trow-wrap" }, h("div", { class: "set-trow", style: { "--cols": cols.length } }, cells, states), errs);
		});
		return h("div", { class: "set-table" }, head, rows);
	}

	function itemTitle(item, i) {
		const name = item.name ?? item.alias ?? item.id ?? item.label;
		const sub = [];
		if (name !== undefined && item["base-url"]) sub.push(item["base-url"]);
		for (const [k, v] of Object.entries(item)) if (Array.isArray(v) && v.length) sub.push(plural(v.length, human(k).toLowerCase().replace(/s$/, ""), human(k).toLowerCase()));
		return { title: name !== undefined ? String(name) : item["base-url"] ?? `Entry ${i + 1}`, sub: sub.join(", ") };
	}

	function itemBlocks(path, list, indices) {
		return indices.map((i) => {
			const item = list[i];
			const p = [...path, i];
			const id = pathId(p);
			if (!item || typeof item !== "object" || Array.isArray(item)) return entry(p, i, item);
			const { title, sub } = itemTitle(item, i);
			const body = h("div", { class: "set-item-body" });
			const det = h("details", { class: "set-item", open: open.has(id) },
				h("summary", null, icon("chevronRight", 14), h("span", { class: "set-item-title mono" }, title), sub ? h("span", { class: "set-item-sub" }, sub) : null),
				body,
			);
			const fill = () => { if (!body.childElementCount) body.append(...fieldsFor(item, p)); };
			if (det.open) fill();
			det.addEventListener("toggle", () => { if (det.open) { open.add(id); fill(); } else open.delete(id); });
			return det;
		});
	}

	function matches(item, q) {
		if (isScalar(item)) return item !== null && String(item).toLowerCase().includes(q);
		if (Array.isArray(item)) return item.some((x) => matches(x, q));
		return Object.entries(item).some(([k, v]) => !(SECRET.test(k) && typeof v === "string") && matches(v, q));
	}

	function listBlock(path, key, list) {
		const id = pathId(path);
		const cols = flatColumns(list);
		const large = list.length > ITEMS_SHOWN;
		const host = h("div", { class: "set-list-body" });
		const count = h("span", { class: "set-count" });
		const draw = () => {
			const q = (queries.get(id) ?? "").trim().toLowerCase();
			let indices = list.map((_, i) => i);
			let note = null;
			if (large) {
				if (!open.has(id) && !q) { host.replaceChildren(); count.textContent = ""; return; }
				if (q) indices = indices.filter((i) => matches(list[i], q));
				const total = indices.length;
				const cap = q ? 50 : open.has(`${id}\u0002all`) ? total : ITEMS_SHOWN;
				indices = indices.slice(0, cap);
				count.textContent = q ? `${plural(total, "match", "matches")}` : "";
				if (total > indices.length) note = h("div", { class: "set-more" }, `Showing ${fmtInt(indices.length)} of ${fmtInt(total)}. `, q ? "Narrow the search to see the rest." : btn(`Show all ${fmtInt(total)}`, { cls: "ghost sm", onClick: () => { open.add(`${id}\u0002all`); draw(); } }));
				if (!total) { host.replaceChildren(h("p", { class: "set-empty" }, `No entry mentions “${q}”.`)); return; }
			}
			replace(host, cols ? tableRows(path, list, cols, indices) : h("div", { class: "set-items" }, itemBlocks(path, list, indices)), note);
		};
		const toggleBtn = large ? btn(open.has(id) ? "Hide" : `Show first ${ITEMS_SHOWN}`, { cls: "outline sm", onClick: () => {
			if (open.has(id)) open.delete(id); else open.add(id);
			toggleBtn.lastChild.textContent = open.has(id) ? "Hide" : `Show first ${ITEMS_SHOWN}`;
			draw();
		} }) : null;
		const finder = large ? h("input", { class: "input sm set-list-search", type: "search", placeholder: `Search ${fmtInt(list.length)} entries`, value: queries.get(id) ?? "", "aria-label": `Search ${human(key)}`, onInput: (e) => { queries.set(id, e.target.value); draw(); } }) : null;
		const hint = lookup(HINTS, path);
		const block = h("div", { class: "set-list", dataset: { path: plainPath(path) } },
			h("div", { class: "set-list-head" },
				h("div", { class: "set-label" },
					h("span", { class: "set-name" }, human(key), " ", h("span", { class: "set-count-total" }, plural(list.length, "entry", "entries"))),
					keyCode(key),
					hint ? h("p", { class: "set-hint" }, hint) : null,
				),
				h("div", { class: "set-list-tools" }, finder, count, toggleBtn, editLink(path)),
			),
			host,
		);
		draw();
		return block;
	}

	// ---------- sections ----------

	function renderForm() {
		fields = new Map();
		const keys = Object.keys(data);
		const known = SECTIONS.map(([k]) => k).filter((k) => keys.includes(k));
		const nested = keys.filter((k) => !SECTION.has(k) && !isScalar(data[k]) && !Array.isArray(data[k])).sort();
		const loose = keys.filter((k) => !SECTION.has(k) && !nested.includes(k));
		const cards = [];
		for (const key of [...known, ...nested]) {
			const meta = SECTION.get(key) ?? { title: human(key), intro: "Settings this page has no description for yet; they are shown as the file has them." };
			const value = data[key];
			cards.push(card({
				title: h("span", { class: "set-title" }, meta.title, meta.title.toLowerCase() !== key.replace(/-/g, " ") ? h("code", { class: "set-key" }, key) : null),
				sub: meta.intro,
				actions: [editLink([key])],
				cls: "set-card",
				body: Array.isArray(value) ? entry([key], key, value) : h("div", { class: "set-fields" }, fieldsFor(value, [key])),
				rise: false,
			}));
			cards.at(-1).id = `set-sec-${key}`;
		}
		const others = loose.filter((k) => k !== "config-version");
		if (others.length) {
			cards.push(card({ title: "Other settings", sub: "Top-level settings outside any group.", cls: "set-card", rise: false, body: h("div", { class: "set-fields" }, others.map((k) => entry([k], k, data[k]))) }));
			cards.at(-1).id = "set-sec-other";
		}
		formHost.replaceChildren(...cards);
		index.replaceChildren(...[...known, ...nested].map((k) => h("a", { class: "set-index-link", href: `#set-sec-${k}`, onClick: (e) => { e.preventDefault(); document.getElementById(`set-sec-${k}`)?.scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "start" }); } }, SECTION.get(k)?.title ?? human(k))),
			h("a", { class: "set-index-link file", href: "#set-file", onClick: (e) => { e.preventDefault(); file.openAt(null); } }, icon("scroll", 13), "The file"));
		file.setVersion(data["config-version"]);
	}

	async function refreshForm() {
		await fetchConfig();
		if (alive) renderForm();
	}

	// ---------- the file ----------

	const file = (() => {
		let editor = null;
		let format = null; // "yaml" | "toml"
		let baseline = null;
		let fileEtag = null;
		let busy = false;
		let loaded = false;
		const name = () => `config.${format ?? "yaml"}`;
		const errorBox = h("div", { class: "inline-error", id: "set-file-error", role: "alert", hidden: true });
		const notice = h("div", { class: "set-file-notice", hidden: true });
		const stats = h("span", { class: "set-file-stats mono" });
		const findInput = h("input", { class: "input sm", type: "search", placeholder: "Find in file", "aria-label": "Find in file" });
		const findCount = h("span", { class: "set-find-count" });
		const saveButton = btn("Save file", { icon: "save", cls: "primary sm", disabled: true, onClick: () => save() });
		const reloadFile = btn("Reload from disk", { icon: "refresh", cls: "ghost sm", onClick: () => load({ ask: true }) });
		let secretsShown = false;
		const secretsButton = btn("Show secrets", { icon: "eye", cls: "ghost sm", onClick: () => {
			secretsShown = !secretsShown;
			editor?.showSecrets(secretsShown);
			secretsButton.replaceChildren(icon(secretsShown ? "eyeOff" : "eye", 15), secretsShown ? "Hide secrets" : "Show secrets");
		} });
		const toolbar = h("div", { class: "set-file-tools" },
			h("div", { class: "search set-find" }, icon("search", 15), findInput),
			findCount,
			iconBtn("arrowLeft", "Previous match", () => runFind({ backwards: true }), "sm"),
			iconBtn("arrowRight", "Next match", () => runFind({}), "sm"),
			h("span", { class: "spacer" }),
			stats,
			secretsButton,
			reloadFile,
			saveButton,
		);
		const editorHost = h("div", { class: "set-file-editor" });
		const body = h("div", { class: "set-file-body", hidden: true }, notice, errorBox, toolbar, editorHost);
		const toggle = btn("Open the file", { icon: "chevronDown", cls: "outline sm", onClick: () => (body.hidden ? show() : hide()) });
		const titleName = h("code", { class: "set-key" }, "config.yaml");
		const sub = h("span", null, "The file as it is on disk, for anything the form above can't express: adding or removing entries, comments, ordering. The engine checks every save; if it refuses, the reason and the line show here and nothing changes.");
		const el = card({ title: h("span", { class: "set-title" }, "Edit the file", titleName), sub, actions: [toggle], cls: "set-card set-file", body, rise: false });
		el.id = "set-file";
		fileHost.append(el);

		const dirty = () => editor && editor.value !== baseline;
		const update = () => {
			saveButton.disabled = busy || !dirty();
			if (!editor) return;
			const text = editor.value;
			stats.textContent = `${plural(editor.lineCount, "line")}, ${plural(new TextEncoder().encode(text).length, "byte")}${dirty() ? ", edited" : ""}`;
		};
		const showError = (message) => {
			if (!message) { errorBox.hidden = true; editor?.markError(null); editor?.area.removeAttribute("aria-invalid"); return; }
			const line = Number(/\bline (\d+)/i.exec(message)?.[1]) || null;
			errorBox.replaceChildren(
				h("div", { class: "inline-error-title" }, `${name()} was not saved`),
				h("pre", { class: "inline-error-text" }, message),
				h("div", { class: "inline-error-foot" }, "The running config is unchanged and your edits are still in the editor.", line ? btn(`Go to line ${line}`, { cls: "outline sm", onClick: () => editor.goToLine(line) }) : null),
			);
			errorBox.hidden = false;
			editor?.area.setAttribute("aria-invalid", "true");
			editor?.markError(line);
		};
		const runFind = (opts) => {
			if (!editor) return;
			const r = editor.find(findInput.value, opts);
			findCount.textContent = !r ? "" : r.count ? `${fmtInt(r.index + 1)} of ${fmtInt(r.count)}` : "No matches";
		};
		findInput.addEventListener("input", () => runFind({ restart: true }));
		findInput.addEventListener("keydown", (e) => {
			if (e.key === "Enter") { e.preventDefault(); runFind({ backwards: e.shiftKey }); }
			else if (e.key === "Escape" && findInput.value) { findInput.value = ""; runFind({}); }
		});

		async function detect() {
			if (format) return;
			try {
				await engine.raw("GET", "config.toml");
				format = "toml";
			} catch {
				format = "yaml";
			}
			titleName.textContent = name();
		}

		async function load({ ask = false, quiet = false } = {}) {
			if (busy) return;
			if (ask && dirty() && !(await confirm({ title: "Discard your edits?", text: `Load ${name()} from disk again without saving these changes.`, confirmLabel: "Discard edits", danger: true }))) return;
			busy = true;
			update();
			try {
				await detect();
				const res = await engine.raw("GET", name());
				const text = await res.text();
				fileEtag = res.headers.get("ETag");
				baseline = text;
				if (!editor) {
					editor = codeEditor({ value: text, language: format, label: name(), describedBy: "set-file-error", maskKeys: SECRET, onInput: () => { update(); if (!errorBox.hidden) showError(null); }, onSave: () => save(), onFind: () => findInput.focus() });
					editorHost.replaceChildren(editor.el);
				} else if (editor.value !== text) {
					const top = editor.area.scrollTop;
					editor.setValue(text);
					editor.area.scrollTop = top;
				}
				loaded = true;
				notice.hidden = true;
				if (!quiet) showError(null);
			} catch (e) {
				if (!editor) editorHost.replaceChildren(errorState(e, () => load()));
				else showError(errorText(e));
			} finally {
				busy = false;
				update();
			}
		}

		async function save() {
			if (busy || !dirty()) return;
			const text = editor.value;
			busy = true;
			update();
			showError(null);
			try {
				const res = await engine.raw("PUT", name(), { body: text, headers: { "Content-Type": format === "toml" ? "application/toml" : "application/yaml", ...(fileEtag ? { "If-Match": fileEtag } : {}) } });
				fileEtag = res.headers.get("ETag");
				baseline = text;
				busy = false;
				saveButton.replaceChildren(icon("check", 15), "Saved");
				setTimeout(() => saveButton.replaceChildren(icon("save", 15), "Save file"), 2000);
				// The engine may tidy the text (quoting, indentation); show what it wrote,
				// and bring the form above up to date.
				await load({ quiet: true });
				await refreshForm();
			} catch (e) {
				if (e instanceof ApiError && e.status === 412) showError(`${name()} changed on disk after you opened it here, probably from a setting saved above. Copy your edits, choose Reload from disk, and apply them again.`);
				else showError(errorText(e));
			} finally {
				busy = false;
				update();
			}
		}

		async function show() {
			body.hidden = false;
			toggle.replaceChildren(icon("chevronDown", 15), "Close the file");
			toggle.classList.add("on");
			if (!loaded) await load();
		}
		function hide() {
			body.hidden = true;
			toggle.replaceChildren(icon("chevronDown", 15), "Open the file");
			toggle.classList.remove("on");
		}

		return {
			dirty,
			async reload() { if (loaded) await load(); },
			setVersion(v) { titleName.title = v ? `Config layout version ${v}` : ""; },
			async openAt(path) {
				await show();
				el.scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "start" });
				if (path && editor) editor.goToLine(findKeyLine(editor.value, path, format));
			},
			// A setting was saved from the form: the file on disk moved on.
			async formSaved() {
				if (!loaded) return;
				if (!dirty()) return load({ quiet: true });
				notice.replaceChildren(icon("info", 14), h("span", null, `A setting was saved above, so ${name()} on disk is newer than your edits here. Saving now would be refused.`), btn("Load the latest (discard my edits)", { cls: "outline sm", onClick: () => load() }));
				notice.hidden = false;
			},
		};
	})();

	async function reloadAll() {
		if (file.dirty() && !(await confirm({ title: "Discard your edits?", text: "Your unsaved edits to the file would be replaced with what is on disk.", confirmLabel: "Discard edits", danger: true }))) return;
		reloadButton.disabled = true;
		try {
			await refreshForm();
			await file.reload();
		} catch (e) {
			formHost.replaceChildren(errorState(e, reloadAll));
		} finally {
			reloadButton.disabled = false;
		}
	}

	try {
		await fetchConfig();
		renderForm();
	} catch (e) {
		formHost.replaceChildren(errorState(e, reloadAll));
	}

	return () => { alive = false; };
}
