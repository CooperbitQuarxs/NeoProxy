// Capture the official NeoPi bundled catalog; no profile, network or credentials.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const {getBundledModels} = await import(root + "/pi-catalog/src/models.ts");
const models = getBundledModels("anthropic").map((m: any) => ({
  id: m.id, object: "model", created: 0, owned_by: "anthropic", type: "claude",
  display_name: m.name,
  ...(m.contextWindow ? {inputTokenLimit:m.contextWindow, context_length:m.contextWindow} : {}),
  ...(m.maxTokens ? {outputTokenLimit:m.maxTokens, max_completion_tokens:m.maxTokens} : {}),
  supportedInputModalities: m.input.map((x: string) => x.toUpperCase()),
  supportedOutputModalities: ["TEXT"],
  ...(m.reasoning ? {thinking:{...(m.thinking?.mode !== "anthropic-adaptive" ? {min:1024,max:m.maxTokens ?? 0} : {}),zero_allowed:!m.compat?.requiresThinkingEnabled,dynamic_allowed:true,...(m.thinking?.efforts ? {levels:m.thinking.efforts} : {})}} : {}),
})).sort((a: any,b: any)=>a.id.localeCompare(b.id));
const json = JSON.stringify(models,null,2) + "\n";
if (process.argv[2]) await Bun.write(process.argv[2],json);
else console.log(json);
