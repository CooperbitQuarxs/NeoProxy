import * as RPopover from "@radix-ui/react-popover";
import { AnimatePresence, motion } from "motion/react";
import { useEffect, useMemo, useRef, useState } from "react";
import { canonicalProvider, providerColor, providerInfo } from "../../lib/providers";
import { Button, IconButton, Switch, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useToast } from "../../ui/toast";
import { useUpdateUser, type PoolModel, type Usage, type User, type Vault, type VirtualModel } from "./api";
import { displayName } from "./copy";
import { Meter } from "./widgets";

const providerOf = (model: string) => canonicalProvider(model.split("/")[0] ?? "");

export function StageSwatches({ vm }: { vm: VirtualModel }) {
	return (
		<span className="vm-swatches" aria-hidden="true">
			{vm.stages.map((s) => (
				<span key={s.id} className="vm-swatch" style={{ background: providerColor(providerOf(s.model)) }} />
			))}
		</span>
	);
}

export function stageSummary(vm: VirtualModel, models: PoolModel[]) {
	if (!vm.stages.length) return "No stages yet";
	const names = vm.stages.map((s) => models.find((m) => m.id === s.model)?.display ?? s.model.split("/").pop());
	return names.length === 1 ? `Uses ${names[0]}` : `Tries ${names.slice(0, -1).join(", ")}, then ${names[names.length - 1]}`;
}

/** Fraction of this person's allowance in a model drawn during the current windows. */
function drawn(usage: Usage | undefined, userId: string, vm: VirtualModel, allotment: number) {
	const per = usage?.[userId]?.[vm.id];
	if (!per) return 0;
	let used = 0;
	for (const st of Object.values(per)) for (const v of Object.values(st)) used += v;
	const allowance = allotment * vm.stages.reduce((n, s) => n + s.cap, 0);
	return allowance > 0 ? used / allowance : 0;
}

export function GrantsPanel({ user, vms, models, usage, vault }: { user: User; vms: VirtualModel[]; models: PoolModel[]; usage?: Usage; vault?: Vault }) {
	const update = useUpdateUser();
	const toast = useToast();
	const granted = user.grants ?? [];
	const name = displayName(user);

	const save = (grants: User["grants"], done: string) =>
		update
			.mutateAsync({ user, change: { grants } })
			.then(() => toast(done))
			.catch((e) => toast(`Couldn't save: ${(e as Error).message}. Reload the page and try again.`, "bad"));

	return (
		<section className="panel pp-panel">
			<header className="pp-panel-head">
				<div className="grow">
					<h2 className="pp-h2">What they can use</h2>
					<p className="pp-desc">
						The models {name} can ask for. Each one is a route through the pool, set up on the Models page.{granted.length > 0 && " Their share scales what they may draw from each model: at 50% they get half of every stage's budget."}
					</p>
				</div>
				<GrantPicker vms={vms} models={models} granted={new Set(granted.map((g) => g.virtualModel))} onPick={(vm) => save([...granted, { virtualModel: vm.id, allotment: 1 }], `${name} can now use ${vm.name}`)} />
			</header>

			{granted.length === 0 ? (
				<div className="pp-grants-empty">
					{vms.length === 0 ? (
						<p className="muted">
							There are no models to give yet. Create one on the <a href="#/models">Models page</a>, then come back to give {name} access.
						</p>
					) : (
						<p className="muted">{name} can't ask for any model yet. Give them access to one, then issue a key so their apps can use it.</p>
					)}
				</div>
			) : (
				<ul className="grants">
					<AnimatePresence initial={false}>
						{granted.map((g) => {
							const vm = vms.find((v) => v.id === g.virtualModel);
							return (
								<motion.li key={g.virtualModel} layout initial={{ opacity: 0, y: -6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, height: 0, paddingTop: 0, paddingBottom: 0 }} transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}>
									{vm ? (
										<GrantRow
											vm={vm}
											models={models}
											allotment={g.allotment}
											used={drawn(usage, user.id, vm, g.allotment)}
											onAllotment={(allotment) => save(granted.map((x) => (x.virtualModel === vm.id ? { ...x, allotment } : x)), `${vm.name}: ${Math.round(allotment * 100)}% share saved`)}
											onRemove={() => save(granted.filter((x) => x.virtualModel !== vm.id), `${name} can no longer use ${vm.name}`)}
										/>
									) : (
										<div className="grant">
											<span className="grow muted">A model that no longer exists</span>
											<Button size="sm" variant="ghost" onClick={() => save(granted.filter((x) => x.virtualModel !== g.virtualModel), "Removed")}>
												Remove
											</Button>
										</div>
									)}
								</motion.li>
							);
						})}
					</AnimatePresence>
				</ul>
			)}

			{vault && <DirectAccess user={user} vault={vault} />}
		</section>
	);
}

function GrantRow({ vm, models, allotment, used, onAllotment, onRemove }: { vm: VirtualModel; models: PoolModel[]; allotment: number; used: number; onAllotment: (v: number) => void; onRemove: () => void }) {
	const [value, setValue] = useState(Math.round(allotment * 100));
	useEffect(() => setValue(Math.round(allotment * 100)), [allotment]);
	const commit = () => {
		if (value !== Math.round(allotment * 100)) onAllotment(value / 100);
	};
	const id = `allot-${vm.id}`;
	return (
		<div className="grant">
			<StageSwatches vm={vm} />
			<div className="grant-main">
				<div className="grant-title">
					<strong>{vm.name}</strong>
					<code className="faint">{vm.served}</code>
				</div>
				<p className="faint grant-route">{stageSummary(vm, models)}</p>
				<div className="grant-use">
					<Meter value={used} tone={used >= 1 ? "bad" : used > 0.8 ? "warn" : "accent"} label={`${Math.round(used * 100)}% of their share used this window`} />
					<span className="faint num">{used > 0 ? `${Math.round(used * 100)}% of their share used this window` : "Nothing used this window"}</span>
				</div>
			</div>
			<div className="grant-share">
				<label htmlFor={id} className="grant-share-label">
					Share <span className="num">{value === 0 ? "paused" : `${value}%`}</span>
				</label>
				<input
					id={id}
					type="range"
					className="range"
					min={0}
					max={100}
					step={5}
					value={value}
					style={{ ["--v" as string]: `${value}%` }}
					onChange={(e) => setValue(Number(e.target.value))}
					onPointerUp={commit}
					onKeyUp={commit}
					onBlur={commit}
					aria-describedby={`${id}-hint`}
				/>
				<span id={`${id}-hint`} className="sr-only">
					Scales every budget in this model for them. 50% gives half of each stage's allowance.
				</span>
			</div>
			<IconButton icon="close" label={`Stop access to ${vm.name}`} onClick={onRemove} />
		</div>
	);
}

/** Searchable model picker in a popover; arrow keys move, Enter grants. */
function GrantPicker({ vms, models, granted, onPick }: { vms: VirtualModel[]; models: PoolModel[]; granted: Set<string>; onPick: (vm: VirtualModel) => void }) {
	const [open, setOpen] = useState(false);
	const [q, setQ] = useState("");
	const [active, setActive] = useState(0);
	const listRef = useRef<HTMLUListElement>(null);
	const available = vms.filter((v) => !granted.has(v.id));
	const matches = useMemo(() => {
		const needle = q.trim().toLowerCase();
		if (!needle) return available;
		return available.filter((vm) => [vm.name, vm.served, vm.description, ...vm.stages.map((s) => s.model), ...vm.stages.map((s) => models.find((m) => m.id === s.model)?.display ?? "")].join(" ").toLowerCase().includes(needle));
	}, [available, q, models]);
	useEffect(() => setActive(0), [q, open]);
	useEffect(() => {
		listRef.current?.querySelector<HTMLElement>(`[data-i="${active}"]`)?.scrollIntoView({ block: "nearest" });
	}, [active]);

	const pick = (vm: VirtualModel) => {
		onPick(vm);
		setOpen(false);
		setQ("");
	};

	return (
		<RPopover.Root open={open} onOpenChange={setOpen}>
			<RPopover.Trigger asChild>
				<Button icon="plus" disabled={available.length === 0} title={available.length === 0 ? (vms.length ? "They can already use every model" : "No models exist yet") : undefined}>
					Give access to a model
				</Button>
			</RPopover.Trigger>
			<RPopover.Portal>
				<RPopover.Content className="picker" align="end" sideOffset={8} collisionPadding={16}>
					<div className="search picker-search">
						<Icon name="search" size={16} />
						<input
							autoFocus
							value={q}
							onChange={(e) => setQ(e.target.value)}
							placeholder="Search models"
							aria-label="Search models"
							role="combobox"
							aria-expanded="true"
							aria-controls="grant-options"
							aria-activedescendant={matches[active] ? `grant-opt-${matches[active].id}` : undefined}
							onKeyDown={(e) => {
								if (e.key === "ArrowDown") {
									e.preventDefault();
									setActive((a) => Math.min(matches.length - 1, a + 1));
								} else if (e.key === "ArrowUp") {
									e.preventDefault();
									setActive((a) => Math.max(0, a - 1));
								} else if (e.key === "Enter" && matches[active]) {
									e.preventDefault();
									pick(matches[active]);
								}
							}}
						/>
					</div>
					<ul className="picker-list" role="listbox" id="grant-options" ref={listRef} aria-label="Models">
						{matches.map((vm, i) => (
							<li
								key={vm.id}
								id={`grant-opt-${vm.id}`}
								role="option"
								aria-selected={i === active}
								data-i={i}
								className={cx("picker-item", i === active && "is-active")}
								onMouseMove={() => setActive(i)}
								onClick={() => pick(vm)}
							>
								<StageSwatches vm={vm} />
								<span className="grow picker-text">
									<span className="picker-name">
										{vm.name}
										<code className="faint">{vm.served}</code>
									</span>
									<span className="faint picker-desc">{vm.description || stageSummary(vm, models)}</span>
								</span>
							</li>
						))}
						{matches.length === 0 && <li className="picker-none">{available.length ? "No model matches. Try a provider or model name." : "They can already use every model."}</li>}
					</ul>
					<p className="picker-foot faint">They get a full share; adjust it after adding.</p>
				</RPopover.Content>
			</RPopover.Portal>
		</RPopover.Root>
	);
}

/** Per-person permission to call provider models by their full name. */
function DirectAccess({ user, vault }: { user: User; vault: Vault }) {
	const update = useUpdateUser();
	const toast = useToast();
	const providers = vault.providers.filter((p) => p.kind !== "neopi-gateway");
	const [open, setOpen] = useState((user.providerAccess ?? []).length > 0);
	if (!providers.length) return null;
	const access = user.providerAccess ?? [];
	return (
		<div className="direct">
			<button type="button" className="direct-toggle" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
				<Icon name="chevronRight" size={15} className={cx("direct-chev", open && "is-open")} />
				Direct provider models
				<span className="faint">{access.length ? `${access.length} allowed` : "Off"}</span>
			</button>
			{open && (
				<div className="direct-body">
					<p className="faint">
						Lets {displayName(user)}'s keys call a provider's models by full name (like <code>neoproxy/codex/gpt-5.5</code>) without going through a model above, so no shares or stages apply. Most people don't need this.
					</p>
					<div className="direct-list">
						{providers.map((p) => {
							const info = providerInfo(p.id);
							return (
								<Switch
									key={p.id}
									id={`direct-${p.id}`}
									checked={access.includes(p.id)}
									disabled={!p.exposed || update.isPending}
									onCheckedChange={(v) =>
										update
											.mutateAsync({ user, change: { providerAccess: v ? [...access, p.id] : access.filter((x) => x !== p.id) } })
											.then(() => toast(v ? `Direct ${info.name} models allowed` : `Direct ${info.name} models turned off`))
											.catch((e) => toast(`Couldn't save: ${(e as Error).message}`, "bad"))
									}
									label={
										<span className="row">
											<span className="vm-swatch" style={{ background: providerColor(info.id) }} />
											{info.name}
											{!p.exposed && <span className="faint">hidden on Models</span>}
										</span>
									}
								/>
							);
						})}
					</div>
				</div>
			)}
		</div>
	);
}
