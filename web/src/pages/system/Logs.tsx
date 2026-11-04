import { useQuery } from "@tanstack/react-query";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ApiError } from "../../api/client";
import { ago, fmtInt } from "../../lib/format";
import { Button, EmptyState, IconButton, Notice, SearchInput, Select, Skeleton, Switch, cx } from "../../ui/components";
import { useToast } from "../../ui/toast";
import { downloadErrorDump, fetchLogs, useClearLogs, useEngineConfig, useErrorDumps, usePatchConfig } from "./api";
import { ConfirmDialog, DownloadGlyph, GlyphButton, Segmented, SubHead, fmtBytes } from "./shared";

type Level = "all" | "warn" | "error";
function levelOf(text: string): Level | "info" | "debug" {
	if (/\[(error|fatal|panic)\s*\]|level=(error|fatal)/i.test(text)) return "error";
	if (/\[warn(ing)?\s*\]|level=warn/i.test(text)) return "warn";
	if (/\[debug\s*\]|level=debug/i.test(text)) return "debug";
	return "info";
}

export function Logs() {
	const cfg = useEngineConfig();
	const toFile = cfg.data?.config.observability?.logs?.["logging-to-file"];
	return (
		<div className="stack-lg">
			<SubHead title="Logs" lede="The engine's own messages and the error reports it saves when a request fails hard. To see which requests went where, use Activity instead." />
			{cfg.isPending ? (
				<div className="panel">
					<Skeleton h={320} r={12} />
				</div>
			) : !toFile && !cfg.isError ? (
				<div className="panel">
					<EmptyState title="File logging is off, so there's no tail to show">
						The engine is writing its messages to the service console instead (<code>journalctl --user -u neoproxy-office</code>). Turn on <strong>Write logs to files</strong> below to read them here.
					</EmptyState>
				</div>
			) : (
				<LogTail />
			)}
			<ErrorDumps />
			<LoggingSettings />
		</div>
	);
}

function LogTail() {
	const toast = useToast();
	const [limit, setLimit] = useState("200");
	const [level, setLevel] = useState<Level>("all");
	const [q, setQ] = useState("");
	const [live, setLive] = useState(false);
	const [confirmClear, setConfirmClear] = useState(false);
	const clear = useClearLogs();
	const logs = useQuery({
		queryKey: ["system", "logs", limit],
		queryFn: () => fetchLogs(Number(limit)),
		refetchInterval: live ? 3000 : false,
		retry: false,
	});
	const viewRef = useRef<HTMLDivElement>(null);
	const stick = useRef(true);
	const lines = useMemo(() => (logs.data?.lines ?? []).map((text, i) => ({ n: i + 1, text, level: levelOf(text) })), [logs.data]);
	const needle = q.trim().toLowerCase();
	const visible = lines.filter((l) => (level === "all" || (level === "warn" ? l.level === "warn" || l.level === "error" : l.level === "error")) && (!needle || l.text.toLowerCase().includes(needle)));

	useLayoutEffect(() => {
		const el = viewRef.current;
		if (el && stick.current) el.scrollTop = el.scrollHeight;
	}, [visible.length, logs.dataUpdatedAt]);
	useEffect(() => {
		stick.current = true;
	}, [level, needle, limit]);

	const disabled = logs.error instanceof ApiError && logs.error.status === 400 && /disabled/i.test(logs.error.message);
	const counts = { warn: lines.filter((l) => l.level === "warn").length, error: lines.filter((l) => l.level === "error").length };

	return (
		<section className="stack" aria-labelledby="lg-tail">
			<h3 className="sr-only" id="lg-tail">
				Log tail
			</h3>
			<div className="lg-toolbar">
				<Segmented
					label="Show"
					value={level}
					onChange={setLevel}
					options={[
						{ value: "all", label: "Everything" },
						{ value: "warn", label: `Warnings and errors${counts.warn + counts.error ? ` (${fmtInt(counts.warn + counts.error)})` : ""}` },
						{ value: "error", label: `Errors${counts.error ? ` (${fmtInt(counts.error)})` : ""}` },
					]}
				/>
				<SearchInput value={q} onChange={setQ} placeholder="Find in log lines" />
				<Select
					ariaLabel="How many lines to load"
					className="lg-limit"
					value={limit}
					onValueChange={setLimit}
					options={["100", "200", "500", "1000", "5000"].map((n) => ({ value: n, label: `Last ${fmtInt(Number(n))} lines` }))}
				/>
				<Switch id="lg-live" checked={live} onCheckedChange={setLive} label="Follow" />
				<IconButton icon="refresh" label="Reload the log now" onClick={() => logs.refetch()} disabled={logs.isFetching} />
			</div>

			<div className="panel lg-panel">
				{logs.isPending ? (
					<div className="stack lg-loading">
						{Array.from({ length: 8 }, (_, i) => (
							<Skeleton key={i} w={`${60 + ((i * 37) % 35)}%`} h={12} />
						))}
					</div>
				) : disabled ? (
					<EmptyState title="File logging is off">Turn on Write logs to files below to read the log here.</EmptyState>
				) : logs.isError ? (
					<EmptyState title="Couldn't read the log" action={<Button onClick={() => logs.refetch()}>Try again</Button>}>
						{(logs.error as Error).message}
					</EmptyState>
				) : visible.length === 0 ? (
					<EmptyState title={lines.length ? "No lines match" : "The log is empty"}>
						{lines.length ? "Show everything, or shorten the search." : "New engine messages appear here. Turn on Follow to watch them arrive."}
					</EmptyState>
				) : (
					<div
						className="lg-view"
						ref={viewRef}
						tabIndex={0}
						role="log"
						aria-label="Engine log"
						onScroll={(e) => {
							const el = e.currentTarget;
							stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
						}}
					>
						{visible.map((l) => (
							<div key={l.n} className={cx("lg-line", `lv-${l.level}`)}>
								<span className="lg-n">{l.n}</span>
								<span className="lg-t">{highlight(l.text, needle)}</span>
							</div>
						))}
					</div>
				)}
				<footer className="lg-foot">
					<span className="faint">
						{logs.data ? `Showing ${fmtInt(visible.length)} of ${fmtInt(lines.length)} lines, loaded ${ago(logs.dataUpdatedAt)}${live ? ", following" : ""}` : ""}
					</span>
					<Button size="sm" variant="ghost" onClick={() => setConfirmClear(true)} disabled={!lines.length}>
						Clear log files
					</Button>
				</footer>
			</div>

			<ConfirmDialog
				open={confirmClear}
				onOpenChange={setConfirmClear}
				title="Clear the engine's log files?"
				description="Every line written so far is deleted from disk; the engine keeps logging new messages. Saved error reports below are kept."
				confirmLabel="Clear log files"
				danger
				busy={clear.isPending}
				onConfirm={async () => {
					try {
						await clear.mutateAsync();
						toast("Log files cleared.");
						setConfirmClear(false);
						logs.refetch();
					} catch (e) {
						toast(`Couldn't clear the log: ${(e as Error).message}`, "bad");
					}
				}}
			/>
		</section>
	);
}

function highlight(text: string, needle: string) {
	if (!needle) return text;
	const out: Array<string | React.ReactElement> = [];
	const lower = text.toLowerCase();
	let i = 0;
	let k = 0;
	for (let j = lower.indexOf(needle); j !== -1; j = lower.indexOf(needle, i)) {
		out.push(text.slice(i, j), <mark key={k++}>{text.slice(j, j + needle.length)}</mark>);
		i = j + needle.length;
	}
	out.push(text.slice(i));
	return out;
}

function ErrorDumps() {
	const dumps = useErrorDumps();
	const toast = useToast();
	const files = [...(dumps.data ?? [])].sort((a, b) => (b.modified ?? 0) - (a.modified ?? 0));
	return (
		<section className="stack" aria-labelledby="lg-dumps">
			<div>
				<h3 className="sys-h3" id="lg-dumps">
					Error reports
				</h3>
				<p className="muted sys-h3-desc">When a request to a provider fails badly, the engine saves the full exchange as a file. Download one to see exactly what the provider answered.</p>
			</div>
			<div className="panel">
				{dumps.isPending ? (
					<div className="stack lg-loading">
						<Skeleton />
						<Skeleton w="60%" />
					</div>
				) : dumps.isError ? (
					<EmptyState title="Couldn't list error reports" action={<Button onClick={() => dumps.refetch()}>Try again</Button>}>
						{(dumps.error as Error).message}
					</EmptyState>
				) : files.length === 0 ? (
					<EmptyState title="No error reports">Nothing has failed badly enough to save one. Reports appear here when it does.</EmptyState>
				) : (
					<ul className="dumps">
						{files.map((f) => (
							<li key={f.name} className="dump">
								<code className="grow dump-name">{f.name}</code>
								<span className="faint num">{fmtBytes(f.size)}</span>
								<span className="faint">{f.modified ? ago(f.modified * 1000) : ""}</span>
								<GlyphButton label={`Download ${f.name}`} onClick={() => downloadErrorDump(f.name).catch((e: Error) => toast(e.message, "bad"))}>
									<DownloadGlyph />
								</GlyphButton>
							</li>
						))}
					</ul>
				)}
			</div>
		</section>
	);
}

type LogSetting = { key: "logging-to-file" | "request-log" | "debug"; label: string; on: string; off: string };
const SETTINGS: LogSetting[] = [
	{
		key: "logging-to-file",
		label: "Write logs to files",
		on: "Engine messages go to rotating files, so they show up on this page.",
		off: "Messages only go to the service console. This page can't show them.",
	},
	{
		key: "debug",
		label: "Detailed engine messages",
		on: "The engine logs much more detail about routing and retries. Useful while chasing a problem; noisy otherwise.",
		off: "Only normal messages, warnings and errors are logged.",
	},
	{
		key: "request-log",
		label: "Record every request in full",
		on: "Every request and response body is written to disk, including prompts in plain text. Uses disk quickly; turn it off when you're done.",
		off: "Request bodies aren't written. Activity still records who asked for what.",
	},
];

function LoggingSettings() {
	const cfg = useEngineConfig();
	const patch = usePatchConfig();
	const toast = useToast();
	const [pending, setPending] = useState<string>();
	const logs = cfg.data?.config.observability?.logs ?? {};
	return (
		<section className="stack" aria-labelledby="lg-settings">
			<div>
				<h3 className="sys-h3" id="lg-settings">
					What gets logged
				</h3>
				<p className="muted sys-h3-desc">Each switch takes effect as soon as you flip it.</p>
			</div>
			{cfg.isError ? (
				<Notice tone="bad" title="Couldn't load logging settings">
					{(cfg.error as Error).message}
				</Notice>
			) : (
				<ul className="panel rt-switches">
					{SETTINGS.map((s) => {
						const on = !!logs[s.key];
						return (
							<li key={s.key}>
								<div className="grow">
									<label className="rt-switch-label" htmlFor={`lg-${s.key}`}>
										{s.label}
									</label>
									<p className="faint rt-switch-text">{cfg.data ? (on ? s.on : s.off) : <Skeleton w="60%" h={12} />}</p>
								</div>
								<Switch
									id={`lg-${s.key}`}
									checked={on}
									disabled={!cfg.data || pending === s.key}
									onCheckedChange={async (v) => {
										setPending(s.key);
										try {
											await patch.mutateAsync({ patch: { observability: { logs: { [s.key]: v } } } });
											toast(`${s.label}: ${v ? "on" : "off"}.`);
										} catch (e) {
											toast(`Couldn't change ${s.label.toLowerCase()}: ${(e as Error).message}`, "bad");
										} finally {
											setPending(undefined);
										}
									}}
								/>
							</li>
						);
					})}
				</ul>
			)}
		</section>
	);
}
