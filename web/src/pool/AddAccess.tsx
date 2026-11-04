import { AnimatePresence, motion } from "motion/react";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError } from "../api/client";
import { signIn, useAddApiKey, useConnectLink, useConnectLocalNeoPi, useImport, useImportCandidates, useLocalNeoPi, useProviders, useSaveSource, useUploadFile } from "../api/queries";
import type { AccessKind, Person, ProviderInfo, Session } from "../api/types";
import { providerColor, providerInfo } from "../lib/providers";
import { Button, CodeLine, Dialog, Field, Input, Notice, ProviderMark, Select, cx } from "../ui/components";
import { Icon } from "../ui/icons";
import { useToast } from "../ui/toast";
import { KIND_COPY } from "./copy";

type Step = { method?: AccessKind; done?: { title: string; text: ReactNode } };
const ORDER: AccessKind[] = ["subscription", "apiKey", "neopi", "imported", "file"];
// Older servers accept API keys for these three engine providers only.
const LEGACY_KEY_PROVIDERS: Record<string, string> = { openai: "codex", claude: "claude", gemini: "gemini" };

export function AddAccessDialog({
	open,
	onOpenChange,
	initialMethod,
	initialProvider,
	session,
	people,
	legacy,
}: {
	open: boolean;
	onOpenChange: (v: boolean) => void;
	initialMethod?: AccessKind;
	initialProvider?: string;
	session: Session;
	people: Person[];
	legacy?: boolean;
}) {
	const [step, setStep] = useState<Step>({});
	useEffect(() => {
		if (open) setStep({ method: initialMethod });
	}, [open, initialMethod]);
	const method = step.method;
	const title = step.done ? step.done.title : method ? KIND_COPY[method].title : "Add provider access";
	const description = step.done ? undefined : method ? KIND_COPY[method].detail : "Choose how this access reaches NeoProxy. Every way ends up in the same pool, and you can mix them freely.";
	const finish = (title: string, text: ReactNode) => setStep({ method, done: { title, text } });

	return (
		<Dialog open={open} onOpenChange={onOpenChange} wide={!method && !step.done} title={title} description={description}>
			<AnimatePresence mode="wait" initial={false}>
				<motion.div
					key={step.done ? "done" : method ?? "choose"}
					className="flow"
					initial={{ opacity: 0, x: method ? 16 : -16 }}
					animate={{ opacity: 1, x: 0 }}
					exit={{ opacity: 0, x: method ? -16 : 16 }}
					transition={{ duration: 0.18, ease: [0.22, 1, 0.36, 1] }}
				>
					{step.done ? (
						<Done text={step.done.text} onAnother={() => setStep({})} onClose={() => onOpenChange(false)} />
					) : !method ? (
						<Choose onChoose={(m) => setStep({ method: m })} />
					) : (
						<>
							<button className="back-link" onClick={() => setStep({})}>
								<Icon name="back" size={15} /> All ways to add access
							</button>
							{method === "subscription" && <SignInFlow initialProvider={initialProvider} onDone={finish} />}
							{method === "apiKey" && <ApiKeyFlow session={session} people={people} legacy={legacy} onDone={finish} />}
							{method === "neopi" && <NeoPiShareFlow session={session} people={people} onDone={finish} />}
							{method === "imported" && <ImportFlow session={session} people={people} onDone={finish} onSignIn={() => setStep({ method: "subscription" })} />}
							{method === "file" && <FileFlow session={session} people={people} onDone={finish} />}
						</>
					)}
				</motion.div>
			</AnimatePresence>
		</Dialog>
	);
}

function Choose({ onChoose }: { onChoose: (m: AccessKind) => void }) {
	return (
		<ul className="choose">
			{ORDER.map((k) => (
				<li key={k}>
					<button className="choice" onClick={() => onChoose(k)}>
						<span className={`choice-swatch kind-${k}`} aria-hidden="true">
							{k === "neopi" && <Icon name="link" size={16} />}
							{k === "imported" && <Icon name="lock" size={16} />}
							{k === "file" && <Icon name="file" size={16} />}
						</span>
						<span className="choice-text">
							<span className="choice-title">{KIND_COPY[k].title}</span>
							<span className="choice-summary">{KIND_COPY[k].summary}</span>
							<span className="choice-good">Good for: {KIND_COPY[k].goodFor}</span>
						</span>
						<Icon name="chevronRight" size={18} className="choice-arrow" />
					</button>
				</li>
			))}
		</ul>
	);
}

function Done({ text, onAnother, onClose }: { text: ReactNode; onAnother: () => void; onClose: () => void }) {
	return (
		<div className="done">
			<motion.span className="done-mark" initial={{ scale: 0.6, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} transition={{ type: "spring", stiffness: 380, damping: 20 }}>
				<Icon name="check" size={26} />
			</motion.span>
			<div className="done-text">{text}</div>
			<div className="row">
				<Button onClick={onAnother} icon="plus">
					Add more access
				</Button>
				<Button variant="primary" onClick={onClose}>
					Back to the pool
				</Button>
			</div>
		</div>
	);
}

function providerOptions(list: ProviderInfo[]) {
	return list.map((p) => ({
		value: p.id,
		label: p.name,
		description: p.vendor !== p.name ? p.vendor : undefined,
		leading: <ProviderMark provider={p.id} name={p.name} hue={p.hue} size={22} />,
	}));
}

function ContributorField({ session, people, value, onChange }: { session: Session; people: Person[]; value: string; onChange: (v: string) => void }) {
	if (session.role !== "admin" || people.length < 2) return null;
	return (
		<Field label="Contributor" hint="Whose access this is. It's listed under their name and they can see its usage." htmlFor="contributor">
			<Select id="contributor" value={value} onValueChange={onChange} options={people.map((p) => ({ value: p.id, label: p.name || p.handle, description: p.id === session.userId ? "You" : p.handle }))} />
		</Field>
	);
}

// ---------- Sign in with an account ----------
function SignInFlow({ initialProvider, onDone }: { initialProvider?: string; onDone: (t: string, text: ReactNode) => void }) {
	const providers = useProviders().filter((p) => p.methods.includes("subscription"));
	const [provider, setProvider] = useState(initialProvider && providers.some((p) => p.id === initialProvider) ? initialProvider : providers[0]?.id);
	const [phase, setPhase] = useState<"idle" | "starting" | "waiting" | "error">("idle");
	const [session, setSession] = useState<{ state: string; url?: string; userCode?: string; device?: boolean }>();
	const [error, setError] = useState<string>();
	const [code, setCode] = useState("");
	const popup = useRef<Window | null>(null);
	const info = provider ? providerInfo(provider, providers) : undefined;

	useEffect(() => {
		if (phase !== "waiting" || !session) return;
		let stop = false;
		const tick = async () => {
			try {
				const s = await signIn.status(session.state);
				if (stop) return;
				if (s.status === "ok") {
					onDone("Signed in", (
						<>
							<p>
								<strong>{info?.name}</strong> is now part of the pool, listed under your name. NeoProxy refreshes the sign-in automatically.
							</p>
							<p className="faint">Its plan is detected from the account. You can reassign it or change the plan from its tile.</p>
						</>
					));
					return;
				}
				if (s.status === "error") {
					setError(s.error || "The provider reported an error.");
					setPhase("error");
					return;
				}
			} catch (e) {
				if (!stop && e instanceof ApiError && e.status !== 404) {
					setError(e.message);
					setPhase("error");
					return;
				}
			}
			if (!stop) setTimeout(tick, 1500);
		};
		const t = setTimeout(tick, 1200);
		return () => {
			stop = true;
			clearTimeout(t);
		};
	}, [phase, session, info?.name, onDone]);

	async function start() {
		if (!provider) return;
		setError(undefined);
		setPhase("starting");
		// Open the tab inside the click so browsers don't block it.
		popup.current = window.open("about:blank", "_blank");
		try {
			const r = await signIn.start(provider);
			const device = r.status === "ok" && !!r.user_code;
			setSession({ state: r.state, url: r.url, userCode: r.user_code, device });
			if (r.url && popup.current) popup.current.location.href = r.url;
			else popup.current?.close();
			setPhase("waiting");
		} catch (e) {
			popup.current?.close();
			setError((e as Error).message);
			setPhase("error");
		}
	}

	async function submitCode() {
		if (!provider || !session) return;
		const raw = code.trim();
		const fromUrl = (() => {
			try {
				return new URL(raw).searchParams.get("code") ?? raw;
			} catch {
				return raw;
			}
		})();
		try {
			await signIn.submitCode(provider, session.state, fromUrl);
		} catch (e) {
			setError((e as Error).message);
		}
	}

	return (
		<div className="stack">
			<Field label="Provider" htmlFor="sub-provider">
				<Select id="sub-provider" value={provider} onValueChange={(v) => { setProvider(v); setPhase("idle"); setSession(undefined); }} options={providerOptions(providers)} />
			</Field>
			{phase === "idle" || phase === "starting" ? (
				<>
					<ol className="steps">
						<li>{info?.subscription?.flow === "device" ? `A short code appears here; confirm it on ${info.vendor}'s site.` : `${info?.vendor ?? "The provider"}'s sign-in page opens in a new tab.`}</li>
						<li>Sign in with the account you want to share and approve access.</li>
						<li>Come back to this tab. It notices the sign-in by itself and adds the account to the pool under your name.</li>
					</ol>
					<div className="row flow-actions">
						<Button variant="primary" size="lg" icon="signin" busy={phase === "starting"} onClick={start} disabled={!provider}>
							Continue to {info?.vendor ?? "provider"}
						</Button>
					</div>
				</>
			) : phase === "waiting" && session ? (
				<div className="waiting">
					<div className="waiting-head">
						<span className="pulse-ring" style={{ ["--tile" as string]: providerColor(provider!, info?.hue) }} />
						<div>
							<strong>Waiting for {info?.vendor} sign-in</strong>
							<p className="muted">Finish on the tab that just opened. This updates on its own.</p>
						</div>
					</div>
					{session.userCode && (
						<div className="device-code">
							<span className="faint">Confirm this code on {info?.vendor}'s page</span>
							<span className="device-code-value mono">{session.userCode}</span>
						</div>
					)}
					{session.url && (
						<p className="faint">
							No tab opened?{" "}
							<a href={session.url} target="_blank" rel="noreferrer">
								Open the sign-in page <Icon name="external" size={13} />
							</a>
						</p>
					)}
					{!session.device && (
						<details className="disclosure">
							<summary>Signed in, but this page didn't move on?</summary>
							<p className="faint">If the provider's page ended on an address that didn't load (for example on another computer), copy that full address and paste it here.</p>
							<div className="row">
								<Input mono value={code} onChange={(e) => setCode(e.target.value)} placeholder="http://localhost:1455/auth/callback?code=…" />
								<Button onClick={submitCode} disabled={!code.trim()}>
									Finish sign-in
								</Button>
							</div>
						</details>
					)}
				</div>
			) : null}
			{phase === "error" && error && (
				<Notice tone="bad" title="Sign-in didn't complete" action={<Button size="sm" onClick={() => setPhase("idle")}>Try again</Button>}>
					{error}
				</Notice>
			)}
		</div>
	);
}

// ---------- Paste an API key ----------
function ApiKeyFlow({ session, people, legacy, onDone }: { session: Session; people: Person[]; legacy?: boolean; onDone: (t: string, text: ReactNode) => void }) {
	const all = useProviders().filter((p) => p.methods.includes("apiKey"));
	const providers = legacy ? all.filter((p) => LEGACY_KEY_PROVIDERS[p.id]) : all;
	const [provider, setProvider] = useState(providers[0]?.id);
	const [key, setKey] = useState("");
	const [reveal, setReveal] = useState(false);
	const [baseUrl, setBaseUrl] = useState("");
	const [label, setLabel] = useState("");
	const [budget, setBudget] = useState("");
	const [owner, setOwner] = useState(session.userId);
	const add = useAddApiKey();
	const info = provider ? providerInfo(provider, providers) : undefined;
	const editable = info?.apiKey?.editableBaseUrl;

	async function submit() {
		if (!provider) return;
		try {
			await add.mutateAsync({
				provider: legacy ? LEGACY_KEY_PROVIDERS[provider] : provider,
				apiKey: key.trim(),
				baseUrl: editable ? baseUrl.trim() : undefined,
				label: label.trim() || undefined,
				ownerId: owner,
				monthlyUSD: budget ? Number(budget) : undefined,
			});
			onDone("API key added", (
				<p>
					<strong>{info?.name}</strong> checked out and is now in the pool. Its models are available to virtual models and grants right away.
				</p>
			));
		} catch {
			/* error rendered below */
		}
	}

	return (
		<div className="stack">
			<Field label="Provider" htmlFor="key-provider" hint={legacy ? "This server version accepts keys for these three providers. Updating NeoProxy adds OpenRouter, DeepSeek, Groq and other OpenAI-compatible providers." : undefined}>
				<Select id="key-provider" value={provider} onValueChange={setProvider} options={providerOptions(providers)} />
			</Field>
			{editable && (
				<Field
					label="Server address"
					hint={
						info?.id === "custom"
							? "The OpenAI-compatible base URL, usually ending in /v1. For a local Ollama: http://127.0.0.1:11434/v1"
							: `The gateway's base URL. Leave empty for the default, ${info?.apiKey?.baseUrl}`
					}
					htmlFor="base-url"
				>
					<Input id="base-url" mono value={baseUrl} onChange={(e) => setBaseUrl(e.target.value)} placeholder={info?.id === "custom" ? "https://example.com/v1" : info?.apiKey?.baseUrl} />
				</Field>
			)}
			<Field
				label="API key"
				htmlFor="api-key"
				hint={
					<>
						Stored on this server only and never shown again.{" "}
						{info?.apiKey?.getKeyUrl && (
							<a href={info.apiKey.getKeyUrl} target="_blank" rel="noreferrer">
								Get a {info.vendor} key <Icon name="external" size={12} />
							</a>
						)}
					</>
				}
			>
				<div className="row">
					<Input id="api-key" mono type={reveal ? "text" : "password"} autoComplete="off" value={key} onChange={(e) => setKey(e.target.value)} placeholder={info?.apiKey?.keyHint} />
					<Button variant="ghost" onClick={() => setReveal(!reveal)}>
						{reveal ? "Hide" : "Show"}
					</Button>
				</div>
			</Field>
			<div className="grid-2">
				<Field label="Name" optional hint="Helps tell keys apart, like “Team research”." htmlFor="key-label">
					<Input id="key-label" value={label} onChange={(e) => setLabel(e.target.value)} />
				</Field>
				<Field label="Monthly budget" optional hint="US dollars. Only sizes this key's tile in the pool." htmlFor="key-budget">
					<Input id="key-budget" type="number" min={0} value={budget} onChange={(e) => setBudget(e.target.value)} placeholder="20" />
				</Field>
			</div>
			<ContributorField session={session} people={people} value={owner} onChange={setOwner} />
			{add.error && (
				<Notice tone="bad" title="The key wasn't added">
					{(add.error as Error).message}
				</Notice>
			)}
			<div className="row flow-actions">
				<Button variant="primary" size="lg" busy={add.isPending} disabled={!key.trim() && !(info?.apiKey?.keyOptional ?? editable)} onClick={submit}>
					Check and add key
				</Button>
			</div>
		</div>
	);
}

// ---------- Share from a NeoPi ----------
// Easiest first: a NeoPi on this computer is one click; another computer is one
// pasted link from `npi auth-gateway link`; address and token remain as a fallback.
function NeoPiShareFlow({ session, people, onDone }: { session: Session; people: Person[]; onDone: (t: string, text: ReactNode) => void }) {
	const [owner, setOwner] = useState(session.userId);
	const [link, setLink] = useState("");
	const [manual, setManual] = useState(false);
	const local = useLocalNeoPi(session.role === "admin");
	const connectLocal = useConnectLocalNeoPi();
	const connectLink = useConnectLink();
	const detected = local.data;
	const linkLooksRight = /^neopi-gateway:\/\//i.test(link.trim());
	const linkName = (() => {
		try {
			return new URL(link.trim().replace(/^neopi-gateway:/i, "http:")).searchParams.get("name") ?? "";
		} catch {
			return "";
		}
	})();

	const connected = (name: string, count?: number) =>
		onDone("NeoPi connected", (
			<>
				<p>
					<strong>{name}</strong> is connected.{" "}
					{count !== undefined ? `${count} signed-in ${count === 1 ? "account" : "accounts"} will appear in the pool, each as its own tile.` : "Its signed-in accounts appear in the pool within about 15 seconds, each as its own tile."}
				</p>
				<p className="faint">Accounts stay in NeoPi. If that NeoPi goes offline, its tiles pause until it's back. Give each one a budget so virtual models can use it.</p>
			</>
		));

	return (
		<div className="stack">
			{detected?.found && (
				<div className={cx("local-neopi", detected.running && !detected.alreadyConnected && "is-ready")}>
					<span className="local-neopi-mark" aria-hidden="true">
						<Icon name="link" size={18} />
					</span>
					<div className="grow">
						<strong>{detected.alreadyConnected ? "The NeoPi on this computer is already connected" : detected.running ? `NeoPi on this computer${detected.name ? `: ${detected.name}` : ""}` : "NeoPi is installed here, but its gateway isn't running"}</strong>
						<p className="muted">
							{detected.alreadyConnected
								? "Its accounts are already in the pool."
								: detected.running
									? `${detected.accountCount !== undefined ? `${detected.accountCount} signed-in ${detected.accountCount === 1 ? "account" : "accounts"} ready to share. ` : ""}NeoProxy reads the gateway's token itself; you don't need to copy anything.`
									: detected.reason ?? "Start the gateway, then check again."}
						</p>
						{!detected.running && detected.startCommand && <CodeLine>{detected.startCommand}</CodeLine>}
					</div>
					{detected.running && !detected.alreadyConnected ? (
						<Button
							variant="primary"
							icon="link"
							busy={connectLocal.isPending}
							onClick={() =>
								connectLocal
									.mutateAsync({ ownerId: owner })
									.then((r) => connected(r.label || detected.name || "NeoPi on this computer", detected.accountCount))
									.catch(() => {})
							}
						>
							Connect
						</Button>
					) : !detected.running ? (
						<Button icon="refresh" busy={local.isFetching} onClick={() => local.refetch()}>
							Check again
						</Button>
					) : null}
				</div>
			)}
			{connectLocal.error && (
				<Notice tone="bad" title="Couldn't connect the local NeoPi">
					{(connectLocal.error as Error).message}
				</Notice>
			)}

			<div className="share-link">
				<p className="field-label">{detected?.found ? "NeoPi on another computer" : "Connect a NeoPi"}</p>
				<ol className="steps">
					<li>
						On the computer running NeoPi, start its gateway so other machines can reach it:
						<CodeLine>npi auth-gateway serve --bind=0.0.0.0:4000</CodeLine>
					</li>
					<li>
						Print a connection link and paste it below:
						<CodeLine>npi auth-gateway link</CodeLine>
					</li>
				</ol>
				<Field label="Connection link" hint="It contains the gateway token, so treat it like a password. NeoProxy keeps it on the server and never shows it again." htmlFor="gw-link">
					<Input id="gw-link" mono type="password" autoComplete="off" value={link} onChange={(e) => setLink(e.target.value)} placeholder="neopi-gateway://192.168.1.20:4000?token=…" />
				</Field>
				{link.trim() && !linkLooksRight && <p className="field-error">That doesn't look like a connection link. It starts with neopi-gateway://</p>}
			</div>

			<ContributorField session={session} people={people} value={owner} onChange={setOwner} />
			{connectLink.error && (
				<Notice tone="bad" title="Couldn't connect to that NeoPi">
					{(connectLink.error as Error).message}
				</Notice>
			)}
			<div className="row flow-actions">
				<button type="button" className="link-btn" onClick={() => setManual(!manual)}>
					{manual ? "Hide address and token" : "Enter an address and token instead"}
				</button>
				<span className="grow" />
				<Button
					variant="primary"
					size="lg"
					icon="link"
					busy={connectLink.isPending}
					disabled={!linkLooksRight}
					onClick={() =>
						connectLink
							.mutateAsync({ connectionLink: link.trim(), ownerId: owner })
							.then((r) => connected(r.label || linkName || "NeoPi", r.accountCount))
							.catch(() => {})
					}
				>
					Connect with link
				</Button>
			</div>
			{manual && <ManualNeoPi owner={owner} onConnected={(name) => connected(name)} />}
		</div>
	);
}

// Fallback for older NeoPi versions without `npi auth-gateway link`.
function ManualNeoPi({ owner, onConnected }: { owner: string; onConnected: (name: string) => void }) {
	const [url, setUrl] = useState("");
	const [token, setToken] = useState("");
	const [name, setName] = useState("");
	const save = useSaveSource();
	const derivedName =
		name.trim() ||
		(() => {
			try {
				return `NeoPi on ${new URL(url).hostname}`;
			} catch {
				return "";
			}
		})();
	return (
		<div className="manual-neopi">
			<p className="faint">
				For NeoPi versions without the link command: the address is where this server reaches the gateway, and <code>npi auth-gateway token</code> prints the token.
			</p>
			<Field label="Gateway address" htmlFor="gw-url">
				<Input id="gw-url" mono value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://192.168.1.20:4000" />
			</Field>
			<Field label="Gateway token" hint="Kept on this server and never shown again. It is not a provider password." htmlFor="gw-token">
				<Input id="gw-token" mono type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} />
			</Field>
			<Field label="Name" optional hint={derivedName ? `Shown as “${derivedName}” if left empty.` : "Shown next to its accounts in the pool."} htmlFor="gw-name">
				<Input id="gw-name" value={name} onChange={(e) => setName(e.target.value)} />
			</Field>
			{save.error && (
				<Notice tone="bad" title="Couldn't connect to that NeoPi">
					{(save.error as Error).message}
				</Notice>
			)}
			<div className="row flow-actions">
				<Button
					busy={save.isPending}
					disabled={!url.trim() || !token.trim()}
					onClick={() =>
						save
							.mutateAsync({ label: derivedName || "NeoPi", url: url.trim(), bearer: token.trim(), ownerId: owner })
							.then(() => onConnected(derivedName || "NeoPi"))
							.catch(() => {})
					}
				>
					Connect with address and token
				</Button>
			</div>
		</div>
	);
}

// ---------- Import from NeoPi ----------
function ImportFlow({ session, people, onDone, onSignIn }: { session: Session; people: Person[]; onDone: (t: string, text: ReactNode) => void; onSignIn: () => void }) {
	const [profile, setProfile] = useState("");
	const [scan, setScan] = useState(false);
	const [picked, setPicked] = useState<Set<string>>(new Set());
	const [owner, setOwner] = useState(session.userId);
	const candidates = useImportCandidates(profile.trim() || undefined, scan);
	const doImport = useImport();
	const providers = useProviders();
	const unsupported = candidates.error instanceof ApiError && candidates.error.status === 404;
	const list = candidates.data?.credentials ?? [];
	const lockedPicked = list.filter((c) => picked.has(c.credentialId) && c.kind === "subscription").length;

	async function submit() {
		try {
			const r = await doImport.mutateAsync({ profile: profile.trim() || undefined, credentialIds: [...picked], ownerId: owner });
			onDone("Imported from NeoPi", (
				<>
					<p>
						{r.items?.length ?? picked.size} {r.items?.length === 1 ? "credential is" : "credentials are"} now in the pool.
					</p>
					{r.failed && r.failed.length > 0 && (
						<Notice tone="warn" title={`${r.failed.length} couldn't be imported`}>
							<ul className="import-failed">
								{r.failed.map((f) => (
									<li key={f.credentialId}>
										{list.find((c) => c.credentialId === f.credentialId)?.label ?? f.credentialId}: {f.error}
									</li>
								))}
							</ul>
						</Notice>
					)}
					{lockedPicked > 0 && (
						<p className="faint">
							{lockedPicked} signed-in {lockedPicked === 1 ? "account was" : "accounts were"} imported with refresh locked and marked with a lock in the pool. Re-import them after NeoPi refreshes, or sign in separately to give NeoProxy its own.
						</p>
					)}
				</>
			));
		} catch {
			/* shown below */
		}
	}

	if (unsupported)
		return (
			<Notice tone="info" title="This server can't import yet">
				Importing arrives with the next NeoProxy update. Until then, share the same accounts with <strong>Share from a NeoPi</strong>, which copies nothing.
			</Notice>
		);

	return (
		<div className="stack">
			<Field label="NeoPi profile" optional hint="The NeoPi profile folder on the computer running NeoProxy. Leave empty to use the default, ~/.omp/agent." htmlFor="profile">
				<div className="row">
					<Input id="profile" mono value={profile} onChange={(e) => { setProfile(e.target.value); setScan(false); }} placeholder="~/.omp/agent" />
					<Button icon="search" onClick={() => { setScan(true); candidates.refetch(); }} busy={candidates.isFetching}>
						Find credentials
					</Button>
				</div>
			</Field>
			{candidates.isError && !unsupported && (
				<Notice tone="bad" title="Couldn't read that profile">
					{(candidates.error as Error).message}
				</Notice>
			)}
			{scan && candidates.isSuccess && (
				<>
					{list.length === 0 ? (
						<Notice tone="info" title="No credentials found">This NeoPi profile has no stored credentials.</Notice>
					) : (
						<ul className="pick-list" aria-label="Credentials in this NeoPi profile">
							{list.map((c) => {
								const info = providerInfo(c.neoproxyProvider ?? c.provider, providers);
								const disabled = !c.importable || !!c.alreadyImported;
								return (
									<li key={c.credentialId}>
										<label className={cx("pick", disabled && "is-disabled")}>
											<input
												type="checkbox"
												disabled={disabled}
												checked={picked.has(c.credentialId)}
												onChange={(e) => {
													const next = new Set(picked);
													e.target.checked ? next.add(c.credentialId) : next.delete(c.credentialId);
													setPicked(next);
												}}
											/>
											<ProviderMark provider={info.id} name={info.name} hue={info.hue} size={24} />
											<span className="grow">
												<strong>{info.name}</strong>
												<span className="faint mono pick-label">{c.label}</span>
											</span>
											<span className="pick-kind">
												{c.alreadyImported ? (
													<span className="faint">Already in the pool</span>
												) : !c.importable ? (
													<span className="faint">{c.reason ?? "Not supported"}</span>
												) : c.kind === "subscription" ? (
													<span className="warn-text">
														<Icon name="lock" size={13} /> Refresh locked{c.expiresAt ? `, ${expiresIn(c.expiresAt)}` : ""}
													</span>
												) : (
													<span className="faint">API key, copies fully</span>
												)}
											</span>
										</label>
									</li>
								);
							})}
						</ul>
					)}
					{lockedPicked > 0 && (
						<Notice
							tone="warn"
							title={`${lockedPicked} signed-in ${lockedPicked === 1 ? "account" : "accounts"} will be imported with refresh locked`}
							action={<Button size="sm" onClick={onSignIn}>Sign in instead</Button>}
						>
							NeoProxy uses the copied sign-in but never refreshes it, so NeoPi's own sign-in keeps working. When the copy expires, re-import it or sign in separately.
						</Notice>
					)}
					<ContributorField session={session} people={people} value={owner} onChange={setOwner} />
					{doImport.error && (
						<Notice tone="bad" title="Import failed">
							{(doImport.error as Error).message}
						</Notice>
					)}
					<div className="row flow-actions">
						<Button variant="primary" size="lg" icon="import" busy={doImport.isPending} disabled={picked.size === 0} onClick={submit}>
							Import {picked.size || ""} {picked.size === 1 ? "credential" : "credentials"}
						</Button>
					</div>
				</>
			)}
		</div>
	);
}

function expiresIn(ms: number) {
	const h = Math.round((ms - Date.now()) / 3_600_000);
	if (h < 1) return "expires within the hour";
	if (h < 48) return `expires in ${h} h`;
	return `expires in ${Math.round(h / 24)} days`;
}

// ---------- Upload a credential file ----------
function guessFile(json: unknown): string | undefined {
	if (!json || typeof json !== "object") return undefined;
	const o = json as Record<string, unknown>;
	if (typeof o.type === "string") return o.type;
	if (o.tokens && typeof o.tokens === "object" && "id_token" in (o.tokens as object)) return "codex";
	if (o.claudeAiOauth) return "claude";
	if (typeof o.OPENAI_API_KEY === "string" || o.auth_mode) return "codex";
	return undefined;
}

function FileFlow({ session, people, onDone }: { session: Session; people: Person[]; onDone: (t: string, text: ReactNode) => void }) {
	const [file, setFile] = useState<File>();
	const [guess, setGuess] = useState<string>();
	const [parseError, setParseError] = useState<string>();
	const [over, setOver] = useState(false);
	const [owner, setOwner] = useState(session.userId);
	const upload = useUploadFile();
	const toast = useToast();
	const providers = useProviders();

	async function pick(f: File | undefined) {
		setFile(f);
		setGuess(undefined);
		setParseError(undefined);
		if (!f) return;
		if (f.size > 256 * 1024) return setParseError("That file is larger than any credential file. Check that you picked the right one.");
		try {
			setGuess(guessFile(JSON.parse(await f.text())));
		} catch {
			setParseError("This isn't a JSON file. Credential files are JSON, like ~/.codex/auth.json.");
		}
	}

	async function submit() {
		if (!file) return;
		try {
			await upload.mutateAsync({ file, ownerId: owner });
			onDone("Credential added", <p>The account in <code>{file.name}</code> is now in the pool. NeoProxy refreshes it automatically.</p>);
		} catch (e) {
			toast((e as Error).message, "bad");
		}
	}

	return (
		<div className="stack">
			<label
				className={cx("dropzone", over && "is-over", file && "has-file")}
				onDragOver={(e) => {
					e.preventDefault();
					setOver(true);
				}}
				onDragLeave={() => setOver(false)}
				onDrop={(e) => {
					e.preventDefault();
					setOver(false);
					pick(e.dataTransfer.files[0]);
				}}
			>
				<input type="file" accept=".json,application/json" className="sr-only" onChange={(e) => pick(e.target.files?.[0])} />
				<Icon name="upload" size={22} />
				{file ? (
					<span>
						<strong>{file.name}</strong>
						{guess ? <span className="muted"> looks like a {providerInfo(guess, providers).name} credential.</span> : <span className="muted"> ready to upload.</span>}
					</span>
				) : (
					<span>
						<strong>Drop a credential file here</strong> <span className="muted">or click to choose one</span>
					</span>
				)}
			</label>
			<div className="accepted">
				<p className="field-label">Files this accepts</p>
				<ul>
					<li>
						<strong>Codex CLI</strong> <code>~/.codex/auth.json</code> after <code>codex login</code>
					</li>
					<li>
						<strong>Claude Code</strong> <code>~/.claude/.credentials.json</code>
					</li>
					<li>
						<strong>CLIProxyAPI or another NeoProxy</strong> any file from its <code>auths</code> folder
					</li>
				</ul>
				<p className="faint">The file holds a sign-in. Treat it like a password and delete local copies you no longer need.</p>
			</div>
			{parseError && <Notice tone="bad">{parseError}</Notice>}
			<ContributorField session={session} people={people} value={owner} onChange={setOwner} />
			<div className="row flow-actions">
				<Button variant="primary" size="lg" icon="upload" busy={upload.isPending} disabled={!file || !!parseError} onClick={submit}>
					Add to pool
				</Button>
			</div>
		</div>
	);
}
