// Leave-one-out measurement behind neoPiNewRow (neopi_completions_discovery.go): how often the nearest
// bundled sibling's compat (and effort ladder, for reasoning rows) equals what NeoPi's own buildModel
// derives for an id, against one provider-wide default. Prints one line per provider. Run like
// capture_neopi_completions_discovery.ts.
const root = process.env.NEOPI_SOURCE_MODULES;
const { getBundledModels } = await import(root + "/pi-catalog/src/models.ts");
const { buildModel } = await import(root + "/pi-catalog/src/build.ts");
const build = (provider:string, id:string, baseUrl:string, reasoning=false) => buildModel({id,name:id,api:"openai-completions",provider,baseUrl,reasoning,input:["text"],cost:{input:0,output:0,cacheRead:0,cacheWrite:0},contextWindow:null,maxTokens:null} as any);
const canon = (o:any)=>JSON.stringify(Object.fromEntries(Object.entries(o??{}).sort(([a],[b])=>a<b?-1:1)));
const sig = (m:any)=>canon({t:canon(m.thinking),c:canon(m.compat)});
const lcp=(a:string,b:string)=>{a=a.toLowerCase();b=b.toLowerCase();let i=0;while(i<a.length&&i<b.length&&a[i]===b[i])i++;return i;};
for (const p of ["fireworks","nanogpt","moonshot","alibaba-token-plan","huggingface","together","nvidia","groq","cerebras","mistral","deepseek"]) {
  const rows = getBundledModels(p).filter((m:any)=>m.api==="openai-completions");
  const baseUrl = rows[0].baseUrl;
  const generic = sig(build(p,"zz-unknown-model-1",baseUrl,true));
  const reasoningRows = rows.filter((r:any)=>r.reasoning);
  let n=0, sib=[0,0,0], gen=0;
  for (const r of reasoningRows) {
    const target = sig(build(p, r.id, baseUrl, true));
    n++;
    let best:any=null, bl=-1;
    for (const o of reasoningRows) { if (o===r) continue; const l=lcp(o.id,r.id); if(l>bl){bl=l;best=o;} }
    [0.3,0.5,0.7].forEach((t,i)=>{ const use = best && bl/r.id.length>=t; const pred = use ? sig(build(p,best.id,baseUrl,true)) : generic; if (pred===target) sib[i]++; });
    if (generic===target) gen++;
  }
  console.log(p, "reasoning rows", n, "generic ok", gen, "sibling thr .3/.5/.7", sib.join(","));
}
