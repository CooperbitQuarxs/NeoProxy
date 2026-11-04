import { useState } from "react";
import { ago, fmtInt, plural } from "../../lib/format";
import { Avatar, Button, CopyButton, EmptyState, ProviderMark, SearchInput, Section, Tip } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { RouteChain } from "./Chain";
import type { NeoModel, NeoUser, Vault, VirtualModel } from "./data";
import { colorOf, isGenerated, labelProvider, modelName, providerOfModel, searchText } from "./route";

type Counts = { byAsked: Map<string, number>; capped: boolean } | undefined;

export function VirtualList({
	vms,
	models,
	vault,
	users,
	counts,
	onCreate,
	canCreate,
}: {
	vms: VirtualModel[];
	models: NeoModel[];
	vault: Vault;
	users: NeoUser[];
	counts: Counts;
	onCreate: () => void;
	canCreate: boolean;
}) {
	const [query, setQuery] = useState("");
	const [showAllShared, setShowAllShared] = useState(false);
	const q = query.trim().toLowerCase();
	const matching = vms.filter((vm) => !q || searchText(vm, models, vault.providers).includes(q));
	const made = matching.filter((vm) => !isGenerated(vm, models)).sort((a, b) => a.name.localeCompare(b.name));
	const shared = matching.filter((vm) => isGenerated(vm, models)).sort((a, b) => a.name.localeCompare(b.name));
	const catalog = { models, providers: vault.providers, accounts: vault.accounts };
	const grantedTo = (vm: VirtualModel) => users.filter((u) => u.grants.some((g) => g.virtualModel === vm.id));
	const requests = (vm: VirtualModel) => counts?.byAsked.get(vm.served) ?? 0;
	const reqLabel = (n: number) => (counts?.capped && n ? `${fmtInt(n)}+` : fmtInt(n));

	if (!vms.length) {
		return (
			<div className="panel vm-empty">
				<RouteArt />
				<EmptyState
					title="No virtual models yet"
					action={
						canCreate ? (
							<Button variant="primary" size="lg" icon="plus" onClick={onCreate}>
								Create a virtual model
							</Button>
						) : undefined
					}
				>
					{canCreate ? (
						<>Create a name like neoproxy/office-default and choose which provider models answer it, in order. People you grant it to can use it right away.</>
					) : (
						<>
							Virtual models route to the models your pool serves, so they need at least one served provider model first.{" "}
							<a href="#/">Add provider access on the Pool page</a>.
						</>
					)}
				</EmptyState>
			</div>
		);
	}

	const sharedShown = showAllShared || q ? shared : shared.slice(0, 8);
	return (
		<div className="stack vm-tab">
			<div className="vm-toolbar">
				<SearchInput value={query} onChange={setQuery} placeholder="Find by name, provider or model" />
				<span className="faint" role="status" aria-live="polite">
					{q ? `${plural(matching.length, "match", "matches")} of ${fmtInt(vms.length)}` : ""}
				</span>
			</div>

			{made.length > 0 && (
				<Section title="Made here" description="Routes you defined. Select one to change its stages, try a request, or delete it.">
					<ul className="vm-cards">
						{made.map((vm) => {
							const people = grantedTo(vm);
							const n = requests(vm);
							return (
								<li key={vm.id} className="vm-card panel">
									<div className="vm-card-head">
										<a className="vm-card-link" href={`#/models/${encodeURIComponent(vm.id)}`}>
											<span className="vm-served mono">
												<span className="faint">neoproxy/</span>
												{vm.name}
											</span>
										</a>
										<CopyButton text={vm.served} label={`Copy ${vm.served}`} />
										<span className="grow" />
										<Icon name="chevronRight" size={16} className="faint vm-card-chevron" />
									</div>
									{vm.description ? <p className="vm-desc">{vm.description}</p> : <p className="vm-desc faint">No description yet.</p>}
									<RouteChain stages={vm.stages} terminal={vm.terminal} catalog={catalog} />
									<div className="vm-card-foot">
										<span className="vm-people">
											{people.length > 0 ? (
												<>
													<span className="mavatars" aria-hidden="true">
														{people.slice(0, 4).map((u) => (
															<Avatar key={u.id} name={u.handle} size={22} />
														))}
													</span>
													<Tip label={people.map((u) => u.handle).join(", ")}>
														<span tabIndex={0}>{people.length === 1 ? `Granted to ${people[0].handle}` : `Granted to ${people.length} people`}</span>
													</Tip>
												</>
											) : (
												<span className="faint">Not granted to anyone yet</span>
											)}
										</span>
										<span className={n ? undefined : "faint"}>
											<span className="num">{reqLabel(n)}</span> {n === 1 ? "request" : "requests"} in 24 hours
										</span>
										<span className="faint vm-updated">Changed {ago(vm.updatedAt)}{vm.updatedBy ? ` by ${users.find((u) => u.id === vm.updatedBy || u.handle === vm.updatedBy)?.handle ?? "an administrator"}` : ""}</span>
									</div>
								</li>
							);
						})}
					</ul>
				</Section>
			)}

			{shared.length > 0 && (
				<Section
					className="vm-shared"
					title="Shared from NeoPi"
					description="NeoProxy creates one name for each model a connected NeoPi shares, so you can grant them one by one. Each goes straight to that model. Open one to add stages or fallbacks."
				>
					<ul className="vm-rows panel">
						{sharedShown.map((vm) => {
							const s = vm.stages[0];
							const label = labelProvider(providerOfModel(s.model, models), vault.providers);
							const people = grantedTo(vm).length;
							const n = requests(vm);
							return (
								<li key={vm.id}>
									<a className="vm-row" href={`#/models/${encodeURIComponent(vm.id)}`} style={{ ["--p" as string]: colorOf(label) }}>
										<span className="vm-row-name mono">
											<span className="faint">neoproxy/</span>
											{vm.name}
										</span>
										<span className="vm-row-target">
											<ProviderMark provider={label.colorKey} name={label.name} size={16} />
											{modelName(s.model, models)}
											<span className="faint">via {label.source}</span>
										</span>
										<span className="faint num">{people ? plural(people, "person", "people") : "Not granted"}</span>
										<span className="faint num">{n ? `${reqLabel(n)} today` : "Idle today"}</span>
									</a>
								</li>
							);
						})}
					</ul>
					{!q && shared.length > 8 && (
						<div>
							<Button variant="ghost" size="sm" onClick={() => setShowAllShared((v) => !v)}>
								{showAllShared ? "Show fewer" : `Show all ${shared.length}`}
							</Button>
						</div>
					)}
				</Section>
			)}

			{!matching.length && (
				<div className="panel">
					<EmptyState title="Nothing matches">Try a provider name, a model like gpt-5.5, or part of a virtual name.</EmptyState>
				</div>
			)}
		</div>
	);
}

/** A quiet illustration of a route: three colored steps feeding one name. */
function RouteArt() {
	const hues = [165, 45, 255];
	return (
		<svg className="vm-art" viewBox="0 0 320 96" aria-hidden="true">
			<rect x="4" y="34" width="72" height="28" rx="8" className="vm-art-name" />
			{hues.map((h, i) => (
				<g key={h} style={{ animationDelay: `${120 + i * 110}ms` }} className="vm-art-step">
					<path d={`M${76 + i * 80} 48 h16`} className="vm-art-link" />
					<rect x={92 + i * 80} y="34" width="64" height="28" rx="8" style={{ fill: `oklch(var(--provider-l) var(--provider-c) ${h} / 0.22)`, stroke: `oklch(var(--provider-l) var(--provider-c) ${h})` }} />
					<circle cx={106 + i * 80} cy="48" r="5" style={{ fill: `oklch(var(--provider-l) var(--provider-c) ${h})` }} />
				</g>
			))}
		</svg>
	);
}
