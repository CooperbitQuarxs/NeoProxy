// How a stage picks an account, shown as tiles: a request on the left, four
// targets on the right, and the pick outlined. Each tile is one small SVG
// drawn from a description of which targets light up, so a new mode only
// needs a line in the table.

import { h, svgEl } from "./dom.js";

// Illustration per mode: which target indices are picked (outlined), which are
// dimmed, optional labels at each target, and a short caption.
const ART = {
	priority: { picked: [0], labels: ["1st", "2nd", "3rd", "4th"], caption: "the first, while it has room" },
	fill: { picked: [0], dashed: [1], labels: ["", "", "", ""], caption: "fill the first, then the next" },
	"round-robin": { picked: [2], cycle: true, caption: "take turns, 1 → 2 → 3 → 4" },
	alternate: { picked: [2], cycle: true, caption: "take turns, 1 → 2 → 3 → 4" },
	random: { picked: [1], caption: "any one, never the same twice in a row" },
	"strict-random": { picked: [1], labels: ["", "", "", "×2"], caption: "any one, repeats allowed" },
	weighted: { picked: [0], labels: ["60%", "30%", "10%", ""], caption: "a weighted coin per account" },
	"quota-weighted": { picked: [1], bars: [0.2, 0.8, 0.5, 0.3], caption: "a coin weighted by room left" },
	"least-used": { picked: [1], bars: [0.75, 0.2, 0.55, 0.4], caption: "lowest usage right now" },
	headroom: { picked: [1], bars: [0.9, 0.2, 0.6, 0.4], caption: "most room left" },
	p2c: { picked: [2], dashed: [1], labels: ["", "20%", "65%", ""], caption: "pick two, take the lighter" },
	"reset-soonest": { picked: [1], labels: ["58m", "2m", "31m", "12h"], caption: "resets soonest, use it now" },
	"reset-latest": { picked: [3], labels: ["58m", "2m", "31m", "12h"], caption: "resets latest, keep the rest" },
	sticky: { picked: [2], labels: ["", "", "✓", ""], caption: "the one that last worked" },
	"cache-affinity": { picked: [1], loop: true, caption: "same prompt, same account" },
	cheapest: { picked: [3], labels: ["$9", "$3", "$0.5", "free"], caption: "cheapest plan first" },
	"fit-context": { picked: [1], labels: ["8K", "200K", "32K", "128K"], caption: "a model the request fits in" },
	auto: { picked: [1], labels: ["72", "91", "84", "55"], caption: "a live score of room, success, speed" },
};

// OmniRoute's name(s) for a mode, minus any that equal our own id.
function aliasText(m) {
	return String(m.omniroute ?? "").split(",").map((x) => x.trim()).filter((x) => x && x !== m.id).join(", ");
}

function tileArt(id) {
	const a = ART[id] ?? { picked: [0], caption: "" };
	const W = 150, H = 76;
	const svg = svgEl("svg", { viewBox: `0 0 ${W} ${H}`, class: "draw-art", "aria-hidden": "true" });
	const ys = [12, 30, 48, 66];
	// request dot
	svg.append(svgEl("circle", { cx: 12, cy: 39, r: 3.5, class: "req" }));
	ys.forEach((y, i) => {
		const picked = a.picked.includes(i);
		const dashed = a.dashed?.includes(i);
		const path = svgEl("path", { d: `M16 39 C 50 39, 60 ${y}, 92 ${y}`, class: `lane ${picked ? "pick" : ""} ${dashed ? "dash" : ""}` });
		svg.append(path);
		svg.append(svgEl("rect", { x: 94, y: y - 5, width: 26, height: 10, rx: 3, class: `tgt ${picked ? "pick" : ""}` }));
		if (a.bars) svg.append(svgEl("rect", { x: 96, y: y - 3, width: 22 * a.bars[i], height: 6, rx: 2, class: "bar" }));
		if (a.labels?.[i]) { const t = svgEl("text", { x: 124, y: y + 3.5, class: "lbl" }); t.textContent = a.labels[i]; svg.append(t); }
	});
	if (a.cycle) svg.append(svgEl("path", { d: "M122 8 q 14 31 0 62", class: "lane cycle" }));
	if (a.loop) svg.append(svgEl("path", { d: "M120 30 q 16 0 16 9 q 0 9 -16 9", class: "lane cycle" }));
	return svg;
}

// `modes`: the server's catalogue [{id,label,short,doc,omniroute,needs}];
// `value`: current id; `available`: ids the stage can use (others are shown
// with the reason they cannot).
export function drawPicker({ modes, value, onPick, unavailable = {} }) {
	const grid = h("div", { class: "draw-grid", role: "radiogroup", "aria-label": "How to draw" });
	const paint = () => {
		grid.replaceChildren(...modes.map((m) => {
			const why = unavailable[m.id];
			const on = m.id === value;
			const tile = h("button", { type: "button", role: "radio", "aria-checked": String(on), class: `draw-tile ${on ? "on" : ""} ${why ? "off" : ""}`, title: why ? `${m.doc}\n\nNot usable here: ${why}` : m.doc, disabled: !!why, onClick: () => { value = m.id; onPick(m.id); paint(); } },
				h("div", { class: "draw-name" }, h("code", null, m.id), aliasText(m) ? h("span", { class: "draw-alias" }, aliasText(m)) : null),
				tileArt(m.id),
				h("div", { class: "draw-caption" }, ART[m.id]?.caption ?? m.short),
				why ? h("div", { class: "draw-why" }, why) : null,
			);
			return tile;
		}));
	};
	paint();
	return grid;
}
