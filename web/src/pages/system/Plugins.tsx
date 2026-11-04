import { useEffect, useMemo, useState } from "react";
import { fmtInt } from "../../lib/format";
import { Button, Chip, Dialog, EmptyState, IconButton, Notice, SearchInput, Select, Skeleton, Switch, TabPanel, Tabs } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useToast } from "../../ui/toast";
import {
	useClearPluginQuota,
	useEngineCredentials,
	useInstallPlugin,
	usePatchConfig,
	usePluginQuota,
	usePluginStore,
	usePlugins,
	useRemovePlugin,
	useSetPluginEnabled,
	type InstalledPlugin,
	type StorePlugin,
} from "./api";
import { ConfirmDialog, Facts, SubHead } from "./shared";

function hue(s: string) {
	let h = 0;
	for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 360;
	return h;
}
function PlugMark({ name, size = 34 }: { name: string; size?: number }) {
	const letters = name.replace(/[^A-Za-z0-9 ]/g, "").split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0]!.toUpperCase()).join("") || "?";
	return (
		<span className="plug-mark" style={{ width: size, height: size, fontSize: size * 0.36, ["--h" as string]: hue(name) }} aria-hidden="true">
			{letters}
		</span>
	);
}
const nameOf = (p: InstalledPlugin) => p.metadata?.name || p.id;
const repoUrl = (r?: string) => (!r ? undefined : r.startsWith("http") ? r : `https://github.com/${r}`);

export function Plugins() {
	const installed = usePlugins();
	const store = usePluginStore();
	const [tab, setTab] = useState("installed");
	const [openId, setOpenId] = useState<string>();
	const list = installed.data?.plugins ?? [];
	const loadingOn = installed.data?.plugins_enabled;
	useEffect(() => {
		if (installed.data && list.length === 0) setTab("store");
	}, [installed.data, list.length]);
	const open = list.find((p) => p.id === openId);

	return (
		<div className="stack-lg">
			<SubHead
				title="Plugins"
				lede="Extensions that run inside the engine, for example to rewrite requests or report an account's remaining quota. A plugin sees every request that passes through, so install only ones you trust."
			/>

			<PluginLoading enabled={loadingOn} installedCount={list.length} dir={installed.data?.plugins_dir} />

			<Tabs
				value={tab}
				onValueChange={setTab}
				items={[
					{ value: "installed", label: "Installed", count: installed.data ? list.length : undefined },
					{ value: "store", label: "Plugin store", count: store.data?.plugins?.length },
				]}
			>
				<TabPanel value="installed">
					{installed.isPending ? (
						<div className="panel plug-loading">
							<Skeleton h={44} />
						</div>
					) : installed.isError ? (
						<Notice tone="bad" title="Couldn't list installed plugins">
							{(installed.error as Error).message}. Check that the plugins folder exists and is readable, then reload.
						</Notice>
					) : list.length === 0 ? (
						<div className="panel">
							<EmptyState title="No plugins installed" action={<Button onClick={() => setTab("store")}>Browse the plugin store</Button>}>
								NeoProxy works fully without plugins. Add one only when you need something it doesn't do.
							</EmptyState>
						</div>
					) : (
						<ul className="panel plug-list">
							{list.map((p) => (
								<InstalledRow key={p.id} plugin={p} loadingOn={!!loadingOn} onOpen={() => setOpenId(p.id)} />
							))}
						</ul>
					)}
				</TabPanel>
				<TabPanel value="store">
					<Store installedIds={new Set(list.map((p) => p.id))} onInstalled={() => setTab("installed")} />
				</TabPanel>
			</Tabs>

			<PluginSheet plugin={open} onClose={() => setOpenId(undefined)} />
		</div>
	);
}

function PluginLoading({ enabled, installedCount, dir }: { enabled?: boolean; installedCount: number; dir?: string }) {
	const patch = usePatchConfig();
	const toast = useToast();
	if (enabled === undefined) return null;
	return (
		<div className="panel plug-loading-row">
			<div className="grow">
				<label className="rt-switch-label" htmlFor="plug-loading">
					Load plugins
				</label>
				<p className="faint rt-switch-text">
					{enabled
						? `The engine loads enabled plugins from ${dir ? `the ${dir} folder` : "its plugins folder"} when it starts or when plugins change.`
						: installedCount
							? "Plugin loading is off, so installed plugins don't run even when switched on. Turn it on to use them."
							: "Plugin loading is off. You can still install plugins; they run once this is on and they're enabled."}
				</p>
			</div>
			<Switch
				id="plug-loading"
				checked={enabled}
				disabled={patch.isPending}
				onCheckedChange={async (v) => {
					try {
						await patch.mutateAsync({ patch: { plugins: { enabled: v } } });
						toast(v ? "Plugin loading is on." : "Plugin loading is off. Plugins stop running.");
					} catch (e) {
						toast(`Couldn't change plugin loading: ${(e as Error).message}`, "bad");
					}
				}}
			/>
		</div>
	);
}

function InstalledRow({ plugin, loadingOn, onOpen }: { plugin: InstalledPlugin; loadingOn: boolean; onOpen: () => void }) {
	const setEnabled = useSetPluginEnabled();
	const remove = useRemovePlugin();
	const toast = useToast();
	const [confirm, setConfirm] = useState(false);
	const name = nameOf(plugin);
	const state = plugin.effective_enabled || (plugin.enabled && plugin.registered) ? { tone: "good" as const, text: "Running" } : plugin.enabled ? { tone: "warn" as const, text: loadingOn ? "On, not loaded yet" : "On, loading is off" } : { tone: "neutral" as const, text: "Off" };
	return (
		<li className="plug-row">
			<button className="plug-main" onClick={onOpen}>
				<PlugMark name={name} />
				<span className="plug-text">
					<strong>{name}</strong>
					<span className="faint">
						<code>{plugin.id}</code>
						{plugin.metadata?.version ? `, version ${plugin.metadata.version}` : ""}
					</span>
				</span>
				<Chip tone={state.tone}>{state.text}</Chip>
				<Icon name="chevronRight" size={16} className="faint" />
			</button>
			<Switch
				id={`plug-${plugin.id}`}
				checked={!!plugin.enabled}
				disabled={setEnabled.isPending}
				onCheckedChange={async (v) => {
					try {
						await setEnabled.mutateAsync({ id: plugin.id, enabled: v });
						toast(v ? `${name} is on.${loadingOn ? "" : " Turn on plugin loading for it to run."}` : `${name} is off.`);
					} catch (e) {
						toast(`Couldn't switch ${name}: ${(e as Error).message}`, "bad");
					}
				}}
			/>
			<span className="sr-only">
				<label htmlFor={`plug-${plugin.id}`}>Turn {name} on or off</label>
			</span>
			<IconButton icon="trash" label={`Remove ${name}`} onClick={() => setConfirm(true)} />
			<ConfirmDialog
				open={confirm}
				onOpenChange={setConfirm}
				title={`Remove ${name}?`}
				description="Its files, settings and saved quota state are deleted, and it stops running. You can install it again from the store later."
				confirmLabel="Remove plugin"
				danger
				busy={remove.isPending}
				onConfirm={async () => {
					try {
						await remove.mutateAsync(plugin.id);
						toast(`${name} removed.`);
						setConfirm(false);
					} catch (e) {
						toast(`Couldn't remove ${name}: ${(e as Error).message}`, "bad");
					}
				}}
			/>
		</li>
	);
}

const PAGE = 24;
function Store({ installedIds, onInstalled }: { installedIds: Set<string>; onInstalled: () => void }) {
	const store = usePluginStore();
	const install = useInstallPlugin();
	const toast = useToast();
	const [q, setQ] = useState("");
	const [tag, setTag] = useState("all");
	const [limit, setLimit] = useState(PAGE);
	const [picked, setPicked] = useState<StorePlugin>();
	const all = store.data?.plugins ?? [];
	const tags = useMemo(() => [...new Set(all.flatMap((p) => p.tags ?? []))].sort((a, b) => a.localeCompare(b)), [all]);
	const needle = q.trim().toLowerCase();
	const rows = all.filter((p) => (tag === "all" || p.tags?.includes(tag)) && (!needle || `${p.name} ${p.id} ${p.description} ${p.author}`.toLowerCase().includes(needle)));
	useEffect(() => setLimit(PAGE), [needle, tag]);

	if (store.isPending)
		return (
			<div className="store-grid">
				{Array.from({ length: 6 }, (_, i) => (
					<div key={i} className="panel store-card">
						<Skeleton h={34} w={34} r={9} />
						<Skeleton w="60%" />
						<Skeleton h={40} />
					</div>
				))}
			</div>
		);
	if (store.isError)
		return (
			<div className="panel">
				<EmptyState title="Couldn't reach the plugin store" action={<Button onClick={() => store.refetch()}>Try again</Button>}>
					{(store.error as Error).message}. The store is a list on GitHub, so this server needs internet access to browse it. Installed plugins keep working.
				</EmptyState>
			</div>
		);

	return (
		<div className="stack">
			<div className="row wrap">
				<SearchInput value={q} onChange={setQ} placeholder="Search plugins" />
				<Select
					ariaLabel="Filter by tag"
					className="store-tag"
					value={tag}
					onValueChange={setTag}
					options={[{ value: "all", label: "All kinds" }, ...tags.map((t) => ({ value: t, label: t }))]}
				/>
				<span className="grow" />
				<span className="faint">{store.data?.sources?.length ? `From ${store.data.sources.map((s) => s.name).join(" and ")} store` : ""}</span>
			</div>
			{rows.length === 0 ? (
				<div className="panel">
					<EmptyState title="No plugins match">Try another word, or show all kinds.</EmptyState>
				</div>
			) : (
				<>
					<ul className="store-grid">
						{rows.slice(0, limit).map((p) => {
							const has = p.installed || installedIds.has(p.id);
							return (
								<li key={p.store_id ?? p.id} className="panel store-card">
									<div className="row">
										<PlugMark name={p.name || p.id} />
										<div className="grow store-title">
											<strong>{p.name || p.id}</strong>
											<span className="faint">{p.author ? `by ${p.author}` : p.id}</span>
										</div>
									</div>
									<p className="store-desc muted">{p.description || "No description provided."}</p>
									<div className="row wrap store-tags">
										{p.tags?.slice(0, 3).map((t) => (
											<Chip key={t}>{t}</Chip>
										))}
										{p.version && <span className="faint num">v{p.version.replace(/^v/, "")}</span>}
									</div>
									<div className="store-action">
										{has ? (
											p.update_available ? (
												<Button size="sm" onClick={() => setPicked(p)}>
													Update to {p.version}
												</Button>
											) : (
												<Chip tone="good" icon="check">
													Installed
												</Chip>
											)
										) : (
											<Button size="sm" onClick={() => setPicked(p)}>
												Install
											</Button>
										)}
										{repoUrl(p.repository || p.homepage) && (
											<a className="store-link" href={repoUrl(p.repository || p.homepage)} target="_blank" rel="noreferrer">
												Source
												<Icon name="external" size={13} />
											</a>
										)}
									</div>
								</li>
							);
						})}
					</ul>
					{rows.length > limit && (
						<div className="row store-more">
							<Button variant="ghost" onClick={() => setLimit((n) => n + PAGE * 2)}>
								Show {fmtInt(Math.min(PAGE * 2, rows.length - limit))} more of {fmtInt(rows.length - limit)}
							</Button>
						</div>
					)}
				</>
			)}

			<ConfirmDialog
				open={!!picked}
				onOpenChange={(v) => !v && setPicked(undefined)}
				title={picked ? `${picked.update_available ? "Update" : "Install"} ${picked.name || picked.id}?` : ""}
				description={
					picked ? (
						<>
							NeoProxy downloads version {picked.version} {picked.repository ? <>from <code>{picked.repository.replace(/^https:\/\/github.com\//, "")}</code></> : "from the store"} into its plugins folder.{" "}
							{picked.update_available ? "Its settings are kept." : "It's installed switched off; turn it on from Installed when you're ready."}
						</>
					) : (
						""
					)
				}
				confirmLabel={picked?.update_available ? "Update plugin" : "Install plugin"}
				busy={install.isPending}
				onConfirm={async () => {
					if (!picked) return;
					try {
						await install.mutateAsync({ id: picked.id, source: picked.source_id });
						toast(`${picked.name || picked.id} installed.`);
						setPicked(undefined);
						onInstalled();
					} catch (e) {
						toast(`Couldn't install ${picked.name || picked.id}: ${(e as Error).message}`, "bad");
					}
				}}
			>
				{picked && (
					<div className="stack">
						<Notice tone="warn">Plugins run inside the engine with access to every request and response. Install only from authors you trust{picked.license ? `; this one is ${picked.license} licensed` : ""}.</Notice>
						{picked.auth_required && !picked.auth_configured && (
							<Notice tone="info" title="Needs credentials">
								This plugin's download requires authentication that isn't set up yet. Add a matching entry under <code>plugins.store-auth</code> in Configuration first, or the install will fail.
							</Notice>
						)}
					</div>
				)}
			</ConfirmDialog>
		</div>
	);
}

function PluginSheet({ plugin, onClose }: { plugin?: InstalledPlugin; onClose: () => void }) {
	const [shown, setShown] = useState(plugin);
	useEffect(() => {
		if (plugin) setShown(plugin);
	}, [plugin]);
	const p = plugin ?? shown;
	return (
		<Dialog sheet open={!!plugin} onOpenChange={(v) => !v && onClose()} title={p ? nameOf(p) : ""} description={p ? "An installed engine plugin. Switch it on or off from the list." : undefined}>
			{p && (
				<>
					<Facts
						items={[
							["Plugin id", <code key="id">{p.id}</code>],
							["Version", p.metadata?.version || "Not reported"],
							["Author", p.metadata?.author || "Not reported"],
							["State", p.effective_enabled ? "Running" : p.enabled ? "On, not running" : "Off"],
							["Adds", [p.supports_quota && "Quota reporting", p.supports_oauth && `Sign-in for ${p.oauth_provider || "a provider"}`].filter(Boolean).join(", ") || "Request handling"],
							["Source", repoUrl(p.metadata?.github_repository) ? <a href={repoUrl(p.metadata?.github_repository)} target="_blank" rel="noreferrer">{p.metadata?.github_repository}</a> : "Not reported"],
						]}
					/>
					{p.path && (
						<p className="faint plug-path">
							Installed at <code>{p.path}</code>
						</p>
					)}
					{(p.config_fields?.length ?? 0) > 0 && (
						<section className="stack">
							<h3 className="sys-sub">Settings it reads</h3>
							<ul className="plug-fields">
								{p.config_fields!.map((f) => (
									<li key={f.name}>
										<code>{f.name}</code>
										{f.type && <span className="faint"> {f.type}</span>}
										{f.description && <p className="muted">{f.description}</p>}
									</li>
								))}
							</ul>
							<p className="faint">
								Change these under <code>plugins.configs.{p.id}</code> in <a href="#/system/configuration">Configuration</a>.
							</p>
						</section>
					)}
					{p.supports_quota && <QuotaBlock plugin={p} />}
				</>
			)}
		</Dialog>
	);
}

function QuotaBlock({ plugin }: { plugin: InstalledPlugin }) {
	const creds = useEngineCredentials();
	const toast = useToast();
	const clear = useClearPluginQuota();
	const [confirm, setConfirm] = useState(false);
	const options = (creds.data ?? []).filter((c) => c.auth_index && (!plugin.quota_provider || c.provider === plugin.quota_provider));
	const [authIndex, setAuthIndex] = useState<string>();
	const quota = usePluginQuota(plugin.id, authIndex);
	const picked = options.find((o) => o.auth_index === authIndex);
	return (
		<section className="stack">
			<h3 className="sys-sub">Quota</h3>
			<p className="muted">This plugin reports how much of an account's allowance is left. Quota is tracked per account, so pick one.</p>
			{options.length === 0 ? (
				<p className="faint">{creds.isPending ? "Loading accounts…" : `No ${plugin.quota_provider ? `${plugin.quota_provider} ` : ""}accounts in the pool to check.`}</p>
			) : (
				<Select
					ariaLabel="Account"
					value={authIndex}
					onValueChange={setAuthIndex}
					placeholder="Choose an account"
					options={options.map((o) => ({ value: o.auth_index!, label: o.label || o.email || o.name, description: o.provider }))}
				/>
			)}
			{authIndex &&
				(quota.isPending ? (
					<Skeleton h={60} />
				) : quota.isError ? (
					<Notice tone="bad" title="Couldn't read quota">
						{(quota.error as Error).message}
					</Notice>
				) : (
					<Facts items={Object.entries(quota.data ?? {}).map(([k, v]) => [k, <code key={k}>{typeof v === "object" ? JSON.stringify(v) : String(v)}</code>])} />
				))}
			{authIndex && (
				<div>
					<Button variant="danger" size="sm" onClick={() => setConfirm(true)}>
						Clear saved quota
					</Button>
				</div>
			)}
			<ConfirmDialog
				open={confirm}
				onOpenChange={setConfirm}
				title="Clear saved quota for this account?"
				description={`The plugin forgets what it knew about ${picked?.label || picked?.name || "this account"}'s allowance and checks again on the next request. The account itself isn't changed.`}
				confirmLabel="Clear quota"
				danger
				busy={clear.isPending}
				onConfirm={async () => {
					try {
						await clear.mutateAsync({ pluginId: plugin.id, authIndex: authIndex! });
						toast("Saved quota cleared.");
						setConfirm(false);
					} catch (e) {
						toast(`Couldn't clear quota: ${(e as Error).message}`, "bad");
					}
				}}
			/>
		</section>
	);
}
