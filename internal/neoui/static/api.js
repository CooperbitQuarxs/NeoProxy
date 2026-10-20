// Management API client. The key lives in sessionStorage (closing the tab
// forgets it) unless the user asks to remember it, which moves it to
// localStorage like upstream's control panel does.

const KEY = "neo-management-key";

export const auth = {
	get key() {
		return sessionStorage.getItem(KEY) || localStorage.getItem(KEY) || "";
	},
	set(key, remember) {
		sessionStorage.removeItem(KEY);
		localStorage.removeItem(KEY);
		(remember ? localStorage : sessionStorage).setItem(KEY, key);
	},
	clear() {
		sessionStorage.removeItem(KEY);
		localStorage.removeItem(KEY);
	},
};

export class ApiError extends Error {
	constructor(status, message, body) {
		super(message);
		this.status = status;
		this.body = body;
	}
}

const listeners = new Set();
export function onUnauthorized(fn) {
	listeners.add(fn);
	return () => listeners.delete(fn);
}

async function request(method, path, { json, body, headers = {}, raw = false, query } = {}) {
	let url = path.startsWith("/") ? path : `/v8/management/${path}`;
	if (query) {
		const q = new URLSearchParams();
		for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
		const s = q.toString();
		if (s) url += (url.includes("?") ? "&" : "?") + s;
	}
	const init = { method, headers: { ...headers } };
	if (auth.key) init.headers.Authorization = `Bearer ${auth.key}`;
	if (json !== undefined) {
		init.headers["Content-Type"] = "application/json";
		init.body = JSON.stringify(json);
	} else if (body !== undefined) {
		init.body = body;
	}
	const res = await fetch(url, init);
	if (res.status === 401 || res.status === 403) {
		for (const fn of listeners) fn(res.status);
	}
	if (raw) return res;
	const text = await res.text();
	let data = text;
	const ct = res.headers.get("content-type") || "";
	if (ct.includes("application/json") && text) {
		try {
			data = JSON.parse(text);
		} catch {}
	}
	if (!res.ok) {
		const msg = (data && typeof data === "object" && (data.error?.message || data.error || data.message)) || `${res.status} ${res.statusText}`;
		throw new ApiError(res.status, typeof msg === "string" ? msg : JSON.stringify(msg), data);
	}
	return data;
}

export const api = {
	get: (p, o) => request("GET", p, o),
	put: (p, json, o) => request("PUT", p, { ...o, json }),
	patch: (p, json, o) => request("PATCH", p, { ...o, json }),
	post: (p, json, o) => request("POST", p, { ...o, json }),
	del: (p, o) => request("DELETE", p, o),
	raw: (m, p, o) => request(m, p, { ...o, raw: true }),
};

// A small cache so pages that share data (config, credentials) do not refetch
// on every navigation; `invalidate` after a write.
const cache = new Map();
export async function cached(key, loader, ttlMs = 15000) {
	const hit = cache.get(key);
	if (hit && Date.now() - hit.at < ttlMs) return hit.value;
	const value = await loader();
	cache.set(key, { at: Date.now(), value });
	return value;
}
export function invalidate(prefix) {
	for (const k of [...cache.keys()]) if (!prefix || k.startsWith(prefix)) cache.delete(k);
}

export const load = {
	config: () => cached("config", () => api.get("config")),
	configYaml: () => api.get("config.yaml"),
	credentials: () => cached("credentials", () => api.get("credentials")),
	credentialModels: () => cached("credential-models", () => api.get("credentials/models"), 60000),
	usage: () => cached("usage", () => api.get("observability/usage/api-keys"), 5000),
	plugins: () => cached("plugins", () => api.get("plugins")),
	pluginStore: () => cached("plugin-store", () => api.get("plugins/store"), 60000),
	latestVersion: () => cached("latest-version", () => api.get("server/latest-version"), 300000),
	logs: (lines) => api.get("observability/logs", { query: { lines } }),
	errorLogs: () => api.get("observability/logs/errors"),
};
