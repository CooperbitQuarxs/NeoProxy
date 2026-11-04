import { useState } from "react";
import { fmtInt, plural } from "../../lib/format";
import { Button, CopyButton, Dialog, EmptyState, ProviderMark, SearchInput, Switch, Tip, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useToast } from "../../ui/toast";
import { useSetModelServed, useSetProviderServed, type NeoModel, type NeoProvider, type NeoUser, type Vault, type VirtualModel } from "./data";
import { colorOf, compactCtx, labelProvider, providerOfModel } from "./route";

type Counts = { byAsked: Map<string, number>; byModel: Map<string, number>; capped: boolean } | undefined;
type Show = "all" | "on" | "off";
type PendingOff = { kind: "provider"; provider: NeoProvider; routes: VirtualModel[] } | { kind: "model"; model: NeoModel; routes: VirtualModel[] };

export function Catalogue({ models, vault, vms, users, counts }: { models: NeoModel[]; vault: Vault; vms: VirtualModel[]; users: NeoUser[]; counts: Counts }) {
	const toast = useToast();
	const setModel = useSetModelServed();
	const setProvider = useSetProviderServed();
	const [query, setQuery] = useState("");
	const [show, setShow] = useState<Show>("all");
	const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
	const [pending, setPending] = useState<PendingOff>();
	const q = query.trim().toLowerCase();

	const routesUsingProvider = (pid: string) => vms.filter((vm) => vm.stages.some((s) => providerOfModel(s.model, models) === pid) || (vm.terminal.kind === "passthrough" && vm.terminal.model && providerOfModel(vm.terminal.model, models) === pid));
	const routesUsingModel = (mid: string) => vms.filter((vm) => vm.stages.some((s) => s.model === mid) || (vm.terminal.kind === "passthrough" && vm.terminal.model === mid));
	// Direct calls and virtual-model stages both record the provider model they used.
	const reqs = (m: NeoModel) => Math.max(counts?.byAsked.get(m.served) ?? 0, counts?.byModel.get(m.id) ?? 0);

	const apply = async (p: PendingOff | { kind: "provider-on"; provider: NeoProvider } | { kind: "model-on"; model: NeoModel }) => {
		try {
			if (p.kind === "provider" || p.kind === "provider-on") {
				const on = p.kind === "provider-on";
				const name = labelProvider(p.provider.id, vault.providers).name;
				await setProvider.mutateAsync({ id: p.provider.id, exposed: on });
				toast(on ? `${name} is served again` : `${name} is turned off for everyone`);
			} else {
				const on = p.kind === "model-on";
				await setModel.mutateAsync({ id: p.model.id, exposed: on });
				toast(on ? `${p.model.display} is served` : `${p.model.display} is turned off`);
			}
		} catch (e) {
			toast(`Couldn't change that: ${(e as Error).message}. Try again in a moment.`, "bad");
		}
		setPending(undefined);
	};

	const groups = vault.providers
		.map((p) => {
			const label = labelProvider(p.id, vault.providers);
			const all = models.filter((m) => m.providerId === p.id);
			const list = all.filter((m) => {
				const on = m.exposed && p.exposed;
				if (show === "on" && !on) return false;
				if (show === "off" && on) return false;
				return !q || [m.display, m.name, m.id, label.name, label.source, ...m.tags].some((v) => v?.toLowerCase().includes(q));
			});
			return { p, label, all, list };
		})
		.filter((g) => g.list.length > 0);

	const local = models.filter((m) => !m.virtualOnly);
	const servedCount = models.filter((m) => m.exposed && vault.providers.find((p) => p.id === m.providerId)?.exposed).length;

	if (!models.length) {
		return (
			<div className="panel">
				<EmptyState title="No provider models yet" action={<Button variant="primary" icon="plus" onClick={() => (location.hash = "#/")}>Add provider access</Button>}>
					Models appear here as soon as the pool has provider access that unlocks them: a signed-in account, an API key or a NeoPi share.
				</EmptyState>
			</div>
		);
	}

	return (
		<div className="stack cat-tab">
			<p className="cat-lede">
				Every model your pool can reach, by provider. Turned-off models can't be used by anyone, directly or as a stage in a virtual model. Right now{" "}
				<strong className="num">{fmtInt(servedCount)}</strong> of {plural(models.length, "model")} are served
				{local.length < models.length ? `, ${fmtInt(models.length - local.length)} of them shared from NeoPi` : ""}.
			</p>
			<div className="vm-toolbar">
				<SearchInput value={query} onChange={setQuery} placeholder="Find a model or provider" />
				<div className="mseg" role="radiogroup" aria-label="Show">
					{(
						[
							["all", "All"],
							["on", "Served"],
							["off", "Turned off"],
						] as const
					).map(([v, l]) => (
						<button key={v} role="radio" aria-checked={show === v} className={cx("mseg-btn", show === v && "is-on")} onClick={() => setShow(v)}>
							{l}
						</button>
					))}
				</div>
			</div>

			{groups.length === 0 && (
				<div className="panel">
					<EmptyState title="Nothing matches">Try a provider name, a model id like gpt-5.5, or switch the filter to All.</EmptyState>
				</div>
			)}

			{groups.map(({ p, label, all, list }) => {
				const isCollapsed = collapsed.has(p.id) && !q;
				const on = all.filter((m) => m.exposed).length;
				const accounts = vault.accounts.filter((a) => a.providerId === p.id).length;
				const direct = users.filter((u) => u.providerAccess.includes(p.id)).length;
				const routes = routesUsingProvider(p.id);
				return (
					<section key={p.id} className={cx("cat-group panel", !p.exposed && "is-off")} style={{ ["--p" as string]: colorOf(label) }}>
						<header className="cat-head">
							<button
								className="cat-toggle"
								aria-expanded={!isCollapsed}
								onClick={() =>
									setCollapsed((s) => {
										const n = new Set(s);
										if (n.has(p.id)) n.delete(p.id);
										else n.add(p.id);
										return n;
									})
								}
							>
								<Icon name="chevron" size={16} className={cx("cat-caret", isCollapsed && "is-collapsed")} />
								<ProviderMark provider={label.colorKey} name={label.name} size={26} />
								<span className="cat-title">
									<strong>{label.name}</strong>
									<span className="faint">{label.shared ? `Shared by ${label.source}` : `${plural(accounts, "account")} in the pool`}</span>
								</span>
							</button>
							<div className="cat-facts">
								<span>
									<span className="num">{on}</span> of {plural(all.length, "model")} on
								</span>
								<span>{routes.length ? `Used by ${plural(routes.length, "virtual model")}` : "Not in any virtual model"}</span>
								{label.shared ? (
									<Tip label="Shared models can only be reached through virtual models, so grant those to people.">
										<span tabIndex={0}>Through virtual models only</span>
									</Tip>
								) : (
									direct > 0 && <span>{direct === 1 ? "1 person calls it directly" : `${direct} people call it directly`}</span>
								)}
							</div>
							<Switch
								checked={p.exposed}
								onCheckedChange={(v) => (v ? apply({ kind: "provider-on", provider: p }) : routes.length ? setPending({ kind: "provider", provider: p, routes }) : apply({ kind: "provider", provider: p, routes }))}
								label={<span className="sr-only">Serve {label.name}</span>}
								id={`serve-${p.id}`}
							/>
						</header>
						{!p.exposed && <p className="cat-off-note">Turned off. Nobody can reach these models, and virtual model stages on {label.name} are skipped until you turn it back on.</p>}
						{!isCollapsed && (
							<ul className="cat-rows">
								{list.map((m) => {
									const served = m.exposed && p.exposed;
									const n = reqs(m);
									return (
										<li key={m.id} className={cx("cat-row", !served && "is-off")}>
											<span className="cat-model">
												<span className="cat-model-name">{m.display}</span>
												<span className="cat-model-id mono faint">{m.served}</span>
											</span>
											<CopyButton text={m.served} label={`Copy ${m.served}`} />
											<span className="cat-tags">
												{m.tags.map((t) => (
													<span key={t} className="chip">
														{t}
													</span>
												))}
											</span>
											<span className="faint num cat-ctx">{m.context ? `${compactCtx(m.context)} context` : ""}</span>
											<span className={cx("num cat-req", !n && "faint")}>{n ? `${counts?.capped ? `${fmtInt(n)}+` : fmtInt(n)} today` : "Idle today"}</span>
											<Tip label={!p.exposed ? `Turn on ${label.name} first` : m.exposed ? "Turn off this model" : "Serve this model"}>
												<span>
													<Switch
														checked={m.exposed}
														disabled={!p.exposed}
														onCheckedChange={(v) => {
															const r = routesUsingModel(m.id);
															if (v) apply({ kind: "model-on", model: m });
															else if (r.length) setPending({ kind: "model", model: m, routes: r });
															else apply({ kind: "model", model: m, routes: r });
														}}
														id={`serve-${m.id}`}
														label={<span className="sr-only">Serve {m.display}</span>}
													/>
												</span>
											</Tip>
										</li>
									);
								})}
							</ul>
						)}
					</section>
				);
			})}

			<Dialog
				open={!!pending}
				onOpenChange={(v) => !v && setPending(undefined)}
				title={pending?.kind === "provider" ? `Turn off ${labelProvider(pending.provider.id, vault.providers).name}?` : pending ? `Turn off ${pending.model.display}?` : ""}
				description={
					pending?.kind === "provider"
						? "Every model from this provider stops being served. Nobody can call them directly, and stages that use them are skipped."
						: "Nobody can call this model directly, and stages that use it are skipped."
				}
				footer={
					<>
						<Button variant="ghost" onClick={() => setPending(undefined)}>
							Keep it on
						</Button>
						<Button variant="danger" busy={setModel.isPending || setProvider.isPending} onClick={() => pending && apply(pending)}>
							Turn off
						</Button>
					</>
				}
			>
				{pending && (
					<div className="stack">
						<p>
							{pending.routes.length === 1 ? "This virtual model uses it:" : `These ${pending.routes.length} virtual models use it:`}
						</p>
						<ul className="cat-affected">
							{pending.routes.map((vm) => (
								<li key={vm.id} className="mono">
									{vm.served}
								</li>
							))}
						</ul>
						<p className="muted">Requests to them move on to their next stage, or end the way each one is set to end. You can turn it back on any time.</p>
					</div>
				)}
			</Dialog>
		</div>
	);
}
