// Mirrors the /v8/neo/access contract (local spec: neoproxy-redux.md).
export type AccessKind = "subscription" | "apiKey" | "neopi" | "imported" | "file";
export type Health = "healthy" | "cooling" | "limited" | "error" | "expired" | "disabled";
export type RefreshMode = "auto" | "locked" | "neopi" | "none";
export type AccessAction = "reassign" | "setPlan" | "disable" | "remove" | "refresh" | "reimport" | "signInSeparately";

export interface Plan {
	id: string;
	name: string;
	monthlyUSD: number;
	source?: "detected" | "admin" | "default";
}

export interface AccessItem {
	id: string;
	provider: string;
	kind: AccessKind;
	label: string;
	ownerId: string;
	plan: Plan;
	health: Health;
	healthMessage?: string;
	refresh: RefreshMode;
	importedFrom?: { profile: string; credentialId: string; importedAt: number };
	neopiSource?: { id: string; name: string };
	models: string[];
	usage: { requests24h: number; failed24h: number; lastUsedAt?: number; buckets?: Array<{ t: number; ok: number; fail: number }> };
	createdAt?: number;
	actions: AccessAction[];
}

export interface ProviderInfo {
	id: string;
	name: string;
	vendor: string;
	hue: number;
	neutral?: boolean;
	methods: Array<"subscription" | "apiKey">;
	subscription?: { flow: "browser" | "device"; note?: string };
	apiKey?: { style: "openai" | "anthropic" | "gemini"; baseUrl: string; editableBaseUrl?: boolean; keyHint?: string; getKeyUrl?: string };
	plans?: Plan[];
}

export interface Person {
	id: string;
	handle: string;
	name: string;
	role?: string;
}

export interface AccessResponse {
	items: AccessItem[];
	providers: ProviderInfo[];
	people: Person[];
	totals: { monthlyUSD: number; items: number; healthy: number };
	/** True when composed client-side from older endpoints (no /v8/neo/access yet). */
	legacy?: boolean;
}

export interface Session {
	role: "admin" | "owner" | "member" | "user" | string;
	userId: string;
	handle: string;
	expiresAt: number;
}

export interface NeoPiSource {
	id: string;
	name: string;
	url?: string;
	state?: string;
	ownerId?: string;
	accounts?: number;
	lastError?: string;
}
