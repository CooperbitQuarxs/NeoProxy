import type { InferenceKey, Role, User, UserState } from "./api";

// Roles are explained once, in the same words everywhere they appear.
export const ROLE_COPY: Record<Role, { name: string; summary: string; can: string }> = {
	admin: {
		name: "Administrator",
		summary: "Runs this NeoProxy.",
		can: "Manages the pool, models, people and system settings, and sees everyone's activity.",
	},
	owner: {
		name: "Contributor",
		summary: "Shares their own provider access.",
		can: "Adds and manages their own accounts in the pool, and uses the models they are given.",
	},
	member: {
		name: "Member",
		summary: "Uses the models they are given.",
		can: "Signs in to see their models, keys and activity. Can't change the pool or other people.",
	},
	service: {
		name: "Service",
		summary: "An app or machine, not a person.",
		can: "Uses inference keys only and never signs in to this dashboard. Good for CI, bots and servers.",
	},
};
export const ROLE_ORDER: Role[] = ["member", "owner", "admin", "service"];

export const STATE_COPY: Record<UserState, { name: string; tone: "good" | "warn" | "bad" | "idle"; explain: string }> = {
	active: { name: "Active", tone: "good", explain: "Can sign in and use their keys." },
	invited: { name: "Invited", tone: "warn", explain: "Hasn't signed in or used a key yet. Issuing their first key makes them active." },
	suspended: { name: "Suspended", tone: "bad", explain: "Can't sign in, and every request with their keys is refused. Nothing is deleted." },
};

export const roleName = (r: string) => ROLE_COPY[r as Role]?.name ?? r;
export const displayName = (u: Pick<User, "name" | "handle"> | undefined) => (u ? u.name || u.handle : "Someone removed");

export function keyScope(k: InferenceKey, accountLabel: (id: string) => string) {
	if (!k.accountIds?.length) return "Works with every account they can use";
	if (k.accountIds.length === 1) return `Only uses ${accountLabel(k.accountIds[0])}`;
	return `Only uses ${k.accountIds.length} chosen accounts`;
}

export function fmtNumberLimit(n: number | null | undefined, unit: string) {
	return n ? `${new Intl.NumberFormat("en-US", { notation: n >= 100_000 ? "compact" : "standard", maximumFractionDigits: 1 }).format(n)} ${unit}` : "No limit";
}

/** Readable, typeable, 20 characters (well inside the 12 to 72 byte rule). */
export function generatePassword() {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
	const bytes = crypto.getRandomValues(new Uint8Array(20));
	const chars = Array.from(bytes, (b) => alphabet[b % alphabet.length]);
	return [chars.slice(0, 5), chars.slice(5, 10), chars.slice(10, 15), chars.slice(15, 20)].map((g) => g.join("")).join("-");
}

export function passwordProblem(pw: string) {
	const bytes = new TextEncoder().encode(pw).length;
	if (bytes < 12) return "Use at least 12 characters.";
	if (bytes > 72) return "Use at most 72 bytes; shorten it.";
	return undefined;
}
