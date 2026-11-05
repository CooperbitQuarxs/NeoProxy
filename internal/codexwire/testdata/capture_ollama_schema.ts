// Runs NeoPi's OWN toolWireSchema then sanitizeSchemaForOllama (the
// ollama-chat convertTools path) over schemas reaching every branch,
// recording each JSON.stringify'd result. Run from the NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi bun --preserve-symlinks <this file> > ollama_schema_capture.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { toolWireSchema } = await import(root + "/pi-ai/src/utils/schema/wire.ts");
const { sanitizeSchemaForOllama } = await import(root + "/pi-ai/src/utils/schema/normalize.ts");

const obj = (properties: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({ type: "object", properties, ...extra });
const schemas: Record<string, unknown> = {
	"plain": obj({ path: { type: "string", description: "File path." }, n: { type: "integer", minimum: 1 } }, { required: ["path"] }),
	"empty-parameters": {},
	"boolean-subschemas": obj({ anything: true, never: false, arr: { type: "array", items: true }, open: {} }, { additionalProperties: false }),
	"additional-properties": obj({ strict: { type: "object", additionalProperties: false }, loose: { type: "object", additionalProperties: true }, typed: { type: "object", additionalProperties: { type: "string" } }, open: { type: "object", additionalProperties: {} } }),
	"unevaluated": obj({ a: { type: "object", unevaluatedProperties: false, unevaluatedItems: true } }),
	"type-array-nullable": obj({ limit: { type: ["number", "null"] }, only: { type: ["null"] }, dup: { type: ["string", "string", "null"] } }),
	"type-array-multi": obj({ v: { type: ["string", "array", "null"], items: { type: "string" } }, w: { type: ["integer", "boolean"], allOf: [{ minimum: 0 }] } }),
	"type-array-allof-first": obj({ v: { allOf: [{ description: "x" }], type: ["string", "number"] } }),
	"type-array-odd": obj({ empty: { type: [], description: "Nothing." }, numbers: { type: [1, 2] } }),
	"combiners": obj({ a: { anyOf: [{ type: "string" }, true] }, b: { oneOf: [false, { type: ["string", "null"] }] }, c: { allOf: [{ type: "object", properties: { x: true } }] } }),
	"tuple": obj({ pair: { type: "array", prefixItems: [{ type: "string" }, true], items: false } }),
	"draft7-items-array": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { pair: { type: "array", items: [{ type: "string" }, true], additionalItems: false } } },
	"conditionals": obj({ c: { if: { properties: { k: { const: "a" } } }, then: true, else: false, not: { type: ["string", "null"] } } }),
	"value-keys": obj({ s: { type: "string", propertyNames: true, contentSchema: false }, arr: { type: "array", contains: true } }),
	"maps": { type: "object", properties: { node: { $ref: "#/$defs/Node" } }, $defs: { Node: { type: ["object", "null"], properties: { child: true } } }, definitions: { Leaf: false }, patternProperties: { "^x": { type: ["string", "integer"] } }, dependentSchemas: { a: true }, dependencies: { b: ["a"], c: { type: ["string", "null"] } } },
	"non-schema-positions": obj({ e: { enum: [true, false, ["a", "b"]] }, d: { default: { additionalProperties: false, type: ["a", "b"] } }, x: { examples: [true] } }),
	"property-named-keywords": obj({ type: { type: ["string", "null"] }, items: { type: "boolean" }, additionalProperties: { type: "string" } }),
	"root-type-array": { type: ["object", "null"], properties: { a: { type: "string" } } },
};

const cases = Object.entries(schemas).map(([name, parameters]) => {
	const wire = toolWireSchema({ name, description: "", parameters } as any);
	return { name, parameters, wire: JSON.stringify(wire), ollama: JSON.stringify(sanitizeSchemaForOllama(wire)) };
});
console.log(JSON.stringify({ source: "NeoPi OWN toolWireSchema + sanitizeSchemaForOllama", cases }, null, 2));
