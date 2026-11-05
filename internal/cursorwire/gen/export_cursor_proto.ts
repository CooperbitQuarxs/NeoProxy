// Exports the message descriptors of NeoPi's OWN generated Cursor protocol
// (pi-catalog/src/discovery/cursor-proto.ts) as cursor_proto.json, the input
// of this directory's Go generator. The module is loaded with its `pb` codec
// factory swapped for a recorder, so the field lists are exactly the ones
// NeoPi's codec encodes, in its declaration order.
//
//   bun export_cursor_proto.ts > cursor_proto.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { mkdtempSync, writeFileSync } = await import("node:fs");
const { tmpdir } = await import("node:os");
const { join } = await import("node:path");

const sourcePath = root + "/pi-catalog/src/discovery/cursor-proto.ts";
const source = await Bun.file(sourcePath).text();
const importLine = 'import { pb, type MessageCodec, type ProtoMessage } from "./protobuf";';
if (!source.includes(importLine)) throw new Error("cursor-proto.ts no longer imports pb as expected");

const dir = mkdtempSync(join(tmpdir(), "cursor-proto-export-"));
writeFileSync(
	join(dir, "recorder.ts"),
	`export const recorded: any[] = [];
export function pb(typeName: string, fields: readonly any[] = []) {
	const ref = { typeName, fields };
	recorded.push(ref);
	return ref;
}`,
);
writeFileSync(join(dir, "cursor-proto.ts"), source.replace(importLine, 'import { pb } from "./recorder";'));
const mod = await import(join(dir, "cursor-proto.ts"));
const { recorded } = await import(join(dir, "recorder.ts"));

const names = new Map<any, string>();
for (const [exportName, value] of Object.entries(mod)) {
	if (exportName.endsWith("Schema") && recorded.includes(value)) names.set(value, exportName.slice(0, -"Schema".length));
}
for (const ref of recorded) {
	if (!names.has(ref)) throw new Error(`message ${ref.typeName} is not exported`);
}

const field = (desc: any): any => {
	switch (desc.kind) {
		case "oneof":
			return { kind: "oneof", name: desc.name, variants: desc.variants.map(field) };
		case "map":
			return {
				kind: "map",
				no: desc.no,
				name: desc.name,
				key: desc.K,
				value: typeof desc.V === "function" ? { message: names.get(desc.V()) } : { scalar: desc.V },
			};
		case "message":
			return { kind: "message", no: desc.no, name: desc.name, message: names.get(desc.T()), repeat: desc.repeat === true };
		default:
			return { kind: desc.kind, no: desc.no, name: desc.name, repeat: desc.repeat === true, optional: desc.optional === true };
	}
};

console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN pi-catalog/src/discovery/cursor-proto.ts (pb descriptors, declaration order)",
			messages: recorded.map((ref: any) => ({ name: names.get(ref), typeName: ref.typeName, fields: ref.fields.map(field) })),
		},
		null,
		1,
	),
);
