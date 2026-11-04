import type { AccessItem, Person, ProviderInfo } from "../api/types";
import { ago, perMonth, plural } from "../lib/format";
import { providerColor, providerInfo } from "../lib/providers";
import { Avatar, StatusDot } from "../ui/components";
import { Icon } from "../ui/icons";
import { KIND_COPY, healthCopy, personName } from "./copy";

export function AccessList({ items, providers, people, onSelect }: { items: AccessItem[]; providers: ProviderInfo[]; people: Person[]; onSelect: (i: AccessItem) => void }) {
	const groups = new Map<string, AccessItem[]>();
	for (const i of [...items].sort((a, b) => b.plan.monthlyUSD - a.plan.monthlyUSD)) groups.set(i.provider, [...(groups.get(i.provider) ?? []), i]);
	return (
		<div className="access-groups">
			{[...groups.entries()].map(([provider, list]) => {
				const info = providerInfo(provider, providers);
				const sum = list.reduce((n, i) => n + i.plan.monthlyUSD, 0);
				return (
					<div key={provider} className="access-group panel">
						<header className="access-group-head">
							<span className="legend-swatch lg" style={{ background: providerColor(provider, info.hue) }} />
							<h3>{info.name}</h3>
							<span className="faint">{info.vendor}</span>
							<span className="grow" />
							<span className="muted num">
								{plural(list.length, "entry", "entries")}, {perMonth(sum)}
							</span>
						</header>
						<ul className="access-rows">
							{list.map((item) => {
								const h = healthCopy(item);
								const owner = personName(item.ownerId, people);
								return (
									<li key={item.id}>
										<button className="access-row" onClick={() => onSelect(item)}>
											<span className="access-who">
												<Avatar name={owner} size={30} />
												<span className="access-who-text">
													<strong>{owner}</strong>
													<span className="faint mono access-label">{item.label}</span>
												</span>
											</span>
											<span className="access-source">
												<span className={`legend-texture kind-${item.kind}`} style={{ ["--tile" as string]: providerColor(provider, info.hue) }} />
												{KIND_COPY[item.kind].noun}
												{item.refresh === "locked" && <Icon name="lock" size={14} className="faint" />}
											</span>
											<span className="access-plan">
												<span>{item.plan.name}</span>
												<span className="faint num">
													{perMonth(item.plan.monthlyUSD)}
													{item.plan.source === "default" ? " est." : ""}
												</span>
											</span>
											<span className="access-health">
												<StatusDot tone={h.tone} />
												{h.label}
											</span>
											<span className="access-usage">
												<Spark buckets={item.usage.buckets} />
												<span className="faint">{item.usage.requests24h ? `${item.usage.requests24h} today` : ago(item.usage.lastUsedAt)}</span>
											</span>
											<Icon name="chevronRight" size={16} className="faint" />
										</button>
									</li>
								);
							})}
						</ul>
					</div>
				);
			})}
		</div>
	);
}

export function Spark({ buckets }: { buckets?: Array<{ ok: number; fail: number }> }) {
	const data = (buckets ?? []).slice(-24);
	const max = Math.max(1, ...data.map((b) => b.ok + b.fail));
	return (
		<svg className="spark" width="72" height="20" viewBox="0 0 72 20" aria-hidden="true">
			{data.length === 0 ? (
				<line x1="0" y1="18.5" x2="72" y2="18.5" />
			) : (
				data.map((b, i) => {
					const w = 72 / data.length;
					const hOk = (b.ok / max) * 18;
					const hFail = (b.fail / max) * 18;
					return (
						<g key={i}>
							<rect x={i * w + 0.5} y={19 - hOk - hFail} width={Math.max(1, w - 1)} height={Math.max(hOk, b.ok ? 1 : 0)} className="spark-ok" />
							{b.fail > 0 && <rect x={i * w + 0.5} y={19 - hFail} width={Math.max(1, w - 1)} height={hFail} className="spark-fail" />}
							{b.ok + b.fail === 0 && <rect x={i * w + 0.5} y={18} width={Math.max(1, w - 1)} height={1} className="spark-idle" />}
						</g>
					);
				})
			)}
		</svg>
	);
}
