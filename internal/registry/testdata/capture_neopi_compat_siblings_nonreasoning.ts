// Leave-one-out measurement behind neoPiNewRow (neopi_completions_discovery.go): how often the nearest
// bundled sibling's compat (and effort ladder, for reasoning rows) equals what NeoPi's own buildModel
// derives for an id, against one provider-wide default. Prints one line per provider. Run like
// capture_neopi_completions_discovery.ts.
// Leave-one-out measurement behind neoPiNewRow (neopi_completions_discovery.go): how often the nearest bundled
// sibling's compat equals what NeoPi's own buildModel derives for an id, against one provider-wide default.
// Run like capture_neopi_completions_discovery.ts; prints one line per provider.
const root = process.env.NEOPI_SOURCE_MODULES;
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const provs = "cerebras,deepseek,fireworks,groq,huggingface,mistral,moonshot,nanogpt,nvidia,together,alibaba-token-plan".split(",");
const build = (provider:string, id:string, baseUrl:string, reasoning=false) => buildModel({id,name:id,api:"openai-completions",provider,baseUrl,reasoning,input:["text"],cost:{input:0,output:0,cacheRead:0,cacheWrite:0},contextWindow:null,maxTokens:null} as any);
const canon = (o:any)=>JSON.stringify(Object.fromEntries(Object.entries(o??{}).sort(([a],[b])=>a<b?-1:1)));
const lcp=(a:string,b:string)=>{a=a.toLowerCase();b=b.toLowerCase();let i=0;while(i<a.length&&i<b.length&&a[i]===b[i])i++;return i;};
for (const p of provs) {
  const rows = getBundledModels(p).filter((m:any)=>m.api==="openai-completions");
  const baseUrl = rows[0].baseUrl;
  const unknown = canon(build(p,"zz-unknown-model-1",baseUrl).compat);
  let n=0, sibOk=0, unkOk=0, either=0; const th=[0.3,0.5,0.7,0.85].map(()=>0);
  for (const r of rows) {
    const target = canon(build(p, r.id, baseUrl).compat);   // what NeoPi derives for this id as an unbundled non-reasoning row
    n++;
    let best:any=null, bl=-1;
    for (const o of rows) { if (o===r||o.reasoning) continue; const l=lcp(o.id,r.id); if(l>bl){bl=l;best=o;} }
    const ratio = bl / r.id.length; [0.3,0.5,0.7,0.85].forEach((t,i)=>{ const use = best && ratio>=t; const pred = use ? canon(build(p,best.id,baseUrl).compat) : unknown; if (pred===target) th[i]++; });
    const sibMatch = best && canon(build(p,best.id,baseUrl).compat)===target;
    const unkMatch = unknown===target;
    if(sibMatch)sibOk++; if(unkMatch)unkOk++; if(sibMatch||unkMatch)either++;
  }
  console.log(p, "rows", n, "nearest-nonreasoning-sibling ok", sibOk, "unknown ok", unkOk, "thresholded(.3,.5,.7,.85)", th.join(","));
}
