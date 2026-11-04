// Runs NeoPi's OWN Devin discovery (fetchDevinModels, then buildModel as the
// model manager does) over a real GetCliModelConfigs response with every
// config enabled, so each family, router and fusion branch is exercised. The
// response is served from loopback through NeoPi's default discovery fetch,
// so the recorded request is what goes on the wire.
//
//   bun capture_devin_npi_discovery.ts <live-response.bin>
//
// Writes devin_cli_model_configs.bin.gz (the enabled response) beside this
// script and prints the request NeoPi sent plus the NeoPiModel records it
// built (shaped by capture_devin_npi_models.ts's neoPiRecord), with the
// loopback base URL's port removed.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const source = process.argv[2];
if (!source) throw new Error("usage: capture_devin_npi_discovery.ts <GetCliModelConfigs response .bin>");
const { gzipSync } = await import("node:zlib");
const proto = await import(root + "/pi-catalog/src/discovery/devin-proto.ts");
const { fromBinary, toBinary } = await import(root + "/pi-catalog/src/discovery/protobuf.ts");
const { fetchDevinModels } = await import(root + "/pi-catalog/src/discovery/devin.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const { neoPiRecord } = await import(import.meta.dir + "/../../runtime/executor/testdata/capture_devin_npi_models.ts");

const response = fromBinary(proto.GetCliModelConfigsResponseSchema, new Uint8Array(await Bun.file(source).arrayBuffer()));
for (const config of response.clientModelConfigs) config.disabled = false;
const body = toBinary(proto.GetCliModelConfigsResponseSchema, response);
await Bun.write(import.meta.dir + "/devin_cli_model_configs.bin.gz", gzipSync(body, { level: 9 }));

const apiKey = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjYXB0dXJlIn0.c2lnbmF0dXJl";
let request: any;
const server = Bun.serve({
	hostname: "127.0.0.1",
	port: 0,
	async fetch(incoming) {
		const sent = new Uint8Array(await incoming.arrayBuffer());
		request = {
			method: incoming.method,
			path: new URL(incoming.url).pathname,
			headers: Object.fromEntries(incoming.headers),
			body: Buffer.from(sent).toString("base64"),
		};
		return new Response(body, { headers: { "content-type": "application/proto" } });
	},
});
const baseUrl = `http://127.0.0.1:${server.port}`;
let specs: any[] | null;
try {
	specs = await fetchDevinModels({ apiKey, baseUrl });
} finally {
	server.stop(true);
}
if (!specs) throw new Error("fetchDevinModels returned null");
console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN fetchDevinModels + buildModel over a live GetCliModelConfigs response, all configs enabled",
			apiKey,
			baseUrl: "http://127.0.0.1",
			request,
			models: specs.map((spec: any) => ({ ...neoPiRecord(buildModel(spec)), baseUrl: "http://127.0.0.1" })),
		},
		null,
		2,
	),
);
