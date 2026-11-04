import { useMemo, useState } from "react";
import type { PageProps } from "../app/App";
import { useAccess, useProviders } from "../api/queries";
import type { AccessItem, AccessKind } from "../api/types";
import { fmtInt, perMonth, plural } from "../lib/format";
import { providerColor, providerInfo } from "../lib/providers";
import { Button, EmptyState, Notice, PageHeader, Section, Skeleton } from "../ui/components";
import { Mosaic, GhostMosaic } from "./Mosaic";
import { AccessList } from "./AccessList";
import { AccessSheet } from "./AccessSheet";
import { AddAccessDialog } from "./AddAccess";
import { KIND_COPY, healthCopy, personName } from "./copy";

export function PoolPage({ session }: PageProps) {
	const access = useAccess();
	const providers = useProviders();
	const [selectedId, setSelectedId] = useState<string>();
	const [adding, setAdding] = useState<{ open: boolean; method?: AccessKind; provider?: string }>({ open: false });
	const admin = session.role === "admin";

	const data = access.data;
	const items = useMemo(() => {
		const all = data?.items ?? [];
		return admin ? all : all.filter((i) => i.ownerId === session.userId);
	}, [data, admin, session.userId]);
	const people = data?.people ?? [];
	const selected = items.find((i) => i.id === selectedId);
	const total = items.reduce((n, i) => n + i.plan.monthlyUSD, 0);
	const contributors = new Set(items.map((i) => i.ownerId).filter(Boolean)).size;
	const models = new Set(items.flatMap((i) => i.models)).size;
	const attention = items.filter((i) => i.health !== "healthy" || i.refresh === "locked");
	const kindsPresent = new Set(items.map((i) => i.kind));
	const providerTotals = new Map<string, number>();
	for (const i of items) providerTotals.set(i.provider, (providerTotals.get(i.provider) ?? 0) + i.plan.monthlyUSD);
	const providersPresent = [...providerTotals.keys()].sort((a, b) => providerTotals.get(b)! - providerTotals.get(a)!);

	const openAdd = (method?: AccessKind, provider?: string) => setAdding({ open: true, method, provider });

	return (
		<>
			<PageHeader
				title={admin ? "Pool" : "Your contributions"}
				lede={
					access.isPending ? (
						<Skeleton w={420} h={18} />
					) : items.length ? (
						<>
							{perMonth(total)} of provider access from {plural(contributors, "contributor")}, unlocking {plural(models, "model")}.
						</>
					) : admin ? (
						"Everything NeoProxy can use to answer requests lives here: signed-in accounts, API keys and NeoPi connections."
					) : (
						"Provider access you add here is pooled with the rest of your organization."
					)
				}
				actions={
					items.length > 0 && (
						<Button variant="primary" icon="plus" onClick={() => openAdd()}>
							Add provider access
						</Button>
					)
				}
			/>

			{access.isError && (
				<Notice tone="bad" title="Couldn't load the pool">
					{(access.error as Error).message}. NeoProxy may be restarting; this page retries on its own.
				</Notice>
			)}

			{access.isPending ? (
				<div className="panel mosaic-panel">
					<Skeleton h={360} r={10} />
				</div>
			) : items.length === 0 ? (
				<div className="panel pool-empty">
					<GhostMosaic />
					<EmptyState
						title={admin ? "Your pool is empty" : "You haven't contributed anything yet"}
						action={
							<Button variant="primary" size="lg" icon="plus" onClick={() => openAdd()}>
								Add provider access
							</Button>
						}
					>
						Add provider access to start serving models. Pick whichever way suits the account you have:
					</EmptyState>
					<ul className="ways">
						{(Object.keys(KIND_COPY) as AccessKind[]).map((k) => (
							<li key={k}>
								<button className="way" onClick={() => openAdd(k)}>
									<span className={`way-swatch kind-${k}`} aria-hidden="true" />
									<span className="way-title">{KIND_COPY[k].title}</span>
									<span className="way-text">{KIND_COPY[k].summary}</span>
								</button>
							</li>
						))}
					</ul>
				</div>
			) : (
				<div className="panel mosaic-panel">
					<Mosaic items={items} providers={providers} people={people} onSelect={(i) => setSelectedId(i.id)} selectedId={selectedId} />
					<footer className="legend" aria-label="How to read the pool">
						<p className="legend-lead">Each tile is one piece of access. Bigger tiles are worth more per month.</p>
						<div className="legend-groups">
							<ul className="legend-list" aria-label="Providers">
								{providersPresent.map((p) => {
									const info = providerInfo(p, providers);
									const sum = items.filter((i) => i.provider === p).reduce((n, i) => n + i.plan.monthlyUSD, 0);
									return (
										<li key={p}>
											<span className="legend-swatch" style={{ background: providerColor(p, info.hue) }} />
											{info.name}
											<span className="faint num">{perMonth(sum)}</span>
										</li>
									);
								})}
							</ul>
							<ul className="legend-list" aria-label="Sources">
								{(["subscription", "apiKey", "neopi", "imported"] as AccessKind[]).map((k) => (
									<li key={k} className={kindsPresent.has(k) ? undefined : "is-absent"}>
										<span className={`legend-texture kind-${k}`} />
										{KIND_COPY[k].noun}
									</li>
								))}
							</ul>
						</div>
					</footer>
				</div>
			)}

			{attention.length > 0 && (
				<Section title="Needs attention" description="Access that isn't serving requests normally, or that will stop on its own.">
					<div className="attention">
						{attention.map((item) => {
							const h = healthCopy(item);
							const info = providerInfo(item.provider, providers);
							return (
								<button key={item.id} className={`attention-item tone-${item.refresh === "locked" && h.tone === "good" ? "warn" : h.tone}`} onClick={() => setSelectedId(item.id)}>
									<span className="legend-swatch" style={{ background: providerColor(item.provider, info.hue) }} />
									<span className="grow">
										<strong>
											{info.name}, {personName(item.ownerId, people)}
										</strong>
										<span className="muted">{item.refresh === "locked" && item.health === "healthy" ? "Imported with refresh locked; it will expire unless re-imported or signed in separately." : h.explain}</span>
									</span>
									<span className="attention-label">{item.refresh === "locked" && item.health === "healthy" ? "Will expire" : h.label}</span>
								</button>
							);
						})}
					</div>
				</Section>
			)}

			{items.length > 0 && (
				<Section title="All access" description={`${fmtInt(items.length)} ${items.length === 1 ? "entry" : "entries"}, grouped by provider. Select one to change its contributor or plan, or to remove it.`}>
					<AccessList items={items} providers={providers} people={people} onSelect={(i: AccessItem) => setSelectedId(i.id)} />
				</Section>
			)}

			{data?.legacy && admin && items.length > 0 && (
				<p className="faint legacy-note">
					This server predates the unified access API, so plans and sources are estimated from older endpoints. Updating NeoProxy fills them in exactly.
				</p>
			)}

			<AccessSheet item={selected} people={people} providers={providers} admin={admin} onClose={() => setSelectedId(undefined)} onSignInSeparately={(p) => openAdd("subscription", p)} />
			<AddAccessDialog
				open={adding.open}
				initialMethod={adding.method}
				initialProvider={adding.provider}
				onOpenChange={(open) => setAdding((a) => ({ ...a, open }))}
				session={session}
				people={people}
				legacy={data?.legacy}
			/>
		</>
	);
}
