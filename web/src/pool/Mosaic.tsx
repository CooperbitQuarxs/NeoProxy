import { hierarchy, treemap, treemapSquarify } from "d3-hierarchy";
import { motion, useReducedMotion } from "motion/react";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { AccessItem, Person, ProviderInfo } from "../api/types";
import { perMonth, ago, plural } from "../lib/format";
import { providerColor, providerInfo } from "../lib/providers";
import { Icon } from "../ui/icons";
import { Tip, cx } from "../ui/components";
import { KIND_COPY, healthCopy, personName } from "./copy";

type Leaf = { item: AccessItem; value: number };
type Group = { provider: string; children: Leaf[] };

// A free plan still occupies visible space so it can be found and hovered.
const MIN_VALUE = 6;

export function Mosaic({
	items,
	providers,
	people,
	onSelect,
	selectedId,
}: {
	items: AccessItem[];
	providers: ProviderInfo[];
	people: Person[];
	onSelect: (item: AccessItem) => void;
	selectedId?: string;
}) {
	const ref = useRef<HTMLDivElement>(null);
	const [size, setSize] = useState<{ w: number; h: number } | null>(null);
	const reduce = useReducedMotion();
	const firstPaint = useRef(true);

	useLayoutEffect(() => {
		const el = ref.current;
		if (!el) return;
		const ro = new ResizeObserver(([entry]) => {
			const w = Math.round(entry.contentRect.width);
			const h = Math.round(Math.min(560, Math.max(300, w * 0.4)));
			setSize((s) => (s && s.w === w && s.h === h ? s : { w, h }));
		});
		ro.observe(el);
		return () => ro.disconnect();
	}, []);

	const pulsing = usePulses(items);

	const tiles = useMemo(() => {
		if (!size) return [];
		const groups = new Map<string, Group>();
		for (const item of items) {
			const g = groups.get(item.provider) ?? { provider: item.provider, children: [] };
			g.children.push({ item, value: Math.max(MIN_VALUE, item.plan.monthlyUSD) });
			groups.set(item.provider, g);
		}
		const root = hierarchy<{ children?: Array<Group | Leaf> } | Group | Leaf>({ children: [...groups.values()] }, (d) => ("children" in d ? (d.children as Array<Group | Leaf>) : undefined))
			.sum((d) => ("value" in d ? d.value : 0))
			.sort((a, b) => (b.value ?? 0) - (a.value ?? 0));
		treemap<typeof root.data>()
			.size([size.w, size.h])
			.tile(treemapSquarify.ratio(1.25))
			.paddingInner((node) => (node.depth === 0 ? 8 : 3))
			.round(true)(root);
		return root.leaves().map((leaf) => {
			const l = leaf as typeof leaf & { x0: number; y0: number; x1: number; y1: number };
			const data = leaf.data as Leaf;
			const siblings = leaf.parent?.children ?? [];
			return { item: data.item, x: l.x0, y: l.y0, w: l.x1 - l.x0, h: l.y1 - l.y0, lead: siblings[0] === leaf };
		});
	}, [items, size]);

	useEffect(() => {
		if (tiles.length) {
			const t = setTimeout(() => (firstPaint.current = false), 900);
			return () => clearTimeout(t);
		}
	}, [tiles.length]);

	return (
		<div ref={ref} className="mosaic" style={{ height: size?.h ?? 360 }} role="list" aria-label="Provider access in the pool">
			{tiles.map(({ item, x, y, w, h, lead }, i) => {
				const info = providerInfo(item.provider, providers);
				const owner = personName(item.ownerId, people);
				const delay = firstPaint.current && !reduce ? 0.04 * i : 0;
				return (
					<Tip key={item.id} side="top" label={<TileCard item={item} info={info} owner={owner} />}>
						<motion.button
							role="listitem"
							type="button"
							className={cx(
								"tile",
								`kind-${item.kind}`,
								`health-${item.health}`,
								item.refresh === "locked" && "is-locked",
								selectedId === item.id && "is-selected",
								pulsing.has(item.id) && "is-pulsing",
								w < 92 || h < 60 ? "is-small" : w < 170 || h < 96 ? "is-medium" : "is-large",
							)}
							style={{ ["--tile" as string]: providerColor(item.provider, info.hue) }}
							initial={reduce ? false : { opacity: 0, scale: 0.9, x, y, width: w, height: h }}
							animate={{ opacity: 1, scale: 1, x, y, width: w, height: h }}
							exit={{ opacity: 0, scale: 0.85 }}
							transition={{ type: "spring", stiffness: 260, damping: 30, mass: 0.9, delay, opacity: { duration: 0.35, delay } }}
							onClick={() => onSelect(item)}
							aria-label={`${info.name}, ${KIND_COPY[item.kind].noun}, contributed by ${owner}, ${item.plan.name}, ${perMonth(item.plan.monthlyUSD)}, ${healthCopy(item).label}`}
						>
							<span className="tile-head">
								{lead && <span className="tile-provider">{info.name}</span>}
								<span className="tile-glyphs">
									{item.kind === "neopi" && <Icon name="link" size={14} />}
									{item.refresh === "locked" && <Icon name="lock" size={14} />}
									{item.health !== "healthy" && <Icon name="alert" size={14} className="tile-alert" />}
								</span>
							</span>
							<span className="tile-body">
								<span className="tile-owner">{owner}</span>
								<span className="tile-plan">{item.plan.name}</span>
							</span>
							<span className="tile-value num">{perMonth(item.plan.monthlyUSD)}</span>
						</motion.button>
					</Tip>
				);
			})}
		</div>
	);
}

function TileCard({ item, info, owner }: { item: AccessItem; info: ProviderInfo; owner: string }) {
	const health = healthCopy(item);
	return (
		<span className="tilecard">
			<span className="tilecard-head">
				<span className="tilecard-swatch" style={{ background: providerColor(item.provider, info.hue) }} />
				<strong>{info.name}</strong>
				<span className="tilecard-vendor">{info.vendor}</span>
			</span>
			<span className="tilecard-row">
				<span>Contributed by</span>
				<strong>{owner}</strong>
			</span>
			<span className="tilecard-row">
				<span>Source</span>
				<strong>{KIND_COPY[item.kind].noun}</strong>
			</span>
			<span className="tilecard-row">
				<span>Plan</span>
				<strong>
					{item.plan.name}, {perMonth(item.plan.monthlyUSD)}
					{item.plan.source === "default" ? " (estimate)" : ""}
				</strong>
			</span>
			<span className="tilecard-row">
				<span>Status</span>
				<strong>{health.label}</strong>
			</span>
			<span className="tilecard-row">
				<span>Unlocks</span>
				<strong>{plural(item.models.length, "model")}</strong>
			</span>
			<span className="tilecard-row">
				<span>Last used</span>
				<strong>{ago(item.usage.lastUsedAt)}</strong>
			</span>
			{item.refresh === "locked" && <span className="tilecard-note">Imported from NeoPi with refresh locked. It stops working when its sign-in expires unless you re-import it or sign in separately.</span>}
			<span className="tilecard-hint">Click for details and actions</span>
		</span>
	);
}

// A tile glows briefly when its request count rises between polls.
function usePulses(items: AccessItem[]) {
	const last = useRef(new Map<string, number>());
	const [pulsing, setPulsing] = useState<Set<string>>(new Set());
	useEffect(() => {
		const fresh: string[] = [];
		for (const item of items) {
			const prev = last.current.get(item.id);
			const now = item.usage.requests24h;
			if (prev !== undefined && now > prev) fresh.push(item.id);
			last.current.set(item.id, now);
		}
		if (!fresh.length) return;
		setPulsing((s) => new Set([...s, ...fresh]));
		const t = setTimeout(() => setPulsing((s) => new Set([...s].filter((id) => !fresh.includes(id)))), 1600);
		return () => clearTimeout(t);
	}, [items]);
	return pulsing;
}

// Shown before any access exists: the shape of a pool, waiting to be filled.
export function GhostMosaic() {
	const cells: Array<[number, number, number, number]> = [
		[1, 1, 6, 5],
		[7, 1, 4, 3],
		[11, 1, 2, 3],
		[7, 4, 3, 2],
		[10, 4, 3, 2],
		[1, 6, 3, 2],
		[4, 6, 3, 2],
		[7, 6, 6, 2],
	];
	return (
		<div className="ghost-mosaic" aria-hidden="true">
			{cells.map(([c, r, w, h], i) => (
				<span key={i} style={{ gridColumn: `${c} / span ${w}`, gridRow: `${r} / span ${h}` }} />
			))}
		</div>
	);
}
