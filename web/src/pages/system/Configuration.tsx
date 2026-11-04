import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { ApiError } from "../../api/client";
import { ago, fmtInt } from "../../lib/format";
import { Button, Chip, EmptyState, Notice, Skeleton, TabPanel, Tabs, cx } from "../../ui/components";
import { useToast } from "../../ui/toast";
import { useConfigYaml, useEngineConfig, useSaveConfigYaml } from "./api";
import { ConfirmDialog, SubHead, fmtBytes, useSaveShortcut, useUnsavedGuard } from "./shared";

const SECRET_KEY = /secret|token|password|credential|api-?key|bearer/i;
const BCRYPT = /^\$2[aby]\$\d\d\$/;

function secretState(text: string): "hashed" | "plain" | "unset" {
	const m = text.match(/secret-key["']?\s*:\s*["']?([^"'\s,}\n]*)/);
	if (!m || !m[1]) return "unset";
	return BCRYPT.test(m[1]) ? "hashed" : "plain";
}

export function Configuration() {
	const yaml = useConfigYaml();
	const save = useSaveConfigYaml();
	const toast = useToast();
	const [text, setText] = useState<string>();
	const [error, setError] = useState<{ message: string; line?: number; conflict?: boolean }>();
	const [confirmReload, setConfirmReload] = useState(false);
	const [tab, setTab] = useState("edit");
	const [, tick] = useState(0);
	const baseline = yaml.data?.text;
	useEffect(() => {
		if (baseline !== undefined) setText((t) => (t === undefined ? baseline : t));
	}, [baseline]);
	useEffect(() => {
		const t = setInterval(() => tick((n) => n + 1), 15_000);
		return () => clearInterval(t);
	}, []);

	const dirty = text !== undefined && baseline !== undefined && text !== baseline;
	const guard = useUnsavedGuard(dirty, "config.yaml");

	const doSave = async () => {
		if (!dirty || save.isPending || text === undefined) return;
		setError(undefined);
		try {
			await save.mutateAsync({ text, etag: yaml.data?.etag });
			// The engine rewrites the file in its canonical layout; show what it actually saved.
			const r = await yaml.refetch();
			if (r.data) setText(r.data.text);
			toast("Saved. The engine validated config.yaml and is using it now.");
		} catch (e) {
			if (e instanceof ApiError && e.status === 412) {
				setError({ message: "config.yaml changed on disk after you opened it, so your version wasn't saved. Copy your edits somewhere, reload from disk, and apply them again.", conflict: true });
				return;
			}
			const message = (e as Error).message;
			const line = Number(message.match(/line (\d+)/i)?.[1]) || undefined;
			setError({ message, line });
		}
	};
	useSaveShortcut(doSave, dirty);

	const reload = async () => {
		setConfirmReload(false);
		setError(undefined);
		const r = await yaml.refetch();
		if (r.data) setText(r.data.text);
	};

	const lines = text?.split("\n").length ?? 0;
	const bytes = text ? new TextEncoder().encode(text).length : 0;
	const secret = text ? secretState(text) : "unset";

	return (
		<div className="stack-lg">
			<SubHead
				title="Configuration"
				lede="The engine's config.yaml, exactly as it's saved on disk. Every other page writes its settings here; edit by hand only for settings that have no page."
				actions={
					<>
						<Button icon="refresh" onClick={() => (dirty ? setConfirmReload(true) : reload())} disabled={yaml.isFetching}>
							Reload from disk
						</Button>
						<Button variant="primary" onClick={doSave} disabled={!dirty} busy={save.isPending}>
							Save config.yaml
						</Button>
					</>
				}
			/>

			<Notice tone="info" title="Advanced">
				The engine checks the whole file before using it. If something is wrong, nothing is saved and the problem shows below. Saving replaces the running configuration straight away, for every request.
			</Notice>

			<Tabs
				value={tab}
				onValueChange={setTab}
				items={[
					{ value: "edit", label: "Edit YAML" },
					{ value: "browse", label: "Browse settings" },
				]}
			>
				<TabPanel value="edit" className="stack">
					{yaml.isPending ? (
						<div className="panel">
							<Skeleton h={420} r={12} />
						</div>
					) : yaml.isError && text === undefined ? (
						<div className="panel">
							<EmptyState title="Couldn't read config.yaml" action={<Button onClick={() => yaml.refetch()}>Try again</Button>}>
								{(yaml.error as Error).message}. Check that NeoProxy is running and can read its config file.
							</EmptyState>
						</div>
					) : (
						<>
							<div className="cfg-secret panel">
								<Chip tone={secret === "hashed" ? "good" : secret === "plain" ? "warn" : "neutral"} icon={secret === "hashed" ? "lock" : undefined}>
									{secret === "hashed" ? "Hashed" : secret === "plain" ? "Plain text" : "Not set"}
								</Chip>
								<p className="grow muted">
									{secret === "hashed" ? (
										<>
											The operator secret (<code>management.secret-key</code>) is stored only as a bcrypt hash. The secret itself is never saved or shown, and can't be recovered from the hash. Leave the hash as it is.
										</>
									) : secret === "plain" ? (
										<>
											<code>management.secret-key</code> holds a plain secret. NeoProxy replaces it with its hash the next time it loads the configuration.
										</>
									) : (
										<>
											No operator secret is set (<code>management.secret-key</code>). People sign in with their own accounts, so it's only needed for scripts that call the engine directly.
										</>
									)}
								</p>
							</div>

							<YamlEditor value={text ?? ""} onChange={setText} errorLine={error?.line} />

							<div className="cfg-foot">
								<span className={cx("cfg-state", dirty && "is-dirty")}>
									<span className="dot" aria-hidden="true" />
									{dirty ? "Unsaved changes" : "Saved"}
								</span>
								<span className="faint num">
									{fmtInt(lines)} lines, {fmtBytes(bytes)}
								</span>
								<span className="faint">{yaml.data ? `Loaded from disk ${ago(yaml.data.loadedAt)}` : ""}</span>
								<span className="grow" />
								{dirty && (
									<Button size="sm" variant="ghost" onClick={() => setText(baseline)}>
										Discard changes
									</Button>
								)}
								<span className="faint cfg-hint">
									<kbd className="kbd">Ctrl</kbd> <kbd className="kbd">S</kbd> saves
								</span>
							</div>

							{error && (
								<Notice
									tone="bad"
									title={error.conflict ? "Someone else saved first" : "The engine rejected this config.yaml"}
									action={
										error.conflict ? (
											<Button size="sm" onClick={() => setConfirmReload(true)}>
												Reload from disk
											</Button>
										) : undefined
									}
								>
									<p className="cfg-error">{error.message}</p>
									{!error.conflict && <p>Nothing was saved; the running configuration is unchanged. Fix the {error.line ? `marked line ${error.line}` : "problem"} and save again.</p>}
								</Notice>
							)}
						</>
					)}
				</TabPanel>
				<TabPanel value="browse">
					<Browse dirty={dirty} />
				</TabPanel>
			</Tabs>

			<ConfirmDialog
				open={confirmReload}
				onOpenChange={setConfirmReload}
				title="Throw away your edits?"
				description="Reloading replaces the editor with config.yaml as it is on disk now. Your unsaved edits are lost."
				confirmLabel="Discard and reload"
				danger
				onConfirm={reload}
			/>
			{guard}
		</div>
	);
}

function YamlEditor({ value, onChange, errorLine }: { value: string; onChange: (v: string) => void; errorLine?: number }) {
	const ta = useRef<HTMLTextAreaElement>(null);
	const gutter = useRef<HTMLDivElement>(null);
	const count = value.split("\n").length;
	const numbers = useMemo(() => Array.from({ length: count }, (_, i) => i + 1), [count]);
	useEffect(() => {
		if (!errorLine || !ta.current) return;
		const el = ta.current;
		const starts = value.split("\n").slice(0, errorLine - 1).reduce((n, l) => n + l.length + 1, 0);
		const lh = parseFloat(getComputedStyle(el).lineHeight) || 20;
		el.scrollTop = Math.max(0, (errorLine - 4) * lh);
		el.setSelectionRange(starts, starts + (value.split("\n")[errorLine - 1]?.length ?? 0));
	}, [errorLine]);
	// Tab indents; Escape first lets the next Tab leave the editor, so focus is never trapped.
	const escaped = useRef(false);
	const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
		if (e.key === "Escape") {
			escaped.current = true;
			return;
		}
		const leave = escaped.current;
		escaped.current = false;
		if (leave || e.key !== "Tab" || e.shiftKey || e.ctrlKey || e.metaKey || e.altKey) return;
		e.preventDefault();
		const el = e.currentTarget;
		el.setRangeText("  ", el.selectionStart, el.selectionEnd, "end");
		onChange(el.value);
	};
	return (
		<div className="panel yaml">
			<div className="yaml-gutter" ref={gutter} aria-hidden="true">
				{numbers.map((n) => (
					<span key={n} className={n === errorLine ? "is-error" : undefined}>
						{n}
					</span>
				))}
			</div>
			<textarea
				ref={ta}
				className="yaml-text"
				value={value}
				onChange={(e) => onChange(e.target.value)}
				onKeyDown={onKey}
				onScroll={(e) => {
					if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
				}}
				spellCheck={false}
				autoCapitalize="off"
				autoComplete="off"
				wrap="off"
				aria-label="config.yaml"
				aria-describedby="cfg-editor-help"
			/>
			<span id="cfg-editor-help" className="sr-only">
				Tab inserts two spaces. Press Escape, then Tab, to move focus out of the editor. Control S saves.
			</span>
		</div>
	);
}

function Browse({ dirty }: { dirty: boolean }) {
	const cfg = useEngineConfig();
	if (cfg.isPending)
		return (
			<div className="panel">
				<Skeleton h={240} r={12} />
			</div>
		);
	if (cfg.isError)
		return (
			<Notice tone="bad" title="Couldn't load the settings">
				{(cfg.error as Error).message}
			</Notice>
		);
	const entries = Object.entries(cfg.data.config);
	return (
		<div className="stack">
			<p className="muted">A read-only view of the saved settings, grouped by section. Secrets are hidden.{dirty ? " Your unsaved edits aren't reflected until you save." : ""}</p>
			<div className="panel cfg-browse">
				{entries.map(([k, v]) =>
					v === null || typeof v !== "object" ? (
						<div key={k} className="cfg-group cfg-scalar">
							<code>{k}</code>
							<Value v={v} secret={SECRET_KEY.test(k)} />
						</div>
					) : (
						<details key={k} className="cfg-group">
							<summary>
								<code>{k}</code>
								<span className="faint">{summarize(v)}</span>
							</summary>
							<div className="cfg-group-body">
								<Value v={v} secret={SECRET_KEY.test(k)} />
							</div>
						</details>
					),
				)}
			</div>
		</div>
	);
}

function summarize(v: unknown): string {
	if (Array.isArray(v)) return v.length === 0 ? "empty list" : `${fmtInt(v.length)} ${v.length === 1 ? "item" : "items"}`;
	if (v && typeof v === "object") {
		const n = Object.keys(v).length;
		return n === 0 ? "empty" : `${fmtInt(n)} ${n === 1 ? "setting" : "settings"}`;
	}
	return String(v);
}

function Value({ v, secret }: { v: unknown; secret?: boolean }): ReactNode {
	if (secret && v !== "" && v != null && !(Array.isArray(v) && v.length === 0)) return <span className="faint">Hidden</span>;
	if (Array.isArray(v)) {
		if (!v.length) return <span className="faint">Empty list</span>;
		return (
			<ul className="cfg-list">
				{v.map((x, i) => (
					<li key={i}>
						<Value v={x} />
					</li>
				))}
			</ul>
		);
	}
	if (v && typeof v === "object") {
		const e = Object.entries(v);
		if (!e.length) return <span className="faint">Empty</span>;
		return (
			<dl className="cfg-kv">
				{e.map(([k, x]) => (
					<div key={k}>
						<dt>
							<code>{k}</code>
						</dt>
						<dd>
							<Value v={x} secret={SECRET_KEY.test(k)} />
						</dd>
					</div>
				))}
			</dl>
		);
	}
	if (v === null || v === undefined) return <span className="faint">Not set</span>;
	if (typeof v === "boolean") return <span>{v ? "Yes" : "No"}</span>;
	return <code>{String(v)}</code>;
}
