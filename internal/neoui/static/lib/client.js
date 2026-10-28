// Cookie-authenticated dashboard transport. Inference keys and operator secrets
// are not dashboard identities; only the explicit one-time bootstrap uses a secret.
let identity = null;
// Remove credentials left by the pre-session dashboard, never migrate them.
for (const storage of [sessionStorage, localStorage]) storage.removeItem("neo-session");
export const session = {
	get: () => identity,
	set(value) { identity = value; invalidate(); },
	clear() { identity = null; invalidate(); },
};

export class ApiError extends Error {
	constructor(status, message, body) { super(message); this.status = status; this.body = body; }
}
const unauthorizedListeners = new Set();
export function onUnauthorized(fn) { unauthorizedListeners.add(fn); return () => unauthorizedListeners.delete(fn); }

async function request(method, path, { json, body, headers = {}, raw = false, query, auth = true } = {}) {
	let url = path.startsWith("/") ? path : `/v8/management/${path}`;
	if (query) {
		const q = new URLSearchParams();
		for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
		if (q.size) url += (url.includes("?") ? "&" : "?") + q;
	}
	const init = { method, credentials: "same-origin", cache: "no-store", headers: { ...headers } };
	const owner = identity;
	if (json !== undefined) { init.headers["Content-Type"] = "application/json"; init.body = JSON.stringify(json); }
	else if (body !== undefined) init.body = body;
	const res = await fetch(url, init);
	// A forbidden operation is not an expired identity. Leave the session intact.
	if (res.status === 401 && auth && owner && owner === identity) for (const fn of unauthorizedListeners) fn();
	if (raw && res.ok) return res;
	const text = await res.text();
	let data = text;
	if ((res.headers.get("content-type") || "").includes("application/json") && text) {
		try { data = JSON.parse(text); } catch {}
	}
	if (!res.ok) {
		const msg = (data && typeof data === "object" && (data.error?.message || data.error || data.message)) || `${res.status} ${res.statusText}`;
		throw new ApiError(res.status, typeof msg === "string" ? msg : JSON.stringify(msg), data);
	}
	return data;
}

const cache = new Map();
async function cached(key, loader, ttl = 15_000) {
	const hit = cache.get(key);
	if (hit && Date.now() - hit.at < ttl) return hit.value;
	const owner = identity;
	const value = await loader();
	if (identity === owner) cache.set(key, { at: Date.now(), value });
	return value;
}
export function invalidate(prefix) { for (const k of cache.keys()) if (!prefix || k.startsWith(prefix)) cache.delete(k); }
export const engine = {
	get: (p, o) => request("GET", p, o),
	put: (p, json, o) => request("PUT", p, { ...o, json }),
	patch: (p, json, o) => request("PATCH", p, { ...o, json }),
	post: (p, json, o) => request("POST", p, { ...o, json }),
	del: (p, o) => request("DELETE", p, o),
	raw: (m, p, o) => request(m, p, { ...o, raw: true }),
	config: () => cached("config", () => request("GET", "config")),
	configYaml: () => request("GET", "config.yaml"),
	credentials: () => cached("credentials", () => request("GET", "credentials")),
	credentialModels: () => cached("credential-models", () => request("GET", "credentials/models"), 60_000),
	usage: () => cached("usage", () => request("GET", "observability/usage/api-keys"), 5_000),
	plugins: () => cached("plugins", () => request("GET", "plugins")),
	pluginStore: () => cached("plugin-store", () => request("GET", "plugins/store"), 60_000),
	latestVersion: () => cached("latest-version", () => request("GET", "server/latest-version"), 300_000),
	logs: (lines) => request("GET", "observability/logs", { query: { lines } }),
	errorLogs: () => request("GET", "observability/logs/errors"),
	health: async () => (await fetch("/healthz", { cache: "no-store" })).ok,
};

const changeListeners = new Set();
export function onChange(fn) { changeListeners.add(fn); return () => changeListeners.delete(fn); }
function changed(what) { for (const fn of changeListeners) fn(what); }
const path = (p) => `/v8/neo/${p}`;
const id = encodeURIComponent;
async function mutate(method, route, json, what) {
	const value = await request(method, path(route), { json });
	// Notify after the caller has consumed the returned entity.
	setTimeout(() => changed(what), 0);
	return value;
}
export const neo = {
	core: () => request("GET", path("core")),
	vault: () => request("GET", path("vault")),
	models: () => request("GET", path("models")),
	setProviderExposed: (provider, exposed) => mutate("PATCH", `vault/providers/${id(provider)}`, { exposed }, "vault"),
	setModelExposed: (model, exposed) => mutate("PATCH", `models/${id(model)}`, { exposed }, "models"),
	virtualModels: () => request("GET", path("virtual-models")),
	virtualModel: (model) => request("GET", path(`virtual-models/${id(model)}`)),
	saveVirtualModel: (vm) => mutate(vm.id ? "PUT" : "POST", `virtual-models${vm.id ? `/${id(vm.id)}` : ""}`, vm, "virtual-models"),
	deleteVirtualModel: (model) => mutate("DELETE", `virtual-models/${id(model)}`, undefined, "virtual-models"),
	users: () => request("GET", path("users")),
	user: (user) => request("GET", path(`users/${id(user)}`)),
	saveUser: (user) => mutate(user.id ? "PUT" : "POST", `users${user.id ? `/${id(user.id)}` : ""}`, user, "users"),
	createKey: (user, label, accountIds) => mutate("POST", `users/${id(user)}/keys`, { label, ...(accountIds ? { accountIds } : {}) }, "users"),
	revokeKey: (user, key) => mutate("DELETE", `users/${id(user)}/keys/${id(key)}`, undefined, "users"),
	usage: () => request("GET", path("usage")),
	requests: (query = {}) => request("GET", path("requests"), { query: { limit: 100, ...query } }),
	request: (requestId) => request("GET", path(`requests/${id(requestId)}`)),
	simulate: ({ asked, userId }) => request("POST", path("simulate"), { json: { asked, userId } }),
	async restore() {
		try { const s = await request("GET", "/v8/session", { auth: false }); session.set(s); return s; }
		catch (e) { session.clear(); if (e.status === 401) return null; throw e; }
	},
	async signIn(handle, password) {
		const s = await request("POST", "/v8/session", { json: { handle, password }, auth: false });
		session.set(s); return s;
	},
	async bootstrap(handle, password, secret, name) {
		const s = await request("POST", "/v8/session/bootstrap", { json: { handle, password, name }, headers: { Authorization: `Bearer ${secret}` }, auth: false });
		session.set(s); return s;
	},
	async signOut() { await request("DELETE", "/v8/session", { auth: false }); session.clear(); },
	// Poll the durable request ledger; never synthesize traffic on an idle server.
	subscribeStream(fn, onError = () => {}) {
		let stopped = false, timer;
		const seen = new Set();
		let initialized = false;
		async function poll() {
			try {
				const rows = await neo.requests({ limit: 14 });
				if (stopped) return;
				for (const r of [...rows].reverse()) { if (initialized && !seen.has(r.id)) fn(r); seen.add(r.id); }
				if (seen.size > 1000) { seen.clear(); for (const r of rows) seen.add(r.id); }
				initialized = true;
				onError(null);
			} catch (e) { if (!stopped) onError(e); }
			if (!stopped) timer = setTimeout(poll, 5000);
		}
		poll();
		return () => { stopped = true; clearTimeout(timer); };
	},
};
export const sources = Object.fromEntries(["overview", "core", "models", "virtual-models", "users", "activity", "credentials", "keys", "logs", "routing", "config", "plugins", "about"].map((name) => [name, "engine"]));
