import { AnimatePresence, motion } from "motion/react";
import { useMemo, useState } from "react";
import { useCore } from "../../api/queries";
import { ago } from "../../lib/format";
import { canonicalProvider, providerInfo } from "../../lib/providers";
import { Button, CopyButton, Dialog, Field, Input, Notice, ProviderMark, Switch, TabPanel, Tabs, cx } from "../../ui/components";
import { Icon } from "../../ui/icons";
import { useToast } from "../../ui/toast";
import { useMintKey, useRevokeKey, type InferenceKey, type User, type VaultAccount } from "./api";
import { displayName, keyScope } from "./copy";
import { Confirm } from "./widgets";

// Inference keys: the only place the key glyph appears in the dashboard.

export function KeysPanel({
	user,
	accounts,
	exampleModel,
	self,
	canUseCount,
}: {
	user: User;
	/** Accounts this key may be narrowed to. For contributors, only their own. */
	accounts: VaultAccount[];
	exampleModel?: string;
	self?: boolean;
	canUseCount: number;
}) {
	const [issuing, setIssuing] = useState(false);
	const [revoking, setRevoking] = useState<InferenceKey>();
	const revoke = useRevokeKey();
	const toast = useToast();
	const accountLabel = (id: string) => accounts.find((a) => a.id === id)?.label ?? "an account no longer in the pool";
	const suspended = user.state === "suspended";
	const sorted = [...(user.keys ?? [])].sort((a, b) => b.createdAt - a.createdAt);
	const who = self ? "you" : displayName(user);

	return (
		<section className="panel pp-panel">
			<header className="pp-panel-head">
				<div className="grow">
					<h2 className="pp-h2">{self ? "Your inference keys" : "Inference keys"}</h2>
					<p className="pp-desc">
						Apps send a key with each request to use NeoProxy as {who}. A key can call the models {self ? "you have" : "they have"} been given and nothing else; it never signs in to this dashboard.
					</p>
				</div>
				<Button icon="key" variant={sorted.length ? "secondary" : "primary"} disabled={suspended} onClick={() => setIssuing(true)}>
					Issue a key
				</Button>
			</header>
			{suspended && <p className="pp-inline-note">Keys can't be issued while {who} {self ? "are" : "is"} suspended.</p>}
			{sorted.length === 0 ? (
				<div className="pp-keys-empty">
					<span className="pp-keys-empty-glyph" aria-hidden="true">
						<Icon name="key" size={22} />
					</span>
					<div>
						<strong>No keys</strong>
						<p className="muted">
							Issue one for each app or device, so you can revoke them one at a time. The key is shown once, together with copy-ready setup for NeoPi and other apps.
						</p>
					</div>
				</div>
			) : (
				<ul className="pp-keys">
					<AnimatePresence initial={false}>
						{sorted.map((k) => (
							<motion.li key={k.id} layout initial={{ opacity: 0, y: -6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, height: 0 }} transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}>
								<span className="pp-key-glyph" aria-hidden="true">
									<Icon name="key" size={16} />
								</span>
								<span className="grow pp-key-text">
									<span className="pp-key-title">
										<strong>{k.label}</strong>
										<code className="faint">{k.prefix}</code>
									</span>
									<span className="pp-key-meta">
										<span>Issued {ago(k.createdAt)}</span>
										<span>{k.lastUsedAt ? `Last used ${ago(k.lastUsedAt)}` : "Not used yet"}</span>
										<span>{keyScope(k, accountLabel)}</span>
										{k.expiresAt ? <span>{k.expiresAt < Date.now() ? "Expired" : `Expires ${new Date(k.expiresAt).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" })}`}</span> : null}
									</span>
								</span>
								<Button size="sm" variant="ghost" onClick={() => setRevoking(k)}>
									Revoke
								</Button>
							</motion.li>
						))}
					</AnimatePresence>
				</ul>
			)}

			<IssueKeyDialog open={issuing} onOpenChange={setIssuing} user={user} accounts={accounts} exampleModel={exampleModel} self={self} canUseCount={canUseCount} />

			<Confirm
				open={!!revoking}
				onOpenChange={(v) => !v && setRevoking(undefined)}
				title={`Revoke “${revoking?.label ?? ""}”?`}
				confirmLabel="Revoke key"
				busy={revoke.isPending}
				onConfirm={() =>
					revoking &&
					revoke
						.mutateAsync({ userId: user.id, keyId: revoking.id })
						.then(() => {
							toast(`Revoked “${revoking.label}”`);
							setRevoking(undefined);
						})
						.catch((e) => toast(`Couldn't revoke the key: ${(e as Error).message}. Reload the page and try again.`, "bad"))
				}
			>
				<p>
					Any app using <code>{revoking?.prefix}</code> stops working immediately; its next request is refused. This can't be undone. To keep that app running, issue a new key first and swap it in.
				</p>
			</Confirm>
		</section>
	);
}

function IssueKeyDialog({
	open,
	onOpenChange,
	user,
	accounts,
	exampleModel,
	self,
	canUseCount,
}: {
	open: boolean;
	onOpenChange: (v: boolean) => void;
	user: User;
	accounts: VaultAccount[];
	exampleModel?: string;
	self?: boolean;
	canUseCount: number;
}) {
	const mint = useMintKey();
	const [label, setLabel] = useState("");
	const [scoped, setScoped] = useState(false);
	const [scope, setScope] = useState<Set<string>>(new Set());
	const [error, setError] = useState<string>();
	const [issued, setIssued] = useState<{ key: InferenceKey; secret: string }>();
	const contributor = user.role === "owner";
	const mustScope = contributor;
	const useScope = mustScope || scoped;

	const reset = () => {
		setLabel("");
		setScoped(false);
		setScope(new Set());
		setError(undefined);
		setIssued(undefined);
		mint.reset();
	};
	const close = (v: boolean) => {
		onOpenChange(v);
		if (!v) setTimeout(reset, 250);
	};

	const submit = async () => {
		setError(undefined);
		if (!label.trim()) return setError("Give the key a name so you can tell it apart later.");
		if (useScope && scope.size === 0) return setError(mustScope ? "Pick at least one of their accounts for this key." : "Pick at least one account, or turn off the account limit.");
		try {
			const res = await mint.mutateAsync({ userId: user.id, label: label.trim(), accountIds: useScope ? [...scope] : undefined });
			setIssued(res);
		} catch (e) {
			setError(`NeoProxy didn't issue the key: ${(e as Error).message}. Check the name and accounts, then try again.`);
		}
	};

	return (
		<Dialog
			open={open}
			onOpenChange={close}
			wide={!!issued}
			title={issued ? "Copy the key now" : self ? "Issue a key for yourself" : `Issue a key for ${displayName(user)}`}
			description={
				issued
					? "The key works right away. Paste it into the app that will use it; the setup below is ready to copy."
					: `Name it after the app or device that will use it. The key can call the models ${self ? "you have" : "they have"} been given; you'll see it once, next.`
			}
			footer={
				issued ? (
					<Button variant="primary" onClick={() => close(false)}>
						Done
					</Button>
				) : (
					<>
						<Button variant="ghost" onClick={() => close(false)}>
							Cancel
						</Button>
						<Button variant="primary" icon="key" busy={mint.isPending} onClick={submit}>
							Issue key
						</Button>
					</>
				)
			}
		>
			<AnimatePresence mode="wait" initial={false}>
				{issued ? (
					<motion.div key="reveal" className="stack-lg" initial={{ opacity: 0, x: 16 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0 }} transition={{ duration: 0.2, ease: [0.22, 1, 0.36, 1] }}>
						<Reveal secret={issued.secret} label={issued.key.label} exampleModel={exampleModel} />
						{canUseCount === 0 && (
							<Notice tone="info" title={self ? "You can't ask for any model yet" : `${displayName(user)} can't ask for any model yet`}>
								The key is valid, but requests are refused until {self ? "an administrator gives you access to a model" : "you give them access to a model under “What they can use”"}.
							</Notice>
						)}
					</motion.div>
				) : (
					<motion.form
						key="form"
						className="stack-lg"
						initial={{ opacity: 0 }}
						animate={{ opacity: 1 }}
						exit={{ opacity: 0, x: -16 }}
						transition={{ duration: 0.16 }}
						onSubmit={(e) => {
							e.preventDefault();
							submit();
						}}
					>
						<Field label="Name" hint="Where it will be used, for example “Lena's laptop” or “CI runner”." htmlFor="key-label">
							<Input id="key-label" autoFocus maxLength={200} value={label} onChange={(e) => setLabel(e.target.value)} placeholder="Laptop" />
						</Field>
						<div className="field">
							{mustScope ? (
								<>
									<span className="field-label">Accounts this key uses</span>
									<p className="field-hint">Contributor keys draw only on the contributor's own accounts. Pick which ones.</p>
								</>
							) : (
								<>
									<Switch id="key-scope" checked={scoped} onCheckedChange={setScoped} label="Limit this key to specific accounts" />
									<p className="field-hint">Optional. Without a limit the key uses whichever account the model's routing picks. A limit only narrows that; it never adds access.</p>
								</>
							)}
							{useScope && <AccountChecklist accounts={accounts} value={scope} onChange={setScope} emptyText={mustScope ? `${self ? "You haven't" : "They haven't"} added any accounts to the pool yet. Add provider access on the Pool page first.` : "No accounts in the pool yet."} />}
						</div>
						{error && (
							<p className="field-error" role="alert">
								{error}
							</p>
						)}
					</motion.form>
				)}
			</AnimatePresence>
		</Dialog>
	);
}

function AccountChecklist({ accounts, value, onChange, emptyText }: { accounts: VaultAccount[]; value: Set<string>; onChange: (s: Set<string>) => void; emptyText: string }) {
	const groups = useMemo(() => {
		const m = new Map<string, VaultAccount[]>();
		for (const a of accounts) m.set(canonicalProvider(a.providerId), [...(m.get(canonicalProvider(a.providerId)) ?? []), a]);
		return [...m.entries()];
	}, [accounts]);
	if (!accounts.length) return <p className="pp-inline-note">{emptyText}</p>;
	return (
		<div className="acct-list">
			{groups.map(([provider, list]) => {
				const info = providerInfo(provider);
				return (
					<fieldset key={provider} className="acct-group">
						<legend>
							<ProviderMark provider={provider} name={info.name} size={18} />
							{info.name}
						</legend>
						{list.map((a) => (
							<label key={a.id} className={cx("acct", value.has(a.id) && "is-on")}>
								<input
									type="checkbox"
									checked={value.has(a.id)}
									onChange={(e) => {
										const next = new Set(value);
										if (e.target.checked) next.add(a.id);
										else next.delete(a.id);
										onChange(next);
									}}
								/>
								<span className="acct-box" aria-hidden="true">
									<Icon name="check" size={13} />
								</span>
								<span className="grow mono acct-label">{a.label}</span>
								{a.state !== "healthy" && <span className="faint">{a.state}</span>}
							</label>
						))}
					</fieldset>
				);
			})}
		</div>
	);
}

function loopback(host: string) {
	return /^(localhost|127\.\d+\.\d+\.\d+|\[::1\])(:\d+)?$/i.test(host);
}

/** One-time reveal with setup snippets. */
function Reveal({ secret, label, exampleModel }: { secret: string; label: string; exampleModel?: string }) {
	const core = useCore();
	const endpoint = (core.data?.endpoint ?? location.origin).replace(/\/+$/, "");
	const base = `${endpoint}/v1`;
	const url = new URL(endpoint);
	// NeoPi only talks plain http to this computer; anything else must be https.
	const needsHttps = url.protocol === "http:" && !loopback(url.host);
	const npiUrl = needsHttps ? endpoint.replace(/^http:/, "https:") : endpoint;
	const model = exampleModel ?? "neoproxy/<model>";
	const [tab, setTab] = useState("neopi");
	const [copied, setCopied] = useState(false);

	return (
		<>
			<div className="reveal">
				<div className="reveal-head">
					<Icon name="key" size={16} />
					<span>{label}</span>
				</div>
				<div className="reveal-key">
					<code>{secret}</code>
					<Button
						variant={copied ? "secondary" : "primary"}
						icon={copied ? "check" : "copy"}
						onClick={async () => {
							await navigator.clipboard.writeText(secret);
							setCopied(true);
						}}
					>
						{copied ? "Copied" : "Copy key"}
					</Button>
				</div>
				<p className="reveal-warn">
					<Icon name="alert" size={15} />
					You won't see this key again. NeoProxy keeps only a fingerprint of it. If it's lost, revoke it and issue a new one.
				</p>
			</div>

			<div className="setup">
				<h3 className="pp-h3">Use it</h3>
				<Tabs
					value={tab}
					onValueChange={setTab}
					items={[
						{ value: "neopi", label: "NeoPi" },
						{ value: "apps", label: "Other apps" },
						{ value: "curl", label: "Test with curl" },
					]}
				>
					<TabPanel value="neopi" className="setup-panel">
						<p className="muted">Run this on the computer that will use NeoProxy, then paste the key when NeoPi asks for it.</p>
						<CodeBlock text={`npi login neoproxy --url ${npiUrl}`} />
						<p className="faint">
							Already inside a NeoPi session? Type <code>/login neoproxy {npiUrl}</code> instead. Logging in again with another key adds it alongside this one.
						</p>
						{needsHttps && (
							<Notice tone="warn" title="NeoPi needs https for this server">
								NeoPi only uses plain http for a NeoProxy on the same computer. Serve NeoProxy over https (for example behind a reverse proxy) before connecting from other machines.
							</Notice>
						)}
					</TabPanel>
					<TabPanel value="apps" className="setup-panel">
						<p className="muted">Any app that speaks the OpenAI API works. Point it at this base URL and use the key as its API key.</p>
						<dl className="setup-facts">
							<dt>Base URL</dt>
							<dd>
								<CodeBlock text={base} />
							</dd>
							<dt>API key</dt>
							<dd>
								<CodeBlock text={secret} />
							</dd>
							<dt>Model</dt>
							<dd>
								<CodeBlock text={model} />
							</dd>
						</dl>
						<p className="faint">Many tools read these environment variables:</p>
						<CodeBlock text={`export OPENAI_BASE_URL=${base}\nexport OPENAI_API_KEY=${secret}`} />
					</TabPanel>
					<TabPanel value="curl" className="setup-panel">
						<p className="muted">Sends one short message. A reply means the key, the model and the pool all work.</p>
						<CodeBlock
							text={`curl ${base}/chat/completions \\\n  -H "Authorization: Bearer ${secret}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${model}","messages":[{"role":"user","content":"Say hello"}]}'`}
						/>
					</TabPanel>
				</Tabs>
			</div>
		</>
	);
}

export function CodeBlock({ text }: { text: string }) {
	return (
		<div className="codeblock">
			<pre>
				<code>{text}</code>
			</pre>
			<CopyButton text={text} />
		</div>
	);
}
