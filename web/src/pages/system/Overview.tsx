import { useEffect, useState } from "react";
import { useCore } from "../../api/queries";
import { ago, plural } from "../../lib/format";
import { Chip, CodeLine, Notice, Skeleton, StatusDot } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useEngineConfig, useEngineCredentials, usePlugins } from "./api";
import { Facts, SubHead, clientBase, parseVersion } from "./shared";
import { SECTIONS, type SectionId } from "./sections";

const ENDPOINTS = [
	{ path: "/v1", name: "OpenAI", what: "Chat Completions and model lists. Most apps and SDKs that say “OpenAI-compatible” want this one." },
	{ path: "/v1/messages", name: "Anthropic", what: "The Messages API, for Claude Code and Anthropic SDKs. Point their base URL at the server root." },
	{ path: "/v1beta", name: "Gemini", what: "Google's generateContent API, for Gemini CLI and Google SDKs." },
	{ path: "/v1/responses", name: "Responses", what: "OpenAI's newer Responses API, used by Codex CLI and agents built on it." },
];

const STRATEGY_NAMES: Record<string, string> = { "round-robin": "Round robin", "weighted-round-robin": "Weighted round robin", "fill-first": "Fill first" };

/** Re-render periodically so relative times stay honest. */
function useTick(ms: number) {
	const [, set] = useState(0);
	useEffect(() => {
		const t = setInterval(() => set((n) => n + 1), ms);
		return () => clearInterval(t);
	}, [ms]);
}

export function Overview() {
	useTick(10_000);
	const core = useCore();
	const cfg = useEngineConfig();
	const creds = useEngineCredentials();
	const plugins = usePlugins();
	const base = clientBase(core.data?.endpoint);
	const c = cfg.data?.config;
	const host = c?.server?.host;
	const localOnly = host === "127.0.0.1" || host === "localhost" || host === "::1";
	// The core endpoint answers with the server's clock; a successful answer is the heartbeat.
	const heartbeat = core.dataUpdatedAt || undefined;
	const stale = heartbeat ? Date.now() - heartbeat > 90_000 : false;
	const version = parseVersion(core.data?.version);
	const tone = core.isError ? "bad" : !core.data ? "idle" : stale ? "warn" : "good";
	const statusText = core.isError ? "Not responding" : !core.data ? "Checking…" : stale ? "No answer for a while" : "Running normally";

	const cooling = (creds.data ?? []).filter((x) => (x.cooldowns?.length ?? 0) > 0).length;
	const keys = c?.access?.["api-keys"]?.length ?? 0;
	const logs = c?.observability?.logs;
	const installed = plugins.data?.plugins?.length ?? 0;

	const summary: Partial<Record<SectionId, React.ReactNode>> = {
		routing: c ? (
			<>
				{STRATEGY_NAMES[c.routing?.strategy ?? "round-robin"] ?? c.routing?.strategy}
				{cooling > 0 && <Chip tone="warn">{plural(cooling, "account")} cooling down</Chip>}
			</>
		) : null,
		"server-keys": c ? keys === 0 ? "None, which is the usual setup" : <Chip tone="warn">{plural(keys, "key")} active</Chip> : null,
		logs: c ? (logs?.["logging-to-file"] ? "Writing to file" : "File logging off") : null,
		plugins: plugins.data ? (installed ? `${plural(installed, "plugin")} installed${plugins.data.plugins_enabled ? "" : ", loading off"}` : "None installed") : null,
		configuration: c ? `config.yaml, schema version ${c["config-version"] ?? "unknown"}` : null,
		about: version ? `NeoProxy ${version.release}${version.channel ? ` ${version.channel}` : ""}` : null,
	};

	return (
		<div className="stack-lg">
			<SubHead title="Overview" lede="Is the server healthy, and where do apps connect? Everything else on this page is reachable from the cards below." />

			<div className="stack">
				<section className="panel sys-card" aria-labelledby="sys-server">
					<header className="sys-card-head">
						<h3 id="sys-server">Server</h3>
						<span className="sys-status">
							<StatusDot tone={tone} />
							{statusText}
						</span>
					</header>
					{core.isPending ? (
						<div className="stack">
							<Skeleton w="60%" />
							<Skeleton w="40%" />
							<Skeleton w="70%" />
						</div>
					) : core.isError ? (
						<Notice tone="bad" title="Couldn't reach NeoProxy">
							{(core.error as Error).message}. If the service is restarting this clears on its own; otherwise check the service with <code>systemctl --user status neoproxy-office</code>.
						</Notice>
					) : (
						<Facts
							items={[
								["Machine", core.data?.host ?? "Unknown"],
								["Last answered", heartbeat ? <span title={new Date(heartbeat).toLocaleString()}>{ago(heartbeat)}</span> : "Not reported"],
								[
									"Version",
									version ? (
										<span className="sys-version">
											{version.release}
											{version.channel && <Chip>{version.channel}</Chip>}
											{version.date && <span className="faint">{version.date}</span>}
										</span>
									) : (
										"Not reported"
									),
								],
								["Listening on", c?.server ? <code>{`${c.server.host || "all interfaces"}:${c.server.port ?? ""}`}</code> : <Skeleton w={120} />],
								["Identity", <code key="i">{(core.data as { identity?: string } | undefined)?.identity ?? "Not reported"}</code>],
							]}
						/>
					)}
					<p className="sys-card-foot faint">The dashboard checks in with the server every 30 seconds while this page is open. NeoProxy doesn't report how long it has been running.</p>
				</section>

				<section className="panel sys-card" aria-labelledby="sys-connect">
					<header className="sys-card-head">
						<h3 id="sys-connect">Where apps connect</h3>
					</header>
					<p className="muted sys-card-intro">
						Give an app the address for the API it speaks, plus a person's inference key from <a href="#/people">People</a>. Requests then count against that person and the models they can use.
					</p>
					<ul className="endpoints">
						{ENDPOINTS.map((e) => (
							<li key={e.path}>
								<div className="endpoint-head">
									<strong>{e.name}</strong>
									<span className="faint">{e.what}</span>
								</div>
								<CodeLine>{`${base}${e.path}`}</CodeLine>
							</li>
						))}
					</ul>
					{localOnly && (
						<Notice tone="info" title="Only this computer can connect">
							The server listens on <code>{host}</code>, so apps on other machines can't reach it. To serve the office, change <code>server.host</code> to <code>0.0.0.0</code> or this machine's LAN address in{" "}
							<a href="#/system/configuration">Configuration</a>, then restart NeoProxy.
						</Notice>
					)}
				</section>
			</div>

			<section aria-labelledby="sys-parts" className="stack">
				<h3 id="sys-parts" className="sys-h3">
					Everything else
				</h3>
				<ul className="sys-cards">
					{SECTIONS.filter((s) => s.id !== "overview").map((s) => (
						<li key={s.id}>
							<a className="sys-link-card panel" href={`#/system/${s.id}`}>
								<span className="sys-link-title">
									{s.title}
									<Icon name="chevronRight" size={16} className="sys-link-chev" />
								</span>
								<span className="sys-link-when">{s.when}</span>
								<span className="sys-link-now">{summary[s.id] ?? <Skeleton w={120} h={12} />}</span>
							</a>
						</li>
					))}
				</ul>
			</section>
		</div>
	);
}
