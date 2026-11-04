// Capture NeoPi's own catalog projection, including neutral discovery rows.
const modules = process.env.NEOPI_SOURCE_MODULES;
if (!modules) throw new Error("NEOPI_SOURCE_MODULES is required");
const { getBundledModels } = await import(modules + "/pi-catalog/src/models.ts");
const { buildModel } = await import(modules + "/pi-catalog/src/build.ts");
const profiles: Record<string, unknown> = {};
for (const bundled of getBundledModels("anthropic")) {
  const model = buildModel(bundled);
  profiles[model.id] = { reasoning: model.reasoning, maxTokens: model.maxTokens, thinking: model.thinking, compat: model.compat };
}
for (const id of ["claude-unfamiliar", "claude-opus-6", "claude-sonnet-6", "claude-haiku-6", "claude-fable-6", "claude-mythos-6"]) {
  const model = buildModel({ id, name:id, api:"anthropic-messages", provider:"anthropic", baseUrl:"https://api.anthropic.com", reasoning:false, input:["text"], cost:{input:0,output:0,cacheRead:0,cacheWrite:0},contextWindow:200000,maxTokens:64000 });
  profiles[id] = { reasoning:model.reasoning, maxTokens:model.maxTokens, thinking:model.thinking, compat:model.compat };
}
console.log(JSON.stringify(profiles, null, 2));
