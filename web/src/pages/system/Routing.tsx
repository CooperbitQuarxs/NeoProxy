import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ApiError } from "../../api/client";
import { fmtInt, plural } from "../../lib/format";
import { Button, Dialog, EmptyState, Field, Input, Notice, SearchInput, Skeleton, Switch, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useToast } from "../../ui/toast";
import { useEngineConfig, useEngineCredentials, useModelDefinitions, usePatchConfig, useResetCooldowns, type EngineConfig, type ModelDefinition } from "./api";
import { ConfirmDialog, SubHead, useSaveShortcut, useUnsavedGuard } from "./shared";

type Strategy = "round-robin" | "weighted-round-robin" | "fill-first";
type Draft = {
	strategy: Strategy;
	requestRetry: string;
	maxRetryCredentials: string;
	maxRetryInterval: string;
	transientCooldown: string;
	sessionAffinity: boolean;
	forceModelPrefix: boolean;
	cooling: boolean;
	keepCooldowns: boolean;
};
type NumKey = "requestRetry" | "maxRetryCredentials" | "maxRetryInterval" | "transientCooldown";
type BoolKey = "sessionAffinity" | "forceModelPrefix" | "cooling" | "keepCooldowns";

function fromConfig(c: EngineConfig | undefined): Draft {
	const r = c?.routing ?? {};
	return {
		strategy: (r.strategy as Strategy) || "round-robin",
		requestRetry: String(r.retry?.["request-retry"] ?? 0),
		maxRetryCredentials: String(r.retry?.["max-retry-credentials"] ?? 0),
		maxRetryInterval: String(r.retry?.["max-retry-interval"] ?? 0),
		transientCooldown: String(r.cooldown?.["transient-error-cooldown-seconds"] ?? 0),
		sessionAffinity: !!r["session-affinity"],
		forceModelPrefix: !!r["force-model-prefix"],
		cooling: !r.cooldown?.["disable-cooling"],
		keepCooldowns: !!r.cooldown?.["save-cooldown-status"],
	};
}

const STRATEGIES: Array<{ id: Strategy; name: string; summary: string; detail: string }> = [
	{ id: "round-robin", name: "Round robin", summary: "Accounts take turns, one request each.", detail: "Spreads load evenly, so every subscription is used about the same and none hits its limit early." },
	{ id: "weighted-round-robin", name: "Weighted", summary: "Accounts take turns in proportion to their weight.", detail: "A credential with weight 3 gets three requests for every one a weight-1 credential gets. Weights are set per credential; the default is 1." },
	{ id: "fill-first", name: "Fill first", summary: "Use one account until it's limited, then the next.", detail: "Keeps one account busy and the rest fresh. Good when one subscription is much bigger, or to keep a backup untouched." },
];

// Which account (0..2) serves request 1..6 under each strategy.
const ORDER: Record<Strategy, number[]> = {
	"round-robin": [0, 1, 2, 0, 1, 2],
	"weighted-round-robin": [0, 1, 0, 2, 0, 1],
	"fill-first": [0, 0, 0, 0, 0, 0],
};

function StrategyArt({ strategy, active }: { strategy: Strategy; active: boolean }) {
	const reduce = useReducedMotion();
	const order = ORDER[strategy];
	const stacks = [0, 0, 0];
	const pips = order.map((col, i) => ({ col, row: stacks[col]++, i }));
	return (
		<svg className="strat-art" viewBox="0 0 132 64" aria-hidden="true">
			{[0, 1, 2].map((c) => (
				<g key={c}>
					<rect x={8 + c * 42} y={4} width={32} height={50} rx={6} className="strat-col" />
					<text x={24 + c * 42} y={62} className="strat-label">
						{"ABC"[c]}
					</text>
				</g>
			))}
			{pips.map((p) => (
				<motion.rect
					key={`${strategy}-${p.i}-${active}`}
					x={13 + p.col * 42}
					y={46 - p.row * 7.2}
					width={22}
					height={5}
					rx={2}
					className="strat-pip"
					initial={reduce || !active ? false : { opacity: 0, y: -14 }}
					animate={{ opacity: 1, y: 0 }}
					transition={{ delay: p.i * 0.09, type: "spring", stiffness: 420, damping: 26 }}
				/>
			))}
			{strategy === "weighted-round-robin" &&
				["×3", "×2", "×1"].map((w, c) => (
					<text key={w} x={24 + c * 42} y={14} className="strat-weight">
						{w}
					</text>
				))}
		</svg>
	);
}

const intOk = (s: string, min: number) => /^-?\d+$/.test(s.trim()) && Number(s) >= min;

type NumField = { key: NumKey; label: string; min: number; unit?: string; explain: (n: number) => string; path: [string, string] };
const NUMBERS: NumField[] = [
	{
		key: "requestRetry",
		label: "Extra retry rounds",
		min: 0,
		path: ["retry", "request-retry"],
		explain: (n) =>
			n === 0 ? "When every account has been tried once and all failed, the app gets the error straight away." : `After every account has failed once, NeoProxy goes around ${n === 1 ? "one more time" : `${n} more times`} before the app sees an error. Fewer errors, slower failures.`,
	},
	{
		key: "maxRetryCredentials",
		label: "Accounts tried per round",
		min: 0,
		path: ["retry", "max-retry-credentials"],
		explain: (n) => (n === 0 ? "A request may try every available account of that provider before giving up on the round." : `A request tries at most ${plural(n, "account")} per round, then moves on or fails.`),
	},
	{
		key: "maxRetryInterval",
		label: "Longest wait for a cooled-down account",
		min: 0,
		unit: "seconds",
		path: ["retry", "max-retry-interval"],
		explain: (n) => (n === 0 ? "Never waits. If every account is cooling down, the request fails immediately." : `If every account is cooling down, a request may wait up to ${n} s for one to come back before failing.`),
	},
	{
		key: "transientCooldown",
		label: "Rest after a temporary error",
		min: -1,
		unit: "seconds",
		path: ["cooldown", "transient-error-cooldown-seconds"],
		explain: (n) =>
			n === -1
				? "Temporary upstream errors (timeouts, 502, 503) don't rest the account; it's tried again right away."
				: n === 0
					? "Uses the engine's built-in rest after temporary upstream errors (timeouts, 502, 503). Set -1 to never rest."
					: `After a temporary upstream error (timeouts, 502, 503) the account rests ${n} s before it's tried again.`,
	},
];

type BoolField = { key: BoolKey; label: string; on: string; off: string };
const SWITCHES: BoolField[] = [
	{
		key: "cooling",
		label: "Rest accounts that fail",
		on: "An account that hits a rate limit or keeps failing is skipped for a while, then tried again.",
		off: "Failing accounts keep getting requests. Expect repeated errors while a provider is limiting you.",
	},
	{
		key: "keepCooldowns",
		label: "Remember rests across restarts",
		on: "Resting accounts stay resting after NeoProxy restarts.",
		off: "A restart clears every rest, so all accounts are tried again immediately.",
	},
	{
		key: "sessionAffinity",
		label: "Keep a conversation on one account",
		on: "Requests from the same chat stay on the same account, so the provider's prompt cache keeps working. It moves only if that account fails.",
		off: "Each request is routed on its own, which can split a conversation across accounts and lose prompt caching.",
	},
	{
		key: "forceModelPrefix",
		label: "Prefixed credentials need the prefix",
		on: "Credentials with a prefix (for example team-a/) only serve requests that name the prefix.",
		off: "Requests without a prefix may also use prefixed credentials.",
	},
];

function describe(key: keyof Draft, v: Draft[keyof Draft]): string {
	if (key === "strategy") return STRATEGIES.find((s) => s.id === v)?.name ?? String(v);
	if (typeof v === "boolean") return v ? "on" : "off";
	return String(v);
}
const LABELS: Record<keyof Draft, string> = {
	strategy: "Strategy",
	...(Object.fromEntries(NUMBERS.map((n) => [n.key, n.label])) as Record<NumKey, string>),
	...(Object.fromEntries(SWITCHES.map((s) => [s.key, s.label])) as Record<BoolKey, string>),
};

function buildPatch(changed: Array<keyof Draft>, d: Draft) {
	const routing: Record<string, Record<string, unknown> | unknown> = {};
	const put = (group: string, key: string, value: unknown) => {
		routing[group] = { ...((routing[group] as Record<string, unknown>) ?? {}), [key]: value };
	};
	for (const k of changed) {
		if (k === "strategy") routing.strategy = d.strategy;
		else if (k === "sessionAffinity") routing["session-affinity"] = d.sessionAffinity;
		else if (k === "forceModelPrefix") routing["force-model-prefix"] = d.forceModelPrefix;
		else if (k === "cooling") put("cooldown", "disable-cooling", !d.cooling);
		else if (k === "keepCooldowns") put("cooldown", "save-cooldown-status", d.keepCooldowns);
		else {
			const f = NUMBERS.find((n) => n.key === k)!;
			put(f.path[0], f.path[1], Number(d[f.key]));
		}
	}
	return { routing };
}

export function Routing() {
	const cfg = useEngineConfig();
	const patch = usePatchConfig();
	const toast = useToast();
	const [baseline, setBaseline] = useState<Draft>();
	const [draft, setDraft] = useState<Draft>();
	const [conflict, setConflict] = useState(false);

	useEffect(() => {
		if (!cfg.data) return;
		const next = fromConfig(cfg.data.config);
		setBaseline((old) => {
			setDraft((d) => (!d || !old || JSON.stringify(d) === JSON.stringify(old) ? next : d));
			return next;
		});
	}, [cfg.data]);

	const changed = useMemo(() => (draft && baseline ? (Object.keys(draft) as Array<keyof Draft>).filter((k) => draft[k] !== baseline[k]) : []), [draft, baseline]);
	const invalid = draft ? NUMBERS.filter((n) => !intOk(draft[n.key], n.min)) : [];
	const dirty = changed.length > 0;
	const guard = useUnsavedGuard(dirty, "routing");

	const save = async () => {
		if (!draft || !dirty || invalid.length || patch.isPending) return;
		setConflict(false);
		try {
			await patch.mutateAsync({ patch: buildPatch(changed, draft), etag: cfg.data?.etag });
			setBaseline(draft);
			toast("Routing saved. New requests use it right away.");
		} catch (e) {
			if (e instanceof ApiError && e.status === 412) setConflict(true);
			else toast(`Couldn't save routing: ${(e as Error).message}`, "bad");
		}
	};
	useSaveShortcut(save, dirty);
	const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => (d ? { ...d, [k]: v } : d));

	return (
		<div className="stack-lg">
			<SubHead
				title="Routing"
				lede="When a provider has more than one account in the pool, this decides which account answers each request, and what happens when one fails. Virtual models and people's limits are decided first, on Models and People; this is the layer underneath."
			/>

			{cfg.isError && (
				<Notice tone="bad" title="Couldn't load routing settings">
					{(cfg.error as Error).message}. Reload the page; if it keeps failing, check that NeoProxy is running.
				</Notice>
			)}
			{conflict && (
				<Notice
					tone="warn"
					title="Someone else saved the configuration"
					action={
						<Button
							size="sm"
							onClick={() => {
								setConflict(false);
								setBaseline(undefined);
								setDraft(undefined);
								cfg.refetch();
							}}
						>
							Load their version
						</Button>
					}
				>
					The settings changed on the server after this page loaded, so your save was not applied. Load the current version, then make your change again.
				</Notice>
			)}

			<section className="stack" aria-labelledby="rt-strategy">
				<div>
					<h3 className="sys-h3" id="rt-strategy">
						How accounts share the work
					</h3>
					<p className="muted sys-h3-desc">Pick how requests are spread across accounts of the same provider. Each picture shows where six requests in a row would go with three accounts.</p>
				</div>
				<div className="strats" role="radiogroup" aria-labelledby="rt-strategy">
					{STRATEGIES.map((s) => {
						const on = draft?.strategy === s.id;
						return (
							<button
								key={s.id}
								type="button"
								role="radio"
								aria-checked={on}
								className={cx("strat panel", on && "is-on")}
								onClick={() => set("strategy", s.id)}
								disabled={!draft}
							>
								<StrategyArt strategy={s.id} active={on} />
								<span className="strat-name">
									{s.name}
									{on && <Icon name="check" size={16} className="strat-check" />}
								</span>
								<span className="strat-summary">{s.summary}</span>
								<span className="strat-detail faint">{s.detail}</span>
							</button>
						);
					})}
				</div>
			</section>

			<section className="stack" aria-labelledby="rt-retry">
				<div>
					<h3 className="sys-h3" id="rt-retry">
						Retries and rests
					</h3>
					<p className="muted sys-h3-desc">What NeoProxy does when an account fails. The line under each setting says what your current value means.</p>
				</div>
				<div className="panel rt-grid">
					{NUMBERS.map((n) => {
						const v = draft?.[n.key] ?? "";
						const ok = draft ? intOk(v, n.min) : true;
						return (
							<Field
								key={n.key}
								label={n.label}
								htmlFor={`rt-${n.key}`}
								error={!ok ? `Enter a whole number${n.min < 0 ? " of -1 or more" : " of 0 or more"}.` : undefined}
								hint={draft ? n.explain(Number(v)) : undefined}
							>
								{draft ? (
									<div className="rt-num">
										<Input id={`rt-${n.key}`} mono inputMode="numeric" value={v} onChange={(e) => set(n.key, e.target.value)} />
										{n.unit && <span className="faint">{n.unit}</span>}
									</div>
								) : (
									<Skeleton h={38} r={10} />
								)}
							</Field>
						);
					})}
				</div>
				<ul className="panel rt-switches">
					{SWITCHES.map((s) => (
						<li key={s.key}>
							<div className="grow">
								<label className="rt-switch-label" htmlFor={`rt-${s.key}`}>
									{s.label}
								</label>
								<p className="faint rt-switch-text">{draft ? (draft[s.key] ? s.on : s.off) : <Skeleton w="70%" h={12} />}</p>
							</div>
							<Switch id={`rt-${s.key}`} checked={!!draft?.[s.key]} disabled={!draft} onCheckedChange={(v) => set(s.key, v)} />
						</li>
					))}
				</ul>
			</section>

			<Cooldowns />
			<ModelDefinitions />

			<AnimatePresence>
				{dirty && draft && baseline && (
					<motion.div className="changebar" initial={{ y: 24, opacity: 0 }} animate={{ y: 0, opacity: 1 }} exit={{ y: 24, opacity: 0 }} transition={{ type: "spring", stiffness: 480, damping: 38 }} role="region" aria-label="Unsaved routing changes">
						<div className="grow">
							<p className="changebar-title">{plural(changed.length, "unsaved change")}</p>
							<ul className="changebar-list">
								{changed.map((k) => (
									<li key={k}>
										{LABELS[k]}: <span className="faint">{describe(k, baseline[k])}</span> <span aria-hidden="true">to</span> <strong>{describe(k, draft[k])}</strong>
									</li>
								))}
							</ul>
						</div>
						<Button variant="ghost" onClick={() => setDraft(baseline)}>
							Discard
						</Button>
						<Button variant="primary" busy={patch.isPending} disabled={invalid.length > 0} onClick={save}>
							Save routing
						</Button>
					</motion.div>
				)}
			</AnimatePresence>
			{guard}
		</div>
	);
}

function Cooldowns() {
	const creds = useEngineCredentials();
	const reset = useResetCooldowns();
	const toast = useToast();
	const [confirm, setConfirm] = useState(false);
	const list = creds.data ?? [];
	const resting = list.filter((c) => (c.cooldowns?.length ?? 0) > 0);
	return (
		<section className="stack" aria-labelledby="rt-cool">
			<div>
				<h3 className="sys-h3" id="rt-cool">
					Accounts resting right now
				</h3>
				<p className="muted sys-h3-desc">A resting account is skipped after a rate limit or repeated failures until its rest ends.</p>
			</div>
			<div className="panel rt-cool">
				<span className={cx("rt-cool-count num", resting.length > 0 && "is-warn")}>{creds.isPending ? "…" : fmtInt(resting.length)}</span>
				<div className="grow">
					<p>
						{creds.isPending
							? "Checking accounts…"
							: creds.isError
								? "Couldn't read account status."
								: resting.length === 0
									? list.length === 0
										? "There are no accounts in the pool yet."
										: list.length === 1
											? "The pool's only account isn't resting."
											: `None of the ${fmtInt(list.length)} accounts is resting.`
									: `${plural(resting.length, "account")} of ${fmtInt(list.length)} ${resting.length === 1 ? "is" : "are"} resting: ${resting.map((r) => r.label || r.email || r.name).join(", ")}.`}
					</p>
					<p className="faint rt-cool-sub">Ending the rests lets every account take requests again immediately. If a provider is still limiting you, they'll fail and rest again.</p>
				</div>
				<Button onClick={() => setConfirm(true)} disabled={!list.length}>
					End all rests
				</Button>
			</div>
			<ConfirmDialog
				open={confirm}
				onOpenChange={setConfirm}
				title="End every account's rest?"
				description={`All ${plural(list.length, "account")} become available right away, including any a provider is still rate limiting. Requests sent to those will fail and the account will rest again.`}
				confirmLabel="End all rests"
				busy={reset.isPending}
				onConfirm={async () => {
					try {
						const n = await reset.mutateAsync(list);
						toast(`Ended rests on ${plural(n, "account")}.`);
						setConfirm(false);
					} catch (e) {
						toast(`Couldn't end rests: ${(e as Error).message}`, "bad");
					}
				}}
			/>
		</section>
	);
}

function ModelDefinitions() {
	const defs = useModelDefinitions();
	const [q, setQ] = useState("");
	const [open, setOpen] = useState<string>();
	const [shown, setShown] = useState<{ channel: string; models: ModelDefinition[] }>();
	const needle = q.trim().toLowerCase();
	const rows = (defs.data ?? []).filter((r) => !needle || r.channel.includes(needle) || r.models.some((m) => m.id.toLowerCase().includes(needle) || m.display_name?.toLowerCase().includes(needle)));
	const current = defs.data?.find((r) => r.channel === open);
	useEffect(() => {
		if (current) setShown(current);
	}, [current]);
	return (
		<section className="stack" aria-labelledby="rt-defs">
			<div className="row between wrap">
				<div>
					<h3 className="sys-h3" id="rt-defs">
						Built-in model list
					</h3>
					<p className="muted sys-h3-desc">The models the engine knows each provider channel offers. Read only; which of them people can use is set on Models.</p>
				</div>
				<SearchInput value={q} onChange={setQ} placeholder="Find a channel or model" />
			</div>
			<div className="panel">
				{defs.isPending ? (
					<div className="stack rt-defs-loading">
						<Skeleton />
						<Skeleton />
						<Skeleton />
					</div>
				) : defs.isError ? (
					<EmptyState title="Couldn't load the model list" action={<Button onClick={() => defs.refetch()}>Try again</Button>}>
						{(defs.error as Error).message}
					</EmptyState>
				) : rows.length === 0 ? (
					<EmptyState title="Nothing matches">Try a shorter search, or clear it to see every channel.</EmptyState>
				) : (
					<ul className="defs">
						{rows.map((r) => (
							<li key={r.channel}>
								<button className="def-row" onClick={() => setOpen(r.channel)}>
									<code className="def-channel">{r.channel}</code>
									<span className="def-preview faint">
										{r.models
											.slice(0, 3)
											.map((m) => m.display_name || m.id)
											.join(", ")}
										{r.models.length > 3 ? `, and ${fmtInt(r.models.length - 3)} more` : ""}
									</span>
									<span className="num muted">{plural(r.models.length, "model")}</span>
									<Icon name="chevronRight" size={16} className="faint" />
								</button>
							</li>
						))}
					</ul>
				)}
			</div>
			<Dialog sheet open={!!open} onOpenChange={(v) => !v && setOpen(undefined)} title={<code>{shown?.channel}</code>} description={shown ? `${plural(shown.models.length, "model")} the engine knows for this channel.` : undefined}>
				<ul className="def-models">
					{(shown?.models ?? []).map((m) => (
						<li key={m.id}>
							<div className="row between">
								<strong>{m.display_name || m.id}</strong>
								{m.context_length ? <span className="faint num">{fmtCtx(m.context_length)} context</span> : null}
							</div>
							<code className="faint">{m.id}</code>
							{m.description && <p className="muted def-desc">{m.description}</p>}
						</li>
					))}
				</ul>
			</Dialog>
		</section>
	);
}

function fmtCtx(n: number): ReactNode {
	return n >= 1_000_000 ? `${(n / 1_000_000).toFixed(n % 1_000_000 ? 1 : 0)}M` : `${Math.round(n / 1000)}K`;
}
