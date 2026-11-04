import { useCore } from "../../api/queries";
import { Chip, CopyButton, Kbd, Skeleton } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useEngineConfig, useLatestVersion } from "./api";
import { Facts, SubHead, parseVersion } from "./shared";

const REPO = "https://github.com/PsychedelicShayna/NeoProxy";
const ENGINE = "https://github.com/router-for-me/CLIProxyAPI";

function Ext({ href, children }: { href: string; children: string }) {
	return (
		<a href={href} target="_blank" rel="noreferrer" className="about-ext">
			{children}
			<Icon name="external" size={13} />
		</a>
	);
}

export function About() {
	const core = useCore();
	const cfg = useEngineConfig();
	const latest = useLatestVersion();
	const v = parseVersion(core.data?.version);
	const panelOn = cfg.data && !cfg.data.config.management?.["disable-control-panel"];
	return (
		<div className="stack-lg">
			<SubHead title="About" lede="What NeoProxy is, which version is running, and where to look next." />

			<div className="sys-grid-2">
				<section className="panel sys-card" aria-labelledby="ab-what">
					<header className="sys-card-head">
						<h3 id="ab-what">What NeoProxy is</h3>
					</header>
					<div className="about-copy">
						<p>
							NeoProxy is a shared pool of AI provider access for a small office. People add the subscriptions and API keys they have, NeoProxy turns them into named models, and everyone gets their own inference key. Nobody shares passwords, and each request is recorded under the person who made it.
						</p>
						<p className="muted">
							It's PsychedelicShayna's fork of CLIProxyAPI, the Go engine that speaks the OpenAI, Anthropic and Gemini APIs. NeoPi, NeoDeck, NeoCode and any compatible app can use it.
						</p>
					</div>
					<Facts
						items={[
							["Source code", <Ext key="r" href={REPO}>PsychedelicShayna/NeoProxy</Ext>],
							["Engine it builds on", <Ext key="e" href={ENGINE}>router-for-me/CLIProxyAPI</Ext>],
							["Design language", <Ext key="d" href={`${REPO}/blob/nightly/docs/design/neo-design-language.md`}>Neo design language</Ext>],
							[
								"Engine's original panel",
								panelOn ? (
									<a key="p" href="/management.html">
										Open the legacy control panel
									</a>
								) : cfg.data ? (
									<span className="faint">Turned off in Configuration</span>
								) : (
									<Skeleton w={140} />
								),
							],
						]}
					/>
				</section>

				<section className="panel sys-card" aria-labelledby="ab-version">
					<header className="sys-card-head">
						<h3 id="ab-version">Version</h3>
					</header>
					{core.isPending ? (
						<div className="stack">
							<Skeleton w="50%" />
							<Skeleton w="70%" />
						</div>
					) : (
						<Facts
							items={[
								[
									"Running",
									v ? (
										<span className="sys-version" key="v">
											NeoProxy {v.release}
											{v.channel && <Chip>{v.channel}</Chip>}
										</span>
									) : (
										"Not reported"
									),
								],
								["Built", v?.date ?? "Not reported"],
								["Build", v?.commit ? <code key="c">{v.commit}</code> : v?.flavor ?? "Not reported"],
								[
									"Full version",
									core.data?.version ? (
										<span className="row" key="f">
											<code className="about-full">{core.data.version}</code>
											<CopyButton text={core.data.version} label="Copy version" />
										</span>
									) : (
										"Not reported"
									),
								],
							]}
						/>
					)}
					<div className="about-upstream">
						<p className="about-upstream-head">
							<span>Newest CLIProxyAPI release</span>
							{latest.isPending ? <Skeleton w={70} /> : latest.data ? <code>{latest.data}</code> : <span className="faint">Couldn't check</span>}
						</p>
						<p className="faint">
							{latest.isError
								? "The server couldn't reach GitHub to check. That's fine on an offline network."
								: "For reference. NeoProxy merges upstream engine releases into its nightly builds and numbers its own versions separately, so the two don't compare directly."}
						</p>
					</div>
				</section>
			</div>

			<section className="panel sys-card" aria-labelledby="ab-keys">
				<header className="sys-card-head">
					<h3 id="ab-keys">Keyboard</h3>
				</header>
				<ul className="about-keys">
					<li>
						<span className="about-chord">
							<Kbd>Ctrl</Kbd>
							<Kbd>S</Kbd>
						</span>
						<span>Save while editing routing or config.yaml. On a Mac, use Command instead of Ctrl.</span>
					</li>
					<li>
						<span className="about-chord">
							<Kbd>Tab</Kbd>
						</span>
						Indent by two spaces inside the config.yaml editor
					</li>
					<li>
						<span className="about-chord">
							<Kbd>Esc</Kbd>
							<Kbd>Tab</Kbd>
						</span>
						Leave the config.yaml editor and move to the next control
					</li>
					<li>
						<span className="about-chord">
							<Kbd>←</Kbd>
							<Kbd>→</Kbd>
						</span>
						Move between choices in a group, such as the log filter
					</li>
					<li>
						<span className="about-chord">
							<Kbd>Esc</Kbd>
						</span>
						Close a dialog or side panel
					</li>
				</ul>
			</section>
		</div>
	);
}
