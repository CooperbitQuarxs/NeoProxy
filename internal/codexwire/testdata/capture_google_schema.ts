// Runs NeoPi's OWN toolWireSchema then normalizeSchemaForGoogle (the Devin
// Gemini-lane and Gemini tool path) over schemas covering every branch the
// option set reaches, recording each JSON.stringify'd result.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { toolWireSchema } = await import(root + "/pi-ai/src/utils/schema/wire.ts");
const { normalizeSchemaForGoogle } = await import(root + "/pi-ai/src/utils/schema/normalize.ts");

const schemas: Record<string, unknown> = {
	"type-array-nullable": { type: "object", properties: { limit: { type: ["number", "null"], description: "Max hits." }, term: { type: "string" } }, required: ["term"] },
	"type-array-multi": { type: "object", properties: { value: { type: ["string", "array", "null"], items: { type: "string" }, minLength: 2, maxItems: 3 } } },
	"const-anyof": { type: "object", properties: { mode: { anyOf: [{ const: "a" }, { const: "b" }, { const: "a" }], description: "Mode." } } },
	"const-mixed-null": { type: "object", properties: { pick: { oneOf: [{ const: 1 }, { const: null }] } } },
	"bare-const": { type: "object", properties: { fixed: { const: "x" }, num: { const: 3 } } },
	"bare-enum": { type: "object", properties: { size: { enum: ["s", "m"] }, level: { enum: [1, 2] }, mixed: { type: "string", enum: ["a", 2] } } },
	"null-type": { type: "object", properties: { nothing: { type: "null", description: "Nada." } } },
	"anyof-null": { type: "object", properties: { maybe: { anyOf: [{ type: "string", maxLength: 5 }, { type: "null" }] }, many: { anyOf: [{ type: "string" }, { type: "integer" }, { type: "null" }] } } },
	"spill": { type: "object", properties: { id: { type: "string", pattern: "^[a-z]+$", format: "uuid", default: "abc", examples: ["x"], $comment: "c" }, n: { type: "integer", minimum: 1, maximum: 9, description: "Count." } } },
	"refs": { type: "object", properties: { node: { $ref: "#/$defs/Node", description: "Root node." }, list: { type: "array", items: { $ref: "#/definitions/Leaf" } } }, $defs: { Node: { type: "object", properties: { child: { $ref: "#/$defs/Node" }, name: { type: "string" } } } }, definitions: { Leaf: { type: "string", enum: ["x", "y"] } } },
	"boolean-subschemas": { type: "object", properties: { anything: true, never: false, arr: { type: "array", items: true } }, additionalProperties: false },
	"snake-case": { type: "object", properties: { opts: { type: "object", additional_properties: false, any_of: [{ type: "object", properties: { a: { type: "string" } } }] } } },
	"empty-object": { type: "object", properties: { cfg: { type: "object" }, nested: { type: "object", properties: { one: { type: "boolean" } } } } },
	"not-enum": { type: "object", properties: { s: { type: "string", not: { enum: [1] } }, t: { type: "string", not: { const: "z" } } } },
	"draft7-items-tuple": { $schema: "http://json-schema.org/draft-07/schema#", type: "object", properties: { pair: { type: "array", items: [{ type: "string" }, { type: "number" }] }, tags: { type: "array", items: { type: "string" }, uniqueItems: true } } },
	"property-named-const": { type: "object", properties: { const: { type: "string" }, type: { type: "string" } } },
};

const cases = Object.entries(schemas).map(([name, parameters]) => {
	const wire = toolWireSchema({ name, description: "", parameters } as any);
	return { name, parameters, wire: JSON.stringify(wire), google: JSON.stringify(normalizeSchemaForGoogle(wire)) };
});
console.log(JSON.stringify({ source: "NeoPi OWN toolWireSchema + normalizeSchemaForGoogle", cases }, null, 2));
