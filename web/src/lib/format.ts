const usd0 = new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 });
const int = new Intl.NumberFormat("en-US");
const compact = new Intl.NumberFormat("en-US", { notation: "compact", maximumFractionDigits: 1 });

export const money = (n: number) => usd0.format(n);
export const perMonth = (n: number) => `${usd0.format(n)}/mo`;
export const fmtInt = (n: number) => int.format(n);
export const fmtCompact = (n: number) => compact.format(n);

export function ago(ms: number | undefined | null, never = "No requests yet") {
	if (!ms) return never;
	const s = Math.round((Date.now() - ms) / 1000);
	if (s < 45) return "just now";
	const m = Math.round(s / 60);
	if (m < 60) return `${m} min ago`;
	const h = Math.round(m / 60);
	if (h < 24) return `${h} h ago`;
	const d = Math.round(h / 24);
	if (d < 30) return `${d} d ago`;
	return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function plural(n: number, one: string, many = `${one}s`) {
	return `${fmtInt(n)} ${n === 1 ? one : many}`;
}
