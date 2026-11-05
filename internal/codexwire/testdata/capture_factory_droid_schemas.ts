// Runs NeoPi's OWN Factory Gemini tool-schema projection,
// normalizeSchemaForFactoryDroid(dereferenceJsonSchema(toolWireSchema(tool))),
// over a corpus of parameter schemas and writes
// testdata/factory_droid_schemas.json: [{name, parameters, wire}].
//
//   cd <neopi>/env && HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks capture_factory_droid_schemas.ts
import { join } from "node:path";

const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { normalizeSchemaForFactoryDroid, dereferenceJsonSchema, toolWireSchema } = await import(root + "/pi-ai/src/utils/schema/index.ts");

const object = (properties: Record<string, unknown>, extra: Record<string, unknown> = {}) => ({ type: "object", properties, ...extra });
const corpus: Record<string, unknown> = {
	plain: object({ term: { type: "string" } }, { required: ["term"] }),
	"key-order": { required: ["a"], properties: { a: { description: "A.", type: "string", maxLength: 9, minLength: 1 } }, type: "object", additionalProperties: false },
	"dropped-keywords": object({ a: { type: "string", examples: ["x"], $comment: "c", deprecated: true, readOnly: true, pattern: "^a" } }),
	"const-and-enum": object({ c: { const: 3 }, e: { enum: [1, "two", true, null] }, s: { type: "string", enum: ["a", "b"] } }),
	"type-array-nullable": object({ n: { type: ["string", "null"] }, m: { type: ["null", "integer", "string"] } }),
	"nullable-anyof": object({ v: { anyOf: [{ type: "string" }, { type: "null" }] } }),
	"mixed-anyof-kept": object({ v: { description: "Either.", anyOf: [{ type: "string" }, { type: "number" }] } }),
	"mixed-anyof-narrowed": object({ v: { type: "string", minLength: 2, anyOf: [{ type: "string", maxLength: 5 }, { type: "array", items: { type: "string" } }] } }),
	"same-type-union-merge": object({
		v: {
			oneOf: [
				object({ a: { type: "string" }, b: { type: "string" } }, { required: ["a", "b"] }),
				object({ a: { type: "string" }, c: { type: "integer" } }, { required: ["a", "c"] }),
			],
		},
	}),
	"allof-conjunction": object({
		v: { allOf: [{ type: "integer", minimum: 1, maximum: 10 }, { minimum: 3, maximum: 20 }] },
		w: { allOf: [object({ a: { type: "string" } }, { required: ["a"] }), object({ b: { type: "string" } }, { required: ["b"] })] },
	}),
	"allof-into-union": object({
		v: {
			allOf: [{ anyOf: [{ type: "string", enum: ["a", "b"] }, { type: "number" }] }, { anyOf: [{ type: "string", enum: ["b", "c"] }, { type: "integer" }] }],
		},
	}),
	"refs-and-defs": {
		type: "object",
		$defs: { Point: object({ x: { type: "number" }, y: { type: "number" } }, { required: ["x", "y"] }) },
		properties: { from: { $ref: "#/$defs/Point" }, to: { $ref: "#/$defs/Point", description: "Target." } },
		required: ["from"],
	},
	"draft07-definitions": {
		type: "object",
		definitions: { Name: { type: "string", minLength: 1 } },
		properties: { name: { $ref: "#/definitions/Name" }, tags: { type: "array", items: { $ref: "#/definitions/Name" }, minItems: 1 } },
	},
	"untyped-inference": { properties: { list: { items: { type: "string" } }, choice: { enum: ["x", "y"] }, nested: { properties: { a: { type: "boolean" } } } } },
	"required-undefined-in-union": object({ v: { anyOf: [object({ a: { type: "string" } }, { required: ["a", "missing"] }), { type: "string" }] } }),
	"bounds-defaults": object({ n: { type: "integer", minimum: 0, maximum: 100, default: 5, example: 7, format: "int32", title: "N" } }),
	"empty-object": { type: "object" },
	"no-params": {},
};

const out = Object.entries(corpus).map(([name, parameters]) => ({
	name,
	parameters,
	wire: JSON.stringify(normalizeSchemaForFactoryDroid(dereferenceJsonSchema(toolWireSchema({ name: "t", description: "", parameters } as any)))),
}));
await Bun.write(join(import.meta.dir, "factory_droid_schemas.json"), JSON.stringify({ source: "NeoPi normalizeSchemaForFactoryDroid(dereferenceJsonSchema(toolWireSchema))", cases: out }, null, "\t") + "\n");
console.log(`${out.length} schemas`);
