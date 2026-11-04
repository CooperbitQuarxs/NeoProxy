// The request ledger over a time range. The API returns newest first, at most
// 200 a page, and pages with `before` (an exclusive timestamp), so a range is
// read page by page until it is covered or `max` records are loaded. Overview
// and Activity both read it this way, so their numbers describe the same requests.

import { neo } from "./client.js";

export const PAGE = 200;
export const MAX = 5000;
export const DAY = 86_400_000;
export const RANGES = [
	{ id: "1h", ms: 3_600_000, label: "Last hour", short: "last hour" },
	{ id: "24h", ms: DAY, label: "Last 24 hours", short: "last 24 h" },
	{ id: "7d", ms: 7 * DAY, label: "Last 7 days", short: "last 7 days" },
];

// Every request since `since` (ms), newest first. `complete` is false when
// `max` was reached first; `oldest` is then the earliest time actually loaded.
export async function requestsSince(since, { userId, max = MAX } = {}) {
	const rows = [];
	const seen = new Set();
	let before;
	for (;;) {
		const page = await neo.requests({ limit: PAGE, userId: userId || undefined, before });
		let added = 0;
		for (const r of page) {
			if (r.at < since) return { rows, complete: true, oldest: since };
			if (seen.has(r.id)) continue;
			seen.add(r.id);
			rows.push(r);
			added++;
		}
		if (page.length < PAGE) return { rows, complete: true, oldest: since };
		if (rows.length >= max) return { rows, complete: false, oldest: rows[rows.length - 1].at };
		// Ask again from the last millisecond so requests sharing it are not lost;
		// step past it when a whole page held nothing new.
		const last = page[page.length - 1].at;
		before = added ? last + 1 : last;
	}
}

// Fold the newest page into a loaded range: new and changed records replace
// old ones and anything now older than `since` drops off. Returns null when the
// page does not reach back to what was loaded (more than a page arrived), so the
// caller reloads the range instead.
export function mergeLatest(rows, latest, since) {
	const known = new Set(rows.map((r) => r.id));
	if (latest.length >= PAGE && !latest.some((r) => known.has(r.id))) return null;
	const byId = new Map(rows.map((r) => [r.id, r]));
	for (const r of latest) byId.set(r.id, r);
	return [...byId.values()].filter((r) => r.at >= since).sort((a, b) => b.at - a.at || (a.id < b.id ? 1 : -1));
}
