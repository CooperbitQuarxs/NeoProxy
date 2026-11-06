// Runs NeoPi's OWN eventstream decoder (packages/ai/src/providers/aws-eventstream.ts)
// over crafted byte streams: Bedrock's string-header event and exception
// frames, every typed header, CRC and framing failures, and chunk splits.
// Each case records the decoded messages (headers, base64 payload) in order and
// the error that ended the stream, if any.
//
// Writes testdata/neopi_eventstream.json.gz.
//
//   cd <neopi-nightly> && HOME=$(mktemp -d) NEOPI_CHECKOUT=$PWD bun <this file>
import { writeFileSync } from "node:fs";
import { join } from "node:path";

const checkout = process.env.NEOPI_CHECKOUT ?? "/home/shayna/source/github/PsychedelicShayna/neopi-nightly";
const { crc32, decodeEventStream, decodeMessage } = await import(join(checkout, "packages/ai/src/providers/aws-eventstream.ts"));

const enc = new TextEncoder();
const bytes = (...parts: (number[] | Uint8Array)[]) => Uint8Array.from(parts.flatMap(p => Array.from(p)));
const u16 = (n: number) => [(n >> 8) & 255, n & 255];
const u32 = (n: number) => [(n >>> 24) & 255, (n >>> 16) & 255, (n >>> 8) & 255, n & 255];
const u64 = (n: bigint) => {
	const v = BigInt.asUintN(64, n);
	return Array.from({ length: 8 }, (_, i) => Number((v >> BigInt(56 - 8 * i)) & 255n));
};
const name = (s: string | Uint8Array) => {
	const b = typeof s === "string" ? enc.encode(s) : s;
	return [b.length, ...b];
};
const str = (n: string, v: string | Uint8Array) => {
	const b = typeof v === "string" ? enc.encode(v) : v;
	return bytes(name(n), [7], u16(b.length), b);
};
const strHeaders = (h: Record<string, string>) => bytes(...Object.entries(h).map(([k, v]) => str(k, v)));

// frame builds a message; headersLen and the CRCs can be overridden to craft
// malformed frames that still pass the checks before the one under test.
function frame(headers: Uint8Array, payload: Uint8Array | string, o: { headersLen?: number; badPrelude?: boolean; badMessage?: boolean } = {}) {
	const body = typeof payload === "string" ? enc.encode(payload) : payload;
	const total = 16 + headers.length + body.length;
	const prelude = bytes(u32(total), u32(o.headersLen ?? headers.length));
	let pcrc = crc32(prelude);
	if (o.badPrelude) pcrc ^= 0xff;
	const head = bytes(prelude, u32(pcrc), headers, body);
	let mcrc = crc32(head);
	if (o.badMessage) mcrc ^= 0xff;
	return bytes(head, u32(mcrc));
}

const ev = (type: string, payload: unknown) =>
	frame(strHeaders({ ":event-type": type, ":content-type": "application/json", ":message-type": "event" }), JSON.stringify(payload));

const typed = bytes(
	name("t"), [0],
	name("f"), [1],
	name("b"), [2, 0xfe],
	name("s"), [3], u16(0x8001),
	name("i"), [4], u32(0xfffffffe),
	name("l"), [5], u64(-5n),
	name("L"), [5], u64(9007199254740993n),
	name("y"), [6], u16(3), [0, 0xff, 7],
	name("str"), [7], u16(enc.encode("héy").length), enc.encode("héy"),
	name("ts"), [8], u64(1730851200123n),
	name("tsneg"), [8], u64(-1n),
	name("tsbig"), [8], u64(253402300800000n),
	name("tsold"), [8], u64(-62198755200000n),
	name("u"), [9], Array.from({ length: 16 }, (_, i) => i * 17),
);

type Case = { name: string; chunks: Uint8Array[]; single?: boolean };
const stream = [
	ev("messageStart", { role: "assistant" }),
	ev("contentBlockDelta", { contentBlockIndex: 0, delta: { text: "hi" } }),
	ev("messageStop", { stopReason: "end_turn" }),
];
const joined = bytes(...stream);
const cases: Case[] = [
	{ name: "bedrock events in one chunk", chunks: [joined] },
	{ name: "bedrock events split at every byte", chunks: Array.from(joined, b => Uint8Array.of(b)) },
	{ name: "bedrock events split mid prelude", chunks: [joined.subarray(0, 2), joined.subarray(2, 70), joined.subarray(70)] },
	{ name: "exception frame", chunks: [frame(strHeaders({ ":message-type": "exception", ":exception-type": "validationException", ":content-type": "application/json" }), '{"message":"input too long"}')] },
	{ name: "typed headers", chunks: [frame(typed, "{}")] },
	{ name: "empty payload and headers", chunks: [frame(new Uint8Array(0), new Uint8Array(0))] },
	{ name: "duplicate and proto headers", chunks: [frame(bytes(str("a", "1"), str("a", "2"), str("__proto__", "x")), "{}")] },
	{ name: "byte order marks", chunks: [frame(bytes(str("\uFEFFn", "\uFEFFv"), str("m", "\uFEFF\uFEFFw")), "")] },
	{ name: "invalid utf-8", chunks: [frame(bytes(str("x", Uint8Array.of(0x61, 0xe2, 0x82, 0x62, 0xed, 0xa0, 0x80, 0xf0, 0x9f, 0x98, 0xc0, 0xff, 0xf4, 0x90, 0x80, 0x80))), "")] },
	{ name: "string header value truncated", chunks: [frame(bytes(name("s"), [7], u16(10), enc.encode("abc")), "", { headersLen: 9 })] },
	{ name: "long header truncated", chunks: [frame(bytes(name("l"), [5], [0xff, 0xff, 0xff]), "")] },
	{ name: "timestamp header truncated", chunks: [frame(bytes(name("t"), [8], [1, 0]), "")] },
	{ name: "timestamp out of range", chunks: [frame(bytes(name("t"), [8], u64(8640000000000001n)), "")] },
	{ name: "short header truncated", chunks: [frame(bytes(name("s"), [3], [1]), "")] },
	{ name: "uuid header truncated", chunks: [frame(bytes(name("u"), [9], [1, 2, 3]), "")] },
	{ name: "missing value type", chunks: [frame(bytes(name("abc")), "")] },
	{ name: "unknown header type", chunks: [frame(bytes(name("q"), [10]), "")] },
	{ name: "headers length past payload", chunks: [frame(str("a", "b"), "zz", { headersLen: 9 })] },
	{ name: "headers length past frame", chunks: [frame(bytes(str("a", "b")), "", { headersLen: 1000 })] },
	{ name: "byte array header into payload", chunks: [frame(bytes(name("y"), [6], u16(4)), "WXYZ", { headersLen: 5 })] },
	{ name: "prelude crc mismatch", chunks: [frame(strHeaders({ ":event-type": "x" }), "", { badPrelude: true })] },
	{ name: "message crc mismatch", chunks: [frame(strHeaders({ ":event-type": "x" }), "{}", { badMessage: true })] },
	{ name: "error after good frame", chunks: [bytes(stream[0], frame(strHeaders({ ":event-type": "x" }), "{}", { badMessage: true }))] },
	{ name: "total below minimum", chunks: [bytes(stream[0], u32(15), [0, 0, 0, 0])] },
	{ name: "truncated at end", chunks: [bytes(stream[0], stream[1].subarray(0, 20))] },
	{ name: "trailing partial length", chunks: [bytes(stream[0], [0, 0])] },
	{ name: "empty stream", chunks: [] },
	{ name: "decodeMessage frame too short", chunks: [new Uint8Array(15)], single: true },
	{ name: "decodeMessage length mismatch", chunks: [bytes(stream[0], [0])], single: true },
];

const hex = (b: Uint8Array) => Buffer.from(b).toString("hex");
const shape = (m: { headers: Record<string, string>; payload: Uint8Array }) => ({
	headers: m.headers,
	payload: Buffer.from(m.payload).toString("base64"),
});
const results: any[] = [];
for (const c of cases) {
	const messages: any[] = [];
	let error: string | undefined;
	try {
		if (c.single) {
			messages.push(shape(decodeMessage(c.chunks[0])));
		} else {
			const body = new ReadableStream<Uint8Array>({
				start(controller) {
					for (const chunk of c.chunks) controller.enqueue(new Uint8Array(chunk));
					controller.close();
				},
			});
			for await (const m of decodeEventStream(body)) messages.push(shape(m));
		}
	} catch (e: any) {
		error = `${e?.name ?? "Error"}: ${e?.message ?? e}`;
	}
	results.push({ name: c.name, ...(c.single ? { single: true } : {}), chunks: c.chunks.map(hex), messages, ...(error !== undefined ? { error } : {}) });
}

const out = join(import.meta.dir, "neopi_eventstream.json.gz");
writeFileSync(out, Bun.gzipSync(JSON.stringify({
	source: "NeoPi OWN packages/ai/src/providers/aws-eventstream.ts decodeEventStream/decodeMessage",
	crc32: { "123456789": crc32(enc.encode("123456789")), empty: crc32(new Uint8Array(0)) },
	results,
})));
console.log(`wrote ${results.length} cases to ${out}`);
