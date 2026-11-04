import type { AccessItem, AccessKind, Person } from "../api/types";

// One vocabulary for the whole dashboard. Each way of adding provider access
// has a name, a one-line explanation, and what NeoProxy does with it.
export const KIND_COPY: Record<AccessKind, { noun: string; title: string; summary: string; detail: string; goodFor: string }> = {
	subscription: {
		noun: "Signed-in account",
		title: "Sign in with an account",
		summary: "Use a subscription like ChatGPT Pro or Claude Max.",
		detail: "You sign in on the provider's own page. NeoProxy keeps the sign-in and refreshes it automatically, so it keeps working.",
		goodFor: "Monthly subscriptions people already pay for.",
	},
	apiKey: {
		noun: "API key",
		title: "Paste an API key",
		summary: "Add a pay-as-you-go key from OpenAI, Anthropic, OpenRouter and others.",
		detail: "NeoProxy checks the key by listing the provider's models, then stores it. Usage is billed by the provider to whoever owns the key.",
		goodFor: "Usage-billed accounts and OpenAI-compatible servers.",
	},
	neopi: {
		noun: "Shared from NeoPi",
		title: "Share from a NeoPi",
		summary: "Borrow the accounts already signed in to a running NeoPi.",
		detail: "Nothing is copied. NeoPi keeps the credentials and refreshes them; NeoProxy sends each request through NeoPi's gateway. If NeoPi is offline, these accounts pause.",
		goodFor: "A NeoPi you already use every day, on this machine or the network.",
	},
	imported: {
		noun: "Imported from NeoPi",
		title: "Import from NeoPi",
		summary: "Copy credentials out of a NeoPi vault on this computer.",
		detail: "API keys copy over completely. Signed-in accounts are copied with refresh locked: NeoProxy never uses their refresh token, so NeoPi's own sign-in keeps working. They stop when the copied sign-in expires; re-import or sign in separately.",
		goodFor: "Moving NeoPi's API keys over in one step.",
	},
	file: {
		noun: "Credential file",
		title: "Upload a credential file",
		summary: "Add an auth file saved by Codex CLI, Claude Code or another proxy.",
		detail: "NeoProxy reads the file, recognizes the provider and plan, and keeps it refreshed like a signed-in account.",
		goodFor: "Accounts already signed in on another machine.",
	},
};

export function healthCopy(item: AccessItem): { label: string; tone: "good" | "warn" | "bad" | "idle"; explain: string } {
	switch (item.health) {
		case "healthy":
			return { label: "Working", tone: "good", explain: "Serving requests normally." };
		case "cooling":
			return { label: "Cooling down", tone: "warn", explain: item.healthMessage || "Recently rate limited by the provider. NeoProxy is giving it a short rest and uses other access meanwhile." };
		case "limited":
			return { label: "Limit reached", tone: "warn", explain: item.healthMessage || "Reached a usage limit. It comes back when the provider's window resets." };
		case "expired":
			return {
				label: "Sign-in expired",
				tone: "bad",
				explain: item.refresh === "locked" ? "The copied sign-in expired. Re-import it from NeoPi, or sign in separately to give NeoProxy its own." : item.healthMessage || "The provider no longer accepts this sign-in. Sign in again.",
			};
		case "error":
			return { label: "Failing", tone: "bad", explain: item.healthMessage || "Recent requests failed. Check the details below." };
		case "disabled":
			return { label: "Paused", tone: "idle", explain: "Turned off by an administrator. It stays in the pool but serves nothing." };
	}
}

export function personName(id: string, people: Person[]) {
	if (!id) return "Unassigned";
	const p = people.find((x) => x.id === id);
	return p?.name || p?.handle || "Unknown person";
}

export const REFRESH_COPY: Record<AccessItem["refresh"], string> = {
	auto: "NeoProxy refreshes this sign-in automatically.",
	locked: "Refresh locked. NeoProxy never uses this credential's refresh token, so NeoPi's copy keeps working.",
	neopi: "NeoPi refreshes this account; NeoProxy only borrows it.",
	none: "API keys don't expire on a schedule.",
};
