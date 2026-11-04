// The switch fabric: the sign-in hero. People on the left, virtual models in
// the middle, providers on the right, joined by ribbons; small packets travel
// left to right, fan out at the middle column and land on a provider. It is
// the Flow board, abstracted and alive. Pure canvas, no dependencies; static
// when the visitor prefers reduced motion.

const PEOPLE = 5;
const MODELS = 4;
const PROVIDERS = 7;

function rng(seed) {
	let s = seed >>> 0;
	return () => ((s = (s * 1664525 + 1013904223) >>> 0) / 2 ** 32);
}

export function fabric(canvas, { seed = 7 } = {}) {
	const ctx = canvas.getContext("2d");
	const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
	const rand = rng(seed);
	let w = 0, h = 0, dpr = 1, raf = 0, t0 = performance.now(), disposed = false;
	const css = (name) => getComputedStyle(document.documentElement).getPropertyValue(name).trim();
	let palette = {};
	function readPalette() {
		palette = { cyan: css("--accent-a") || "#5ad8e6", magenta: css("--accent-b") || "#ed4abf", violet: css("--violet") || "#945ff9", ink: css("--ink-3") || "#6e6974", line: css("--line-3") || "rgba(255,255,255,.14)" };
	}

	// Nodes in unit space; resized into pixels on each layout.
	const people = Array.from({ length: PEOPLE }, (_, i) => ({ u: 0.08, v: 0.1 + (i / (PEOPLE - 1)) * 0.42 }));
	const models = Array.from({ length: MODELS }, (_, i) => ({ u: 0.5, v: 0.14 + (i / (MODELS - 1)) * 0.5 }));
	const providers = Array.from({ length: PROVIDERS }, (_, i) => ({ u: 0.92, v: 0.08 + (i / (PROVIDERS - 1)) * 0.8 }));
	// Edges: each person draws from 1–3 models; each model drains to 1–3 providers.
	const pm = [], mp = [];
	people.forEach((_, p) => { const n = 1 + Math.floor(rand() * 3); const picked = new Set(); while (picked.size < n) picked.add(Math.floor(rand() * MODELS)); for (const m of picked) pm.push({ a: p, b: m, w: 0.6 + rand() * 1.6 }); });
	models.forEach((_, m) => { const n = 1 + Math.floor(rand() * 3); const picked = new Set(); while (picked.size < n) picked.add(Math.floor(rand() * PROVIDERS)); for (const p of picked) mp.push({ a: m, b: p, w: 0.6 + rand() * 1.6 }); });

	const packets = [];
	function spawn(now) {
		const e1 = pm[Math.floor(rand() * pm.length)];
		const outs = mp.filter((e) => e.a === e1.b);
		const e2 = outs[Math.floor(rand() * outs.length)];
		packets.push({ e1, e2, born: now, dur: 2600 + rand() * 1800, hue: rand() < 0.5 ? "magenta" : rand() < 0.5 ? "violet" : "cyan" });
	}

	function layout() {
		const rect = canvas.getBoundingClientRect();
		dpr = Math.min(2, devicePixelRatio || 1);
		w = Math.max(1, Math.round(rect.width));
		h = Math.max(1, Math.round(rect.height));
		canvas.width = w * dpr;
		canvas.height = h * dpr;
		ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		readPalette();
	}
	const px = (n) => [n.u * w, n.v * h];
	function curve(a, b) {
		const [x1, y1] = px(a), [x2, y2] = px(b);
		const mx = (x1 + x2) / 2;
		return { x1, y1, c1x: mx, c1y: y1, c2x: mx, c2y: y2, x2, y2 };
	}
	function point(c, t) {
		const it = 1 - t;
		return [
			it * it * it * c.x1 + 3 * it * it * t * c.c1x + 3 * it * t * t * c.c2x + t * t * t * c.x2,
			it * it * it * c.y1 + 3 * it * it * t * c.c1y + 3 * it * t * t * c.c2y + t * t * t * c.y2,
		];
	}
	function ribbon(c, width, color, alpha) {
		ctx.beginPath();
		ctx.moveTo(c.x1, c.y1);
		ctx.bezierCurveTo(c.c1x, c.c1y, c.c2x, c.c2y, c.x2, c.y2);
		ctx.lineWidth = width;
		ctx.strokeStyle = color;
		ctx.globalAlpha = alpha;
		ctx.lineCap = "round";
		ctx.stroke();
		ctx.globalAlpha = 1;
	}
	function dot(n, r, color, alpha = 1) {
		const [x, y] = px(n);
		ctx.beginPath();
		ctx.arc(x, y, r, 0, Math.PI * 2);
		ctx.fillStyle = color;
		ctx.globalAlpha = alpha;
		ctx.fill();
		ctx.globalAlpha = 1;
	}

	let lastSpawn = 0;
	function frame(now) {
		if (disposed) return;
		const t = (now - t0) / 1000;
		ctx.clearRect(0, 0, w, h);
		// Resting ribbons.
		for (const e of pm) ribbon(curve(people[e.a], models[e.b]), e.w, palette.cyan, 0.16);
		for (const e of mp) ribbon(curve(models[e.a], providers[e.b]), e.w, palette.magenta, 0.16);
		// Packets.
		if (!reduce) {
			if (now - lastSpawn > 520 && packets.length < 14) { spawn(now); lastSpawn = now; }
			for (let i = packets.length - 1; i >= 0; i--) {
				const p = packets[i];
				const k = (now - p.born) / p.dur;
				if (k >= 1) { packets.splice(i, 1); continue; }
				const color = palette[p.hue];
				const seg = k < 0.5 ? curve(people[p.e1.a], models[p.e1.b]) : curve(models[p.e2.a], providers[p.e2.b]);
				const kk = k < 0.5 ? k * 2 : (k - 0.5) * 2;
				// Lit stretch of ribbon behind the packet.
				ctx.save();
				ctx.beginPath();
				const steps = 18;
				for (let s = 0; s <= steps; s++) {
					const [x, y] = point(seg, Math.max(0, kk - 0.18) + (0.18 * s) / steps);
					s ? ctx.lineTo(x, y) : ctx.moveTo(x, y);
				}
				ctx.lineWidth = (k < 0.5 ? p.e1.w : p.e2.w) + 0.6;
				ctx.strokeStyle = color;
				ctx.globalAlpha = 0.55;
				ctx.lineCap = "round";
				ctx.stroke();
				ctx.restore();
				const [x, y] = point(seg, kk);
				ctx.beginPath();
				ctx.arc(x, y, 2.4, 0, Math.PI * 2);
				ctx.fillStyle = color;
				ctx.shadowColor = color;
				ctx.shadowBlur = 10;
				ctx.fill();
				ctx.shadowBlur = 0;
			}
		}
		// Nodes: people cyan, models magenta squares, providers hollow rings.
		for (const n of people) dot(n, 3.2, palette.cyan, 0.9);
		for (const n of models) {
			const [x, y] = px(n);
			ctx.fillStyle = palette.magenta;
			ctx.globalAlpha = 0.95;
			ctx.beginPath();
			ctx.roundRect(x - 4, y - 4, 8, 8, 2);
			ctx.fill();
			ctx.globalAlpha = 1;
		}
		providers.forEach((n, i) => {
			const [x, y] = px(n);
			const r = 5.5;
			ctx.beginPath();
			ctx.arc(x, y, r, 0, Math.PI * 2);
			ctx.lineWidth = 1.5;
			ctx.strokeStyle = palette.line;
			ctx.stroke();
			// A slow-breathing usage arc so the rings read as meters.
			const used = 0.25 + 0.5 * (0.5 + 0.5 * Math.sin(t * 0.25 + i * 1.3));
			ctx.beginPath();
			ctx.arc(x, y, r, -Math.PI / 2, -Math.PI / 2 + used * Math.PI * 2);
			ctx.strokeStyle = used > 0.7 ? palette.magenta : palette.cyan;
			ctx.lineWidth = 1.8;
			ctx.stroke();
		});
		if (!reduce) raf = requestAnimationFrame(frame);
	}

	layout();
	const ro = new ResizeObserver(() => { layout(); if (reduce) frame(performance.now()); });
	ro.observe(canvas);
	const themeObs = new MutationObserver(() => readPalette());
	themeObs.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
	raf = requestAnimationFrame(frame);
	if (reduce) frame(performance.now());
	return () => { disposed = true; cancelAnimationFrame(raf); ro.disconnect(); themeObs.disconnect(); };
}
