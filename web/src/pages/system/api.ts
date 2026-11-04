import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError } from "../../api/client";

// Engine management API (/v8/management). Its errors come in three shapes:
// {"error": "text"}, {"error": "code", "message": "text"} and
// {"error": {"message": "text"}}; the human sentence is preferred.
type Raw = { data: unknown; etag: string | null; status: number };

// Codes the engine sends without a sentence.
const CODE_TEXT: Record<string, string> = {
	invalid_body: "This isn't valid YAML. Check for an unclosed bracket or quote, or a line indented with a tab instead of spaces.",
	invalid_json: "The settings couldn't be read as JSON.",
	config_must_be_object: "The file must be a set of settings (key: value lines), not a list or a single value.",
	read_only_field: "A value NeoProxy manages itself was changed. Put it back as it was",
	invalid_path: "That setting path doesn't exist.",
	not_found: "That setting isn't in the configuration.",
	precondition_failed: "The configuration changed on disk after this page loaded.",
	write_failed: "NeoProxy couldn't write its config file. Check the file's permissions and free disk space.",
	read_failed: "NeoProxy couldn't read its config file. Check that it still exists and is readable.",
};

async function engine(method: string, path: string, opts: { json?: unknown; text?: string; contentType?: string; query?: Record<string, string | number | undefined>; ifMatch?: string | null } = {}): Promise<Raw> {
	let url = `/v8/management/${path.replace(/^\/+/, "")}`;
	if (opts.query) {
		const q = new URLSearchParams();
		for (const [k, v] of Object.entries(opts.query)) if (v !== undefined && v !== "") q.set(k, String(v));
		if (q.size) url += `?${q}`;
	}
	const headers: Record<string, string> = { Accept: "application/json" };
	if (method !== "GET") headers["X-Neoproxy-CSRF"] = "1";
	if (opts.ifMatch) headers["If-Match"] = opts.ifMatch;
	let body: BodyInit | undefined;
	if (opts.json !== undefined) {
		headers["Content-Type"] = "application/json";
		body = JSON.stringify(opts.json);
	} else if (opts.text !== undefined) {
		headers["Content-Type"] = opts.contentType ?? "text/plain";
		body = opts.text;
	}
	const res = await fetch(url, { method, headers, body, credentials: "same-origin", cache: "no-store" });
	const text = await res.text();
	let data: unknown = text;
	if ((res.headers.get("content-type") ?? "").includes("json") && text) {
		try {
			data = JSON.parse(text);
		} catch {
			/* keep text */
		}
	}
	if (!res.ok) {
		const d = data as { error?: unknown; message?: unknown } | string;
		let message = `${res.status} ${res.statusText}`;
		let code: string | undefined;
		if (typeof d === "string" && d) message = d;
		else if (d && typeof d === "object") {
			if (typeof d.message === "string" && d.message) message = d.message;
			else if (typeof d.error === "string") message = CODE_TEXT[d.error] ?? d.error;
			else if (d.error && typeof d.error === "object" && typeof (d.error as { message?: unknown }).message === "string") message = (d.error as { message: string }).message;
			if (d.error === "read_only_field" && typeof (d as { field?: unknown }).field === "string") message += `: ${(d as { field: string }).field}.`;
			if (typeof d.error === "string") code = d.error;
		}
		throw new ApiError(res.status, message, code);
	}
	return { data, etag: res.headers.get("ETag"), status: res.status };
}

const get = async <T>(path: string, query?: Record<string, string | number | undefined>) => (await engine("GET", path, { query })).data as T;

// ---------- configuration ----------
export type EngineConfig = {
	access?: { "api-keys"?: Array<string | { key?: string; "api-key"?: string }> };
	management?: { "secret-key"?: string; "disable-control-panel"?: boolean };
	server?: { host?: string; port?: number };
	routing?: Routing;
	observability?: { logs?: { debug?: boolean; "logging-to-file"?: boolean; "request-log"?: boolean } };
	plugins?: { enabled?: boolean; configs?: Record<string, { enabled?: boolean }> };
	"config-version"?: number;
	[k: string]: unknown;
};

export type Routing = {
	strategy?: string;
	"session-affinity"?: boolean;
	"force-model-prefix"?: boolean;
	retry?: { "request-retry"?: number; "max-retry-credentials"?: number; "max-retry-interval"?: number };
	cooldown?: { "disable-cooling"?: boolean; "save-cooldown-status"?: boolean; "transient-error-cooldown-seconds"?: number };
	[k: string]: unknown;
};

export const CONFIG_KEY = ["system", "config"] as const;
export function useEngineConfig() {
	return useQuery({
		queryKey: CONFIG_KEY,
		queryFn: async () => {
			const r = await engine("GET", "config");
			return { config: r.data as EngineConfig, etag: r.etag };
		},
		staleTime: 10_000,
	});
}

function useInvalidateConfig() {
	const qc = useQueryClient();
	return () => {
		qc.invalidateQueries({ queryKey: CONFIG_KEY });
		qc.invalidateQueries({ queryKey: ["system", "yaml"] });
		qc.invalidateQueries({ queryKey: ["system", "plugins"] });
	};
}

/** Merge-patch the config. Sends If-Match so a concurrent save is reported, not overwritten. */
export function usePatchConfig() {
	const invalidate = useInvalidateConfig();
	return useMutation({
		mutationFn: (v: { patch: Record<string, unknown>; etag?: string | null }) => engine("PATCH", "config", { json: v.patch, ifMatch: v.etag }),
		onSettled: invalidate,
	});
}

/** Replace one config subtree, e.g. path "access/api-keys". */
export function usePutConfigPath() {
	const invalidate = useInvalidateConfig();
	return useMutation({
		mutationFn: (v: { path: string; value: unknown; etag?: string | null }) => engine("PUT", `config/${v.path}`, { json: v.value, ifMatch: v.etag }),
		onSettled: invalidate,
	});
}

export function useConfigYaml() {
	return useQuery({
		queryKey: ["system", "yaml"],
		queryFn: async () => {
			const r = await engine("GET", "config.yaml");
			return { text: typeof r.data === "string" ? r.data : JSON.stringify(r.data, null, 2), etag: r.etag, loadedAt: Date.now() };
		},
		staleTime: Infinity,
		refetchOnWindowFocus: false,
	});
}

export function useSaveConfigYaml() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { text: string; etag?: string | null }) => engine("PUT", "config.yaml", { text: v.text, contentType: "application/yaml", ifMatch: v.etag }),
		onSuccess: (r, v) => {
			qc.setQueryData(["system", "yaml"], { text: v.text, etag: r.etag, loadedAt: Date.now() });
			qc.invalidateQueries({ queryKey: CONFIG_KEY });
			qc.invalidateQueries({ queryKey: ["system", "plugins"] });
		},
	});
}

// ---------- credentials (for cooldowns and plugin quota) ----------
export type EngineCredential = {
	id: string;
	name: string;
	provider: string;
	label?: string;
	email?: string;
	auth_index?: string;
	cooldowns?: unknown[];
	disabled?: boolean;
	status?: string;
};
export function useEngineCredentials() {
	return useQuery({
		queryKey: ["system", "credentials"],
		queryFn: async () => (await get<{ files?: EngineCredential[] }>("credentials")).files ?? [],
		refetchInterval: 20_000,
	});
}

export function useResetCooldowns() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: async (creds: EngineCredential[]) => {
			let count = 0;
			for (const c of creds) {
				if (!c.auth_index) continue;
				await engine("POST", "routing/cooldown/reset", { json: { auth_index: c.auth_index } });
				count++;
			}
			return count;
		},
		onSettled: () => qc.invalidateQueries({ queryKey: ["system", "credentials"] }),
	});
}

// ---------- model definitions ----------
// Canonical channels in internal/registry/model_definitions.go.
export const CHANNELS = ["claude", "gemini", "gemini-interactions", "vertex", "aistudio", "codex", "kimi", "antigravity", "xai", "devin", "meta"];
export type ModelDefinition = { id: string; display_name?: string; description?: string; context_length?: number; max_completion_tokens?: number; owned_by?: string };
export function useModelDefinitions() {
	return useQuery({
		queryKey: ["system", "model-definitions"],
		queryFn: async () => {
			const rows = await Promise.all(
				CHANNELS.map(async (channel) => {
					try {
						const r = await get<{ models?: Array<ModelDefinition | string> }>(`routing/model-definitions/${channel}`);
						return { channel, models: (r?.models ?? []).map((m) => (typeof m === "string" ? { id: m } : m)) };
					} catch (e) {
						if (e instanceof ApiError && e.status === 404) return null;
						throw e;
					}
				}),
			);
			return rows.filter((r): r is { channel: string; models: ModelDefinition[] } => !!r);
		},
		staleTime: 5 * 60_000,
	});
}

// ---------- logs ----------
export type LogsPage = { lines: string[]; "line-count"?: number; "latest-timestamp"?: number; "next-cursor"?: string; "cursor-reset"?: boolean };
export function fetchLogs(limit: number) {
	return get<LogsPage>("observability/logs", { limit });
}
export function useClearLogs() {
	return useMutation({ mutationFn: () => engine("DELETE", "observability/logs") });
}
export type ErrorDump = { name: string; size?: number; modified?: number };
export function useErrorDumps() {
	return useQuery({ queryKey: ["system", "error-dumps"], queryFn: async () => (await get<{ files?: ErrorDump[] }>("observability/logs/errors")).files ?? [], refetchInterval: 30_000 });
}
export async function downloadErrorDump(name: string) {
	const res = await fetch(`/v8/management/observability/logs/errors/${encodeURIComponent(name)}`, { credentials: "same-origin", cache: "no-store" });
	if (!res.ok) throw new ApiError(res.status, `Couldn't download ${name} (${res.status}). It may have been rotated away; reload the list.`);
	const url = URL.createObjectURL(await res.blob());
	const a = document.createElement("a");
	a.href = url;
	a.download = name;
	document.body.append(a);
	a.click();
	a.remove();
	setTimeout(() => URL.revokeObjectURL(url), 1000);
}

// ---------- plugins ----------
export type InstalledPlugin = {
	id: string;
	path?: string;
	configured?: boolean;
	registered?: boolean;
	enabled?: boolean;
	effective_enabled?: boolean;
	supports_oauth?: boolean;
	oauth_provider?: string;
	supports_quota?: boolean;
	quota_provider?: string;
	config_fields?: Array<{ name: string; type?: string; description?: string; default?: unknown; enum_values?: string[] }>;
	metadata?: { name?: string; version?: string; author?: string; github_repository?: string } | null;
};
export type StorePlugin = {
	store_id?: string;
	source_id?: string;
	id: string;
	name?: string;
	description?: string;
	author?: string;
	version?: string;
	repository?: string;
	homepage?: string;
	license?: string;
	tags?: string[];
	source_name?: string;
	installed?: boolean;
	installed_version?: string;
	update_available?: boolean;
	auth_required?: boolean;
	auth_configured?: boolean;
};
export function usePlugins() {
	return useQuery({ queryKey: ["system", "plugins", "installed"], queryFn: () => get<{ plugins_enabled?: boolean; plugins_dir?: string; plugins?: InstalledPlugin[] }>("plugins") });
}
export function usePluginStore() {
	return useQuery({ queryKey: ["system", "plugins", "store"], queryFn: () => get<{ plugins?: StorePlugin[]; sources?: Array<{ id: string; name: string; url: string }> }>("plugins/store"), staleTime: 5 * 60_000, retry: false });
}
function useInvalidatePlugins() {
	const qc = useQueryClient();
	return () => {
		qc.invalidateQueries({ queryKey: ["system", "plugins"] });
		qc.invalidateQueries({ queryKey: CONFIG_KEY });
	};
}
export function useInstallPlugin() {
	const invalidate = useInvalidatePlugins();
	return useMutation({
		mutationFn: (v: { id: string; source?: string }) => engine("POST", `plugins/store/${encodeURIComponent(v.id)}/install`, { json: {}, query: { source: v.source } }),
		onSettled: invalidate,
	});
}
export function useRemovePlugin() {
	const invalidate = useInvalidatePlugins();
	return useMutation({ mutationFn: (id: string) => engine("DELETE", `plugins/${encodeURIComponent(id)}`), onSettled: invalidate });
}
/** v8 has no plugin enable route; the switch lives at plugins.configs.<id>.enabled. */
export function useSetPluginEnabled() {
	const invalidate = useInvalidatePlugins();
	return useMutation({ mutationFn: (v: { id: string; enabled: boolean }) => engine("PATCH", `config/plugins/configs/${encodeURIComponent(v.id)}`, { json: { enabled: v.enabled } }), onSettled: invalidate });
}
export function usePluginQuota(pluginId: string | undefined, authIndex: string | undefined) {
	return useQuery({
		queryKey: ["system", "plugins", "quota", pluginId, authIndex],
		queryFn: () => get<Record<string, unknown>>(`plugins/${encodeURIComponent(pluginId!)}/quota`, { auth_index: authIndex }),
		enabled: !!pluginId && !!authIndex,
		retry: false,
	});
}
export function useClearPluginQuota() {
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (v: { pluginId: string; authIndex: string }) => engine("DELETE", `plugins/${encodeURIComponent(v.pluginId)}/quota`, { query: { auth_index: v.authIndex } }),
		onSettled: (_r, _e, v) => qc.invalidateQueries({ queryKey: ["system", "plugins", "quota", v.pluginId] }),
	});
}

// ---------- versions ----------
export function useLatestVersion() {
	return useQuery({
		queryKey: ["system", "latest-version"],
		queryFn: async () => {
			const r = await get<Record<string, string>>("server/latest-version");
			return r["latest-version"] || r.latest_version || r.tag_name || r.version || null;
		},
		staleTime: 30 * 60_000,
		retry: false,
	});
}
