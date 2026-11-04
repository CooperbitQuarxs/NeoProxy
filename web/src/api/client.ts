export class ApiError extends Error {
	status: number;
	code?: string;
	constructor(status: number, message: string, code?: string) {
		super(message);
		this.status = status;
		this.code = code;
	}
}

const unauthorizedListeners = new Set<() => void>();
export function onUnauthorized(fn: () => void): () => void {
	unauthorizedListeners.add(fn);
	return () => void unauthorizedListeners.delete(fn);
}

type Opts = { json?: unknown; body?: BodyInit; query?: Record<string, string | number | undefined>; headers?: Record<string, string>; quiet401?: boolean };

export async function api<T = unknown>(method: string, path: string, opts: Opts = {}): Promise<T> {
	let url = path.startsWith("/") ? path : `/v8/neo/${path}`;
	if (opts.query) {
		const q = new URLSearchParams();
		for (const [k, v] of Object.entries(opts.query)) if (v !== undefined && v !== "") q.set(k, String(v));
		const s = q.toString();
		if (s) url += `?${s}`;
	}
	const headers: Record<string, string> = { Accept: "application/json", ...opts.headers };
	let body = opts.body;
	if (opts.json !== undefined) {
		headers["Content-Type"] = "application/json";
		body = JSON.stringify(opts.json);
	}
	const res = await fetch(url, { method, headers, body, credentials: "same-origin" });
	const text = await res.text();
	let data: unknown = undefined;
	if (text) {
		try {
			data = JSON.parse(text);
		} catch {
			data = text;
		}
	}
	if (!res.ok) {
		if (res.status === 401 && !opts.quiet401) unauthorizedListeners.forEach((fn) => fn());
		const err = (data as { error?: { message?: string; code?: string } | string } | undefined)?.error;
		const message = typeof err === "string" ? err : err?.message ?? (typeof data === "string" && data ? data : `${res.status} ${res.statusText}`);
		throw new ApiError(res.status, message, typeof err === "object" ? err?.code : undefined);
	}
	return data as T;
}

export const get = <T>(path: string, opts?: Opts) => api<T>("GET", path, opts);
export const post = <T>(path: string, json?: unknown, opts?: Opts) => api<T>("POST", path, { ...opts, json });
export const patch = <T>(path: string, json?: unknown, opts?: Opts) => api<T>("PATCH", path, { ...opts, json });
export const put = <T>(path: string, json?: unknown, opts?: Opts) => api<T>("PUT", path, { ...opts, json });
export const del = <T>(path: string, opts?: Opts) => api<T>("DELETE", path, opts);
