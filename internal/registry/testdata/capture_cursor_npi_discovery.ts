// Runs NeoPi's OWN Cursor model resolution (resolveProviderModels with
// cursorModelManagerOptions: fetchCursorUsableModels, the bundled-catalog
// merge, authoritative retention and collapseBuiltVariants) over live
// discovery bodies, with the clock pinned, and records:
//
// - the three bodies (cursor_discovery_bodies.json.gz beside this script);
// - per scenario (which RPCs fail), the order fetchCursorUsableModels
//   returns and the final roster as spec records;
// - the three discovery requests on the wire, served from loopback through
//   NeoPi's default fetch.
//
//   bun capture_cursor_npi_discovery.ts <dir with AvailableModels.bin GetUsableModels.bin GetDefaultModelForCli.bin>
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const live = process.argv[2];
if (!live) throw new Error("usage: capture_cursor_npi_discovery.ts <live discovery dir>");
const { gzipSync } = await import("node:zlib");
const { fetchCursorUsableModels } = await import(root + "/pi-catalog/src/discovery/cursor.ts");
const { resolveProviderModels } = await import(root + "/pi-catalog/src/model-manager.ts");
const { cursorModelManagerOptions } = await import(root + "/pi-catalog/src/provider-models/special.ts");
const { specRecord } = await import(import.meta.dir + "/capture_cursor_npi_catalog_records.ts");

const names = ["AvailableModels", "GetDefaultModelForCli", "GetUsableModels"] as const;
const bodies: Record<string, Uint8Array> = {};
for (const name of names) bodies[name] = new Uint8Array(await Bun.file(`${live}/${name}.bin`).arrayBuffer());
await Bun.write(
	import.meta.dir + "/cursor_discovery_bodies.json.gz",
	gzipSync(JSON.stringify(Object.fromEntries(names.map(name => [name, Buffer.from(bodies[name]).toString("base64")]))), { level: 9 }),
);

const now = 1_793_800_000_000; // 2026-11-04T13:46:40Z, before gemini-4-argon's dated card
const nativeNow = Date.now;
const nativeFetch = globalThis.fetch;
let failing = new Set<string>();
const scenarios: any[] = [];
try {
	Date.now = () => now;
	globalThis.fetch = Object.assign(
		async (input: any) => {
			const name = new URL(String(input instanceof Request ? input.url : input)).pathname.split("/").pop()!;
			return failing.has(name) ? new Response("", { status: 503 }) : new Response(bodies[name]);
		},
		{ preconnect: () => {} },
	) as typeof fetch;
	for (const [label, fail] of [
		["rich", []],
		["legacy", ["AvailableModels"]],
		["rich-only", ["GetUsableModels"]],
		["no-default", ["GetDefaultModelForCli"]],
		["failed", ["AvailableModels", "GetUsableModels"]],
	] as const) {
		failing = new Set(fail);
		const discovered = await fetchCursorUsableModels({ apiKey: "x" });
		const db = `/tmp/cursor-discovery-capture-${label}-${process.pid}.db`;
		const result = await resolveProviderModels({ ...cursorModelManagerOptions({ apiKey: "x" }), cacheDbPath: db, now: () => now }, "online");
		await Bun.$`rm -f ${db} ${db}-wal ${db}-shm`.quiet();
		scenarios.push({
			name: label,
			fail,
			discovered: discovered === null ? null : discovered.map((spec: any) => spec.id),
			models: result.models.map(specRecord),
		});
	}
} finally {
	Date.now = nativeNow;
	globalThis.fetch = nativeFetch;
}

const apiKey = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYXB0dXJlIn0.c2lnbmF0dXJl";
const requests: any[] = [];
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(incoming) {
		const path = new URL(incoming.url).pathname;
		const headers = Object.fromEntries(incoming.headers);
		delete headers.host;
		requests.push({ method: incoming.method, path, headers, body: Buffer.from(await incoming.arrayBuffer()).toString("base64") });
		return new Response(bodies[path.split("/").pop()!], { headers: { "content-type": "application/proto" } });
	},
});
try {
	await fetchCursorUsableModels({ apiKey, baseUrl: `http://127.0.0.1:${server.port}` });
} finally {
	server.stop(true);
}
requests.sort((a, b) => a.path.localeCompare(b.path));

console.log(JSON.stringify({ source: "NeoPi OWN resolveProviderModels(cursorModelManagerOptions) over live discovery bodies", now, apiKey, requests, scenarios }, null, 1));
