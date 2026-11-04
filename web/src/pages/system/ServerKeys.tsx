import { AnimatePresence, motion } from "motion/react";
import { useState } from "react";
import { ApiError } from "../../api/client";
import { plural } from "../../lib/format";
import { Button, CopyButton, Dialog, EmptyState, Field, IconButton, Input, Notice, Skeleton } from "../../ui/components";
import { useToast } from "../../ui/toast";
import { useEngineConfig, usePutConfigPath } from "./api";
import { ConfirmDialog, ServerKeyGlyph, SubHead } from "./shared";

type Entry = string | { key?: string; "api-key"?: string };
const keyOf = (e: Entry) => (typeof e === "string" ? e : (e.key ?? e["api-key"] ?? ""));
const mask = (k: string) => (k.length <= 10 ? "•".repeat(k.length) : `${k.slice(0, 6)}${"•".repeat(8)}${k.slice(-4)}`);

function generate() {
	const bytes = crypto.getRandomValues(new Uint8Array(24));
	return `sk-server-${Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")}`;
}

export function ServerKeys() {
	const cfg = useEngineConfig();
	const put = usePutConfigPath();
	const toast = useToast();
	const [adding, setAdding] = useState(false);
	const [removing, setRemoving] = useState<string>();
	const entries: Entry[] = cfg.data?.config.access?.["api-keys"] ?? [];
	const keys = entries.map(keyOf);

	const save = async (next: Entry[]) => {
		await put.mutateAsync({ path: "access/api-keys", value: next, etag: cfg.data?.etag });
	};
	const explainError = (e: unknown) =>
		e instanceof ApiError && e.status === 412 ? "The configuration changed on the server since this page loaded. The list has been refreshed; try again." : (e as Error).message;

	return (
		<div className="stack-lg">
			<SubHead
				title="Server keys"
				lede="Bearer keys the engine accepts directly, outside of people. Kept for old clients and scripts that can't use a person's inference key."
				actions={
					keys.length > 0 && (
						<Button className="glyph-btn" onClick={() => setAdding(true)}>
							<ServerKeyGlyph size={16} />
							Add a server key
						</Button>
					)
				}
			/>

			<Notice tone="warn" title="Server keys skip people and virtual models">
				A request with a server key isn't tied to anyone: no per-person limits, no model restrictions, and nobody's name on it in Activity. It can reach every provider model in the pool. For almost every app,{" "}
				<a href="#/people">issue an inference key to a person on People</a> instead.
			</Notice>

			{cfg.isError ? (
				<Notice tone="bad" title="Couldn't load server keys">
					{(cfg.error as Error).message}. Reload the page; if it keeps failing, check that NeoProxy is running.
				</Notice>
			) : cfg.isPending ? (
				<div className="panel sk-list-loading">
					<Skeleton h={18} />
					<Skeleton h={18} w="70%" />
				</div>
			) : keys.length === 0 ? (
				<div className="panel">
					<EmptyState
						art={
							<span className="sk-art" aria-hidden="true">
								<ServerKeyGlyph size={28} />
							</span>
						}
						title="No server keys, which is the usual setup"
						action={
							<Button className="glyph-btn" onClick={() => setAdding(true)}>
								<ServerKeyGlyph size={16} />
								Add a server key
							</Button>
						}
					>
						Apps connect with a person's inference key. Add a server key only if you have a client that can't use one.
					</EmptyState>
				</div>
			) : (
				<section className="stack" aria-label="Server keys">
					<p className="muted">{plural(keys.length, "server key")} can call this server with no person attached.</p>
					<ul className="panel sk-list">
						<AnimatePresence initial={false}>
							{keys.map((k, i) => (
								<motion.li key={k} layout initial={{ opacity: 0, height: 0 }} animate={{ opacity: 1, height: "auto" }} exit={{ opacity: 0, height: 0 }} transition={{ duration: 0.2 }}>
									<div className="sk-row">
										<span className="sk-glyph">
											<ServerKeyGlyph size={17} />
										</span>
										<code className="grow sk-key">{mask(k)}</code>
										<span className="faint sk-index">Key {i + 1}</span>
										<CopyButton text={k} label="Copy key" />
										<IconButton icon="trash" label="Delete this key" onClick={() => setRemoving(k)} />
									</div>
								</motion.li>
							))}
						</AnimatePresence>
					</ul>
				</section>
			)}

			<p className="faint sk-foot">
				Clients send a server key as <code>Authorization: Bearer …</code> to the same addresses listed on the <a href="#/system">Overview</a>. Keys are stored in <code>config.yaml</code> under <code>access.api-keys</code>.
			</p>

			<AddKeyDialog
				open={adding}
				onOpenChange={setAdding}
				existing={keys}
				busy={put.isPending}
				onAdd={async (k) => {
					try {
						await save([...entries, k]);
						toast("Server key added. It works immediately.");
						setAdding(false);
					} catch (e) {
						toast(`Couldn't add the key: ${explainError(e)}`, "bad");
					}
				}}
			/>
			<ConfirmDialog
				open={!!removing}
				onOpenChange={(v) => !v && setRemoving(undefined)}
				title="Delete this server key?"
				description={
					<>
						Anything still using <code>{removing ? mask(removing) : ""}</code> stops working on its next request and gets an authentication error. This can't be undone, but you can add the same key again later.
					</>
				}
				confirmLabel="Delete key"
				danger
				busy={put.isPending}
				onConfirm={async () => {
					try {
						await save(entries.filter((e) => keyOf(e) !== removing));
						toast("Server key deleted.");
						setRemoving(undefined);
					} catch (e) {
						toast(`Couldn't delete the key: ${explainError(e)}`, "bad");
					}
				}}
			/>
		</div>
	);
}

function AddKeyDialog({ open, onOpenChange, existing, busy, onAdd }: { open: boolean; onOpenChange: (v: boolean) => void; existing: string[]; busy: boolean; onAdd: (k: string) => void }) {
	const [value, setValue] = useState("");
	const [touched, setTouched] = useState(false);
	const v = value.trim();
	const error = !v ? (touched ? "Enter a key, or generate one." : undefined) : v.length < 16 ? "Use at least 16 characters so the key can't be guessed." : /\s/.test(v) ? "Keys can't contain spaces." : existing.includes(v) ? "That key is already on the list." : undefined;
	const close = (o: boolean) => {
		if (!o) {
			setValue("");
			setTouched(false);
		}
		onOpenChange(o);
	};
	return (
		<Dialog
			open={open}
			onOpenChange={close}
			title="Add a server key"
			description="The engine will accept this key from any client, with full access to every model and no person attached. Save it somewhere safe; anyone who has it can use the pool."
			footer={
				<>
					<Button variant="ghost" onClick={() => close(false)}>
						Cancel
					</Button>
					<Button
						variant="primary"
						busy={busy}
						onClick={() => {
							setTouched(true);
							if (v && !error) onAdd(v);
						}}
					>
						Add key
					</Button>
				</>
			}
		>
			<Notice tone="info">
				Is this for a person's app? <a href="#/people">An inference key on People</a> is safer: it has limits, can be revoked on its own, and shows up under their name.
			</Notice>
			<Field label="Key" htmlFor="sk-new" error={error} hint="Paste the key your client already uses, or generate a new random one.">
				<div className="row">
					<Input id="sk-new" mono autoComplete="off" spellCheck={false} value={value} onChange={(e) => setValue(e.target.value)} onBlur={() => setTouched(true)} placeholder="sk-…" />
					<Button onClick={() => setValue(generate())}>Generate</Button>
				</div>
			</Field>
			{v && !error && (
				<div className="row">
					<span className="faint grow">Copy it now if your client needs it; you can also copy it later from the list.</span>
					<CopyButton text={v} label="Copy key" />
				</div>
			)}
		</Dialog>
	);
}
