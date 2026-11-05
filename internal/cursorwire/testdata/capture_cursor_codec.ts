// Runs NeoPi's OWN protobuf codec (pi-catalog discovery/protobuf.ts) over
// every message cursor-proto.ts declares, as codec_capture.json.gz:
//
// - encoded: a deterministic message with every field set (each oneof variant
//   in turn, negative int32s, maps whose keys exercise JavaScript property
//   order, packed repeats), encoded by NeoPi;
// - raw/reencoded: that encoding with its top-level wire records reversed and
//   unknown fields spliced in, and what NeoPi's decode + encode makes of it.
//
//   bun capture_cursor_codec.ts
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { gzipSync } = await import("node:zlib");
const proto = await import(root + "/pi-catalog/src/discovery/cursor-proto.ts");
const { create, toBinary, fromBinary } = await import(root + "/pi-catalog/src/discovery/protobuf.ts");
const descriptors = (await Bun.file(import.meta.dir + "/../gen/cursor_proto.json").json()).messages as any[];
const byName = new Map(descriptors.map(m => [m.name, m]));
const schema = (name: string) => (proto as any)[name + "Schema"];

const scalar = (kind: string, no: number, salt: number): any => {
	switch (kind) {
		case "bool":
			return true;
		case "bytes":
			return new Uint8Array([no & 0xff, salt & 0xff, 0x80]);
		case "double":
			return no + 0.5 + salt;
		case "float":
			return no + 0.25;
		case "enum":
			return (no + salt) % 3;
		case "int32":
			return -(no * 1000 + salt);
		case "uint32":
			return no * 100_000 + salt;
		case "int64":
			return -BigInt(no) * 1_000_000_000_000n - BigInt(salt);
		case "uint64":
			return BigInt(no) * 9_000_000_000_000n + BigInt(salt);
		case "string":
			return `s${no}-${salt}-ü\u2028<&>`;
	}
	throw new Error(kind);
};

const build = (name: string, depth: number, variantChoice: number, salt: number): any => {
	const desc = byName.get(name)!;
	const value: any = {};
	for (const field of desc.fields) {
		if (field.kind === "oneof") {
			const variant = field.variants[variantChoice % field.variants.length];
			value[field.name] = {
				case: variant.name,
				value:
					variant.kind === "message"
						? depth < 2
							? build(variant.message, depth + 1, 0, salt + 1)
							: create(schema(variant.message), {})
						: scalar(variant.kind, variant.no, salt),
			};
		} else if (field.kind === "message") {
			const make = (i: number) =>
				depth < 2 ? build(field.message, depth + 1, 0, salt + i) : create(schema(field.message), {});
			value[field.name] = field.repeat ? (depth === 0 ? [make(1), make(2)] : [make(1)]) : make(0);
		} else if (field.kind === "map") {
			const entries: Record<string, any> = {};
			for (const key of ["b", "10", "", "a", "2"]) {
				entries[key] = field.value.message
					? depth < 2
						? build(field.value.message, depth + 1, 0, salt)
						: create(schema(field.value.message), {})
					: scalar(field.value.scalar, field.no, key.length);
			}
			value[field.name] = entries;
		} else if (field.repeat) {
			value[field.name] = [scalar(field.kind, field.no, 1), scalar(field.kind, field.no, 2)];
		} else {
			value[field.name] = scalar(field.kind, field.no, salt);
		}
	}
	return create(schema(name), value);
};

// Top-level wire records of an encoding, as [start, end) offsets.
const records = (bytes: Uint8Array): Uint8Array[] => {
	const out: Uint8Array[] = [];
	let pos = 0;
	const varint = () => {
		let result = 0n;
		let shift = 0n;
		for (;;) {
			const byte = bytes[pos++];
			result |= BigInt(byte & 0x7f) << shift;
			if ((byte & 0x80) === 0) return result;
			shift += 7n;
		}
	};
	while (pos < bytes.length) {
		const start = pos;
		const wire = Number(varint() & 7n);
		if (wire === 0) varint();
		else if (wire === 1) pos += 8;
		else if (wire === 5) pos += 4;
		else {
			const length = Number(varint());
			pos += length;
		}
		out.push(bytes.subarray(start, pos));
	}
	return out;
};
const unknownVarint = new Uint8Array([0xb8, 0x9c, 0x01, 0x2a]); // field 2503, varint 42
const unknownBytes = new Uint8Array([0xc2, 0x9c, 0x01, 0x02, 0x68, 0x69]); // field 2504, "hi"
const concat = (parts: Uint8Array[]) => {
	const total = parts.reduce((n, p) => n + p.length, 0);
	const out = new Uint8Array(total);
	let offset = 0;
	for (const part of parts) {
		out.set(part, offset);
		offset += part.length;
	}
	return out;
};

const cases: any[] = [];
for (const desc of descriptors) {
	const maxVariants = Math.max(1, ...desc.fields.filter((f: any) => f.kind === "oneof").map((f: any) => f.variants.length));
	for (let choice = 0; choice < maxVariants; choice++) {
		const message = build(desc.name, 0, choice, choice);
		const encoded = toBinary(schema(desc.name), message);
		const parts = records(encoded).reverse();
		parts.splice(Math.floor(parts.length / 2), 0, unknownVarint);
		parts.push(unknownBytes);
		const raw = concat(parts);
		let reencoded: Uint8Array;
		try {
			reencoded = toBinary(schema(desc.name), fromBinary(schema(desc.name), raw));
		} catch (error) {
			throw new Error(`${desc.name}#${choice}: ${error} encoded=${Buffer.from(encoded).toString("hex").slice(0, 80)} raw=${Buffer.from(raw).toString("hex").slice(0, 80)}`);
		}
		cases.push({
			type: desc.name,
			choice,
			encoded: Buffer.from(encoded).toString("base64"),
			raw: Buffer.from(raw).toString("base64"),
			reencoded: Buffer.from(reencoded).toString("base64"),
		});
	}
}
const out = JSON.stringify({ source: "NeoPi OWN pi-catalog protobuf.ts codec over cursor-proto.ts", cases });
await Bun.write(import.meta.dir + "/codec_capture.json.gz", gzipSync(out, { level: 9 }));
console.log(`${cases.length} cases, ${out.length} bytes`);
