import { useEffect, useState } from "react";
import { useRefreshAccess, useRemoveAccess, useReimport, useUpdateAccess } from "../api/queries";
import type { AccessItem, Person, ProviderInfo } from "../api/types";
import { ago, perMonth, plural } from "../lib/format";
import { providerInfo } from "../lib/providers";
import { Button, Chip, Dialog, Field, Input, Notice, ProviderMark, Select, StatusDot } from "../ui/components";
import { Icon } from "../ui/icons";
import { useToast } from "../ui/toast";
import { Spark } from "./AccessList";
import { BudgetSection } from "./Budget";
import { KIND_COPY, REFRESH_COPY, healthCopy, personName } from "./copy";

export function AccessSheet({
	item,
	people,
	providers,
	admin,
	userId,
	onClose,
	onSignInSeparately,
}: {
	item?: AccessItem;
	people: Person[];
	providers: ProviderInfo[];
	admin: boolean;
	userId: string;
	onClose: () => void;
	onSignInSeparately: (provider: string) => void;
}) {
	// Keep the last item rendered while the sheet animates out.
	const [shown, setShown] = useState(item);
	useEffect(() => {
		if (item) setShown(item);
	}, [item]);
	const open = !!item;
	const it = item ?? shown;
	return (
		<Dialog
			sheet
			open={open}
			onOpenChange={(v) => !v && onClose()}
			title={it ? <SheetTitle item={it} providers={providers} /> : ""}
			description={it ? KIND_COPY[it.kind].noun + (it.neopiSource ? `, via ${it.neopiSource.name}` : "") : undefined}
		>
			{it && <SheetBody item={it} people={people} providers={providers} admin={admin} userId={userId} onClose={onClose} onSignInSeparately={onSignInSeparately} />}
		</Dialog>
	);
}

function SheetTitle({ item, providers }: { item: AccessItem; providers: ProviderInfo[] }) {
	const info = providerInfo(item.provider, providers);
	return (
		<span className="sheet-title">
			<ProviderMark provider={item.provider} name={info.name} hue={info.hue} size={28} />
			{info.name}
		</span>
	);
}

function SheetBody({
	item,
	people,
	providers,
	admin,
	userId,
	onClose,
	onSignInSeparately,
}: {
	item: AccessItem;
	people: Person[];
	providers: ProviderInfo[];
	admin: boolean;
	userId: string;
	onClose: () => void;
	onSignInSeparately: (provider: string) => void;
}) {
	const toast = useToast();
	const update = useUpdateAccess();
	const remove = useRemoveAccess();
	const reimport = useReimport();
	const refresh = useRefreshAccess();
	const [confirmRemove, setConfirmRemove] = useState(false);
	const [customValue, setCustomValue] = useState("");
	const info = providerInfo(item.provider, providers);
	const health = healthCopy(item);
	const plans = info.plans ?? [];
	const planOptions = [
		...plans.map((p) => ({ value: p.id, label: p.name, description: perMonth(p.monthlyUSD) })),
		{ value: "__custom", label: "Custom amount", description: "Set the monthly value yourself" },
	];
	const planValue = plans.some((p) => p.id === item.plan.id) ? item.plan.id : "__custom";

	const run = async (what: Promise<unknown>, done: string) => {
		try {
			await what;
			toast(done);
		} catch (e) {
			toast((e as Error).message, "bad");
		}
	};

	return (
		<>
			<div className="sheet-status">
				<StatusDot tone={health.tone} />
				<div className="grow">
					<strong>{health.label}</strong>
					<p className="muted">{health.explain}</p>
				</div>
			</div>
			{(item.actions.includes("refresh") || item.actions.includes("download")) && (
				<div className="row wrap sheet-actions">
					{item.actions.includes("refresh") && (
						<Button size="sm" icon="refresh" busy={refresh.isPending} onClick={() => run(refresh.mutateAsync(item), "Sign-in renewed")}>
							Refresh sign-in now
						</Button>
					)}
					{item.actions.includes("download") && (
						<a className="btn btn-secondary btn-sm" href={`/v8/neo/access/${encodeURIComponent(item.id)}/file`} download>
							<Icon name="file" size={15} />
							<span>Download credential file</span>
						</a>
					)}
					<span className="faint sheet-actions-note">
						{item.actions.includes("download") ? "The file holds the sign-in itself. Use it to move this account to another proxy; keep it private." : "NeoProxy also refreshes on its own before the sign-in expires."}
					</span>
				</div>
			)}

			{item.refresh === "locked" && (
				<Notice
					tone="warn"
					title="Imported with refresh locked"
					action={undefined}
				>
					<p>NeoProxy uses the copied sign-in but never its refresh token, so NeoPi's own sign-in keeps working. When the copy expires, this access stops.</p>
					<div className="row wrap notice-actions">
						<Button size="sm" icon="refresh" busy={reimport.isPending} onClick={() => run(reimport.mutateAsync(item), "Re-imported from NeoPi")}>
							Re-import from NeoPi
						</Button>
						<Button size="sm" icon="signin" onClick={() => onSignInSeparately(item.provider)}>
							Sign in separately
						</Button>
					</div>
					{item.importedFrom && (
						<p className="faint import-origin">
							From <code>{item.importedFrom.profile}</code>, imported {ago(item.importedFrom.importedAt)}. Re-import after NeoPi refreshes its sign-in; signing in separately gives NeoProxy its own refreshable sign-in.
						</p>
					)}
				</Notice>
			)}

			<dl className="facts">
				<div>
					<dt>Account</dt>
					<dd className="mono">{item.label}</dd>
				</div>
				<div>
					<dt>Source</dt>
					<dd>
						{KIND_COPY[item.kind].noun}
						<p className="faint">{REFRESH_COPY[item.refresh]}</p>
					</dd>
				</div>
				<div>
					<dt>Usage today</dt>
					<dd className="row">
						<Spark buckets={item.usage.buckets} />
						<span>
							{plural(item.usage.requests24h, "request")}
							{item.usage.failed24h ? <span className="faint">, {item.usage.failed24h} failed</span> : null}
						</span>
					</dd>
				</div>
				<div>
					<dt>Last used</dt>
					<dd>{ago(item.usage.lastUsedAt)}</dd>
				</div>
			</dl>

			<BudgetSection item={item} canEdit={admin || item.ownerId === userId} />

			{admin && (
				<div className="sheet-form">
					<Field label="Contributor" hint="Whose access this is. Contributors see their own access and its usage." htmlFor="owner">
						<Select
							id="owner"
							value={item.ownerId || undefined}
							placeholder="Unassigned"
							onValueChange={(ownerId) => run(update.mutateAsync({ item, change: { ownerId } }), `Now contributed by ${personName(ownerId, people)}`)}
							options={people.map((p) => ({ value: p.id, label: p.name || p.handle, description: p.handle }))}
						/>
					</Field>
					<Field
						label="Plan"
						hint={
							item.plan.source === "detected"
								? "Detected from the account. The plan sets this tile's size in the pool."
								: item.plan.source === "admin"
									? "Set by an administrator. The plan sets this tile's size in the pool."
									: "Not detected, so this is an estimate. Pick the real plan to size the tile correctly."
						}
						htmlFor="plan"
					>
						<Select
							id="plan"
							value={planValue}
							onValueChange={(planId) => {
								if (planId !== "__custom") run(update.mutateAsync({ item, change: { planId } }), "Plan updated");
								else setCustomValue(String(item.plan.monthlyUSD));
							}}
							options={planOptions}
						/>
					</Field>
					{(planValue === "__custom" || customValue) && (
						<Field label="Monthly value" hint="In US dollars. Used only to size the tile and compare contributions; NeoProxy never spends it." htmlFor="value">
							<div className="row">
								<Input id="value" type="number" min={0} step={1} value={customValue || item.plan.monthlyUSD} onChange={(e) => setCustomValue(e.target.value)} />
								<Button
									onClick={() => {
										const n = Number(customValue);
										if (Number.isFinite(n) && n >= 0) run(update.mutateAsync({ item, change: { monthlyUSD: n } }), "Value updated").then(() => setCustomValue(""));
									}}
								>
									Save
								</Button>
							</div>
						</Field>
					)}
				</div>
			)}

			<div className="sheet-models">
				<h3 className="sheet-h3">Unlocks {plural(item.models.length, "model")}</h3>
				<div className="row wrap">
					{item.models.slice(0, 18).map((m) => (
						<Chip key={m}>
							<span className="mono">{m.replace(/^[^/]+\//, "")}</span>
						</Chip>
					))}
					{item.models.length > 18 && <span className="faint">and {item.models.length - 18} more</span>}
					{item.models.length === 0 && <span className="faint">No models discovered yet. They appear once the provider answers a model listing.</span>}
				</div>
			</div>

			{admin && (
				<div className="sheet-danger">
					<div className="grow">
						<strong>{item.health === "disabled" ? "Resume this access" : "Pause this access"}</strong>
						<p className="faint">{item.health === "disabled" ? "It starts serving requests again." : "It stays in the pool but serves nothing until resumed."}</p>
					</div>
					<Button
						onClick={() => run(update.mutateAsync({ item, change: { disabled: item.health !== "disabled" } }), item.health === "disabled" ? "Resumed" : "Paused")}
						busy={update.isPending}
					>
						{item.health === "disabled" ? "Resume" : "Pause"}
					</Button>
				</div>
			)}
			{admin && (
				<div className="sheet-danger">
					<div className="grow">
						<strong>Remove from the pool</strong>
						<p className="faint">
							{item.kind === "neopi"
								? "Detaches this account from NeoProxy. Nothing changes inside NeoPi."
								: item.kind === "imported"
									? "Deletes NeoProxy's copy. NeoPi's original is untouched."
									: "Deletes the stored credential from NeoProxy. Virtual models that used it fall back to other access."}
						</p>
					</div>
					{confirmRemove ? (
						<div className="row">
							<Button variant="ghost" onClick={() => setConfirmRemove(false)}>
								Keep
							</Button>
							<Button
								variant="danger"
								busy={remove.isPending}
								onClick={() =>
									remove
										.mutateAsync(item)
										.then(() => {
											toast(`Removed ${info.name} access`);
											onClose();
										})
										.catch((e) => toast((e as Error).message, "bad"))
								}
							>
								Remove
							</Button>
						</div>
					) : (
						<Button variant="danger" onClick={() => setConfirmRemove(true)}>
							Remove…
						</Button>
					)}
				</div>
			)}
		</>
	);
}
