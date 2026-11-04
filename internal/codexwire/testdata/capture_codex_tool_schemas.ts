// Runs NeoPi's OWN convertOpenAICodexResponsesTools over raw JSON Schema tools
// and records the wire tool payloads. No network access.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { convertOpenAICodexResponsesTools } = await import(root + "/pi-ai/src/providers/openai-codex-responses.ts");

const object = (properties: Record<string, unknown>, required?: string[], extra: Record<string, unknown> = {}) => ({
	type: "object",
	properties,
	...(required ? { required } : {}),
	...extra,
});
const scenarios: Array<{ name: string; strict?: boolean; parameters: Record<string, unknown> }> = [
	{ name: "strict-optional", strict: true, parameters: object({ term: { type: "string", description: "Search term" }, limit: { type: "integer", minimum: 1, default: 5, description: "Max hits" } }, ["term"]) },
	{ name: "strict-nullable-union", strict: true, parameters: object({ mode: { type: ["string", "null"], enum: ["a", "b", null] }, tag: { anyOf: [{ type: "string" }, { type: "null" }] } }, ["mode"]) },
	{ name: "strict-draft07", strict: true, parameters: { $schema: "http://json-schema.org/draft-07/schema#", type: "object", definitions: { point: object({ x: { type: "number" }, y: { type: "number" } }, ["x", "y"]) }, properties: { at: { $ref: "#/definitions/point", description: "Where" }, pair: { type: "array", items: [{ type: "string" }, { type: "number" }] }, maybe: { type: "string", nullable: true } }, required: ["at"] } },
	{ name: "strict-const-enum", strict: true, parameters: object({ kind: { const: "fixed" }, level: { enum: ["low", "high"] }, choice: { anyOf: [{ const: "x", description: "Pick" }, { const: "y", description: "Pick" }], description: "Pick" } }, ["kind", "level", "choice"]) },
	{ name: "strict-unrepresentable", strict: true, parameters: object({ meta: { type: "object", additionalProperties: true } }, ["meta"]) },
	{ name: "strict-empty-branch", strict: true, parameters: object({ value: { anyOf: [{ type: "string" }, {}] } }, ["value"]) },
	{ name: "non-strict-draft07", strict: false, parameters: { type: "object", properties: { list: { type: "array", items: [{ type: "string" }], additionalItems: false }, any: {} }, dependencies: { list: ["any"] } } },
	{ name: "undeclared-strict", parameters: object({ q: { type: "string", pattern: "^(?!x)" } }, ["q"]) },
];
const fixtures = scenarios.map(scenario => {
	const tool = { name: "lookup", description: "Find things.", parameters: scenario.parameters, ...(scenario.strict === undefined ? {} : { strict: scenario.strict }) };
	const [wire] = convertOpenAICodexResponsesTools([tool as any], { applyPatchToolType: undefined, supportsComputerUse: undefined } as any);
	return {
		name: scenario.name,
		downstream: { type: "function", name: tool.name, description: tool.description, parameters: scenario.parameters, ...(scenario.strict === undefined ? {} : { strict: scenario.strict }) },
		wire,
	};
});
console.log(JSON.stringify({ source: "NeoPi OWN convertOpenAICodexResponsesTools", fixtures }, null, 2));
