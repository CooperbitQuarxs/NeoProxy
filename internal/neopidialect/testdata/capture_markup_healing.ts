// Captures NeoPi's own StreamMarkupHealing for the tool-call patterns (kimi,
// dsml, qwen) over leaked-markup streams at several chunkings. Tool-call ids
// are random in NeoPi, so only names and arguments are recorded. Run from the
// NeoPi env directory:
//   HOME=$PWD/home NEOPI_SOURCE_MODULES=$PWD/node_modules/@oh-my-pi \
//     bun --preserve-symlinks /path/to/internal/neopidialect/testdata/capture_markup_healing.ts \
//     > /path/to/internal/neopidialect/testdata/markup_healing.json
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { StreamMarkupHealing } = await import(root + "/pi-ai/src/utils/stream-markup-healing.ts");

type Event = { kind: "text" | "thinking" | "toolCall"; text?: string; name?: string; arguments?: string };
type HealingCase = { name: string; pattern: string; chunks: string[]; feeds: Event[][]; flush: Event[]; closed: boolean };

const kimi = (id: string, args: string) => `<|tool_call_begin|>${id}<|tool_call_argument_begin|>${args}<|tool_call_end|>`;
const kimiSection = (...calls: string[]) => `<|tool_calls_section_begin|>${calls.join("")}<|tool_calls_section_end|>`;
const dsCall = (name: string, args: string) => `<｜tool▁call▁begin｜>${name}<｜tool▁sep｜>${args}<｜tool▁call▁end｜>`;
const dsSection = (...calls: string[]) => `<｜tool▁calls▁begin｜>${calls.join("")}<｜tool▁calls▁end｜>`;
const dsml = (bar: string, body: string) =>
	`<${bar}DSML${bar}tool_calls>\n${body}</${bar}DSML${bar}tool_calls>`;
const dsmlInvoke = (bar: string, name: string, params: Array<[string, string, string]>) =>
	`<${bar}DSML${bar}invoke name="${name}">\n${params
		.map(([key, isString, value]) => `<${bar}DSML${bar}parameter name="${key}" string="${isString}">${value}</${bar}DSML${bar}parameter>\n`)
		.join("")}</${bar}DSML${bar}invoke>\n`;

const inputs: Array<[string, string, string]> = [
	["kimi", "kimi-one", `before${kimiSection(kimi("functions.read:0", '{"path":"a.txt"}'))}after`],
	["kimi", "kimi-two", kimiSection(kimi("functions.read:0", '{"path":"a"}'), kimi("functions.bash:1", '{"cmd":"ls -la"}'))],
	["kimi", "kimi-think", `<think>plan it</think>${kimiSection(kimi("functions.read:0", "{}"))}done`],
	["kimi", "kimi-repair", kimiSection(kimi("functions.write:0", '{"path": "a", "text": "x",}'))],
	["kimi", "kimi-empty-args", kimiSection(kimi("functions.list:0", ""))],
	["kimi", "kimi-unterminated", `x${"<|tool_calls_section_begin|>"}${"<|tool_call_begin|>"}functions.read:0<|tool_call_argument_begin|>{"path":`],
	["kimi", "kimi-stray-token", "visible<|tool_call_end|> text"],
	["kimi", "kimi-leaked-thinking", `${kimiSection(kimi("functions.a:0", '{"k":1}'))}<thinking>later thought</thinking>end`],
	["kimi", "kimi-unicode", `café ${kimiSection(kimi("functions.say:0", '{"t":"🧠 é"}'))} 🧠`],
	["kimi", "kimi-section-junk", "<|tool_calls_section_begin|> junk <|tool_call_begin|>functions.x:0<|tool_call_argument_begin|>{}<|tool_call_end|> <|tool_calls_section_end|>z"],
	["dsml", "ds-canonical", `pre${dsSection(dsCall("read", '{"path":"a"}'))}post`],
	["dsml", "ds-legacy", `${dsSection(`<｜tool▁call▁begin｜>function<｜tool▁sep｜>read\n\`\`\`json\n{"path":"a"}\n\`\`\`<｜tool▁call▁end｜>`)}after`],
	["dsml", "ds-respelled", "<｜tool_calls_begin｜><tool_call_begin｜>read<｜tool_sep｜>{\"a\":1}<｜tool_call_end｜><｜tool_calls_end｜>tail"],
	["dsml", "ds-two", dsSection(dsCall("a", "{}"), dsCall("b", '{"x":[1,2]}'))],
	["dsml", "ds-token-in-json", dsSection(`<｜tool▁call▁begin｜>grep<｜tool▁sep｜>{"q":"<｜tool_call_end｜>"}<｜tool▁call▁end｜>`)],
	["dsml", "ds-missing-close", "<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>a<｜tool▁sep｜>{\"k\":1}<｜tool▁call▁begin｜>b<｜tool▁sep｜>{}<｜tool▁call▁end｜><｜tool▁calls▁end｜>"],
	["dsml", "ds-control-tokens", "<｜Assistant｜>  hello<｜end▁of▁sentence｜>"],
	["dsml", "ds-think", "<think>reason</think>answer"],
	["dsml", "ds-quoted", "use `<｜tool_calls_begin｜>` in prose and ```\n<｜tool_call_begin｜>\n``` too"],
	["dsml", "dsml-full", `text${dsml("｜", dsmlInvoke("｜", "read", [["path", "true", "a.txt"], ["n", "false", "5"]]))}tail`],
	["dsml", "dsml-ascii", dsml("|", dsmlInvoke("|", "run", [["cmd", "true", "ls < x > y"], ["opts", "false", '{"a":[1,2],}']]))],
	["dsml", "dsml-two-invokes", dsml("｜", dsmlInvoke("｜", "a", [["k", "true", "v"]]) + dsmlInvoke("｜", "b", []))],
	["dsml", "dsml-no-invoke", "x<｜DSML｜tool_calls>\nnot a call\n</｜DSML｜tool_calls>y"],
	["dsml", "dsml-orphan-close", "visible</｜DSML｜invoke> and </|DSML|parameter> more"],
	["dsml", "dsml-bare-invoke", "<｜DSML｜invoke name=\"x\">body</｜DSML｜invoke></｜DSML｜tool_calls> after"],
	["dsml", "dsml-unterminated", `a<｜DSML｜tool_calls>\n<｜DSML｜invoke name="r">\n<｜DSML｜parameter name="p" string="true">partial`],
	["dsml", "ds-unterminated-args", "<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>a<｜tool▁sep｜>{\"k\":"],
	["dsml", "ds-leaked-thinking", `${dsSection(dsCall("a", "{}"))}<thinking>t</thinking>z`],
	["qwen", "qwen-one", "pre<tool_calls><read path=\"a.txt\" n='3'/><write path=\"b\"/></tool_calls>post"],
	["qwen", "qwen-unterminated", "x<tool_calls><read path=\"a\"/>"],
	["qwen", "qwen-partial-open", "visible<tool_ca"],
	["qwen", "qwen-not-element", "<tool_calls><bad attr=/><ok/></tool_calls>"],
	["qwen", "qwen-think", "<think>t</think>a<tool_calls><x y=\"1\"/></tool_calls>"],
];

function fixedChunks(text: string, widths: number[]): string[] {
	const chars = Array.from(text);
	const chunks: string[] = [];
	let offset = 0;
	let i = 0;
	while (offset < chars.length) {
		const end = Math.min(chars.length, offset + widths[i % widths.length]!);
		chunks.push(chars.slice(offset, end).join(""));
		offset = end;
		i++;
	}
	return chunks;
}

function convert(event: any): Event {
	if (event.type === "text") return { kind: "text", text: event.text };
	if (event.type === "thinking") return { kind: "thinking", text: event.thinking };
	return { kind: "toolCall", name: event.call.name, arguments: event.call.arguments };
}

const cases: HealingCase[] = [];
function capture(pattern: string, name: string, chunks: string[]): void {
	const healer = new StreamMarkupHealing({ pattern });
	const feeds = chunks.map(chunk => healer.feedEvents(chunk).map(convert));
	const flush = healer.flushEvents().map(convert);
	cases.push({ name, pattern, chunks, feeds, flush, closed: healer.sectionClosed });
}

for (const [pattern, name, text] of inputs) {
	capture(pattern, `${name}:whole`, [text]);
	capture(pattern, `${name}:unit-chunks`, Array.from(text));
	capture(pattern, `${name}:fixed-1-2-3`, fixedChunks(text, [1, 2, 3]));
	capture(pattern, `${name}:fixed-5-7`, fixedChunks(text, [5, 7]));
	capture(pattern, `${name}:fixed-13`, fixedChunks(text, [13]));
}
console.log(JSON.stringify({ source: "NeoPi OWN StreamMarkupHealing(kimi|dsml|qwen)", caseCount: cases.length, cases }, null, 1));
