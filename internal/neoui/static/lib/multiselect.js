// Multi-select for long lists of rows. One set of listeners on the container,
// whatever the number of rows: rows are descendants matching `row` that carry
// a `data-key`, re-rendered by the page at will (call refresh() afterwards).
//
// Pointer: click selects one, Shift+click a range from the anchor, Ctrl/Cmd+
// click toggles one; press and drag sweeps a range (Ctrl/Cmd+drag adds or
// removes it) and scrolls the list when the pointer nears its edge. Touch taps
// toggle, so a phone can still pick several while it scrolls normally.
// Keyboard (roving tabindex: the list is one tab stop): Up/Down/Home/End move
// focus, with Shift they extend the selection; Ctrl/Cmd+A selects the focused
// row's group; Space calls onSpace(keys) for the selection, or for the focused
// row when nothing is selected; Esc clears the selection.

import { h, icon } from "./dom.js";

const EDGE = 56; // px from the scroller's edge where a drag starts scrolling
const MAX_STEP = 22; // px per frame at the very edge

function scrollerOf(el) {
	for (let n = el.parentElement; n; n = n.parentElement) {
		const oy = getComputedStyle(n).overflowY;
		if ((oy === "auto" || oy === "scroll") && n.scrollHeight > n.clientHeight) return n;
	}
	return document.scrollingElement;
}

export function multiSelect(container, { row = "[data-key]", ignore = "[data-act], a, input, select, textarea", groupKeys, onChange, onSpace } = {}) {
	const selected = new Set();
	let painted = new Set();
	let anchor = null;
	let focusKey = null;
	let drag = null;
	let lastPointer = "mouse";
	let rows = [];
	let pos = new Map();
	let byKey = new Map();

	const index = () => {
		rows = [...container.querySelectorAll(row)];
		pos = new Map(rows.map((el, i) => [el, i]));
		byKey = new Map(rows.map((el) => [el.dataset.key, el]));
	};
	const rowOf = (target) => {
		const el = target?.closest?.(row);
		return el && container.contains(el) ? el : null;
	};
	function paint() {
		for (const key of painted) if (!selected.has(key)) { const el = byKey.get(key); el?.classList.remove("is-selected"); el?.setAttribute("aria-selected", "false"); }
		for (const key of selected) if (!painted.has(key)) { const el = byKey.get(key); el?.classList.add("is-selected"); el?.setAttribute("aria-selected", "true"); }
		painted = new Set(selected);
	}
	const changed = () => { paint(); onChange?.(selected); };
	function focusRow(el, { scroll = true } = {}) {
		if (!el) return;
		byKey.get(focusKey)?.setAttribute("tabindex", "-1");
		focusKey = el.dataset.key;
		el.setAttribute("tabindex", "0");
		el.focus({ preventScroll: true });
		if (scroll) el.scrollIntoView({ block: "nearest" });
	}
	// Select the rendered rows between the anchor and `to`, on top of `base`.
	function sweep(base, to, adding = true) {
		const from = pos.get(byKey.get(anchor)) ?? to;
		const next = new Set(base);
		for (let i = Math.min(from, to); i <= Math.max(from, to); i++) {
			const key = rows[i].dataset.key;
			if (adding) next.add(key);
			else next.delete(key);
		}
		selected.clear();
		for (const key of next) selected.add(key);
		changed();
	}

	// ----- pointer -----
	function hit() {
		const r = container.getBoundingClientRect();
		const x = Math.min(Math.max(drag.x, r.left + 4), r.right - 4);
		const el = rowOf(document.elementFromPoint(x, drag.y));
		if (el && pos.has(el)) return pos.get(el);
		// Past the ends of the list: clamp to the first or last rendered row.
		if (rows.length && drag.y < rows[0].getBoundingClientRect().top) return 0;
		if (rows.length && drag.y > rows[rows.length - 1].getBoundingClientRect().bottom) return rows.length - 1;
		return drag.last;
	}
	function track() {
		const i = hit();
		if (i !== drag.last) {
			drag.last = i;
			drag.moved = true;
			sweep(drag.base, i, drag.adding);
		}
	}
	function autoscroll() {
		if (!drag) return;
		const s = drag.scroller;
		const r = s === document.scrollingElement ? { top: 0, bottom: innerHeight } : s.getBoundingClientRect();
		const top = Math.max(r.top, 0), bottom = Math.min(r.bottom, innerHeight);
		let step = 0;
		if (drag.y < top + EDGE) step = -Math.ceil(MAX_STEP * Math.min(1, (top + EDGE - drag.y) / EDGE));
		else if (drag.y > bottom - EDGE) step = Math.ceil(MAX_STEP * Math.min(1, (drag.y - (bottom - EDGE)) / EDGE));
		if (step) {
			s.scrollBy({ top: step, behavior: "instant" });
			track();
		}
		drag.frame = requestAnimationFrame(autoscroll);
	}
	function down(e) {
		lastPointer = e.pointerType;
		if (e.button !== 0 || e.target.closest(ignore)) return;
		const el = rowOf(e.target);
		if (!el) return;
		if (e.pointerType === "touch") return; // taps are handled on click
		e.preventDefault(); // no text selection or native drag while sweeping
		index();
		const key = el.dataset.key;
		const additive = e.ctrlKey || e.metaKey;
		focusRow(el, { scroll: false });
		let base, adding = true;
		if (e.shiftKey && anchor && byKey.has(anchor)) {
			base = additive ? new Set(selected) : new Set();
		} else if (additive) {
			adding = !selected.has(key);
			base = new Set(selected);
			anchor = key;
		} else {
			base = new Set();
			anchor = key;
		}
		drag = { base, adding, x: e.clientX, y: e.clientY, last: pos.get(el), moved: false, scroller: scrollerOf(container), frame: 0, id: e.pointerId };
		sweep(base, pos.get(el), adding);
		container.setPointerCapture(e.pointerId);
		container.classList.add("is-sweeping");
		drag.frame = requestAnimationFrame(autoscroll);
	}
	function move(e) {
		if (!drag || e.pointerId !== drag.id) return;
		drag.x = e.clientX;
		drag.y = e.clientY;
		track();
	}
	function up(e) {
		if (!drag || e.pointerId !== drag.id) return;
		cancelAnimationFrame(drag.frame);
		if (drag.moved) focusRow(rows[drag.last], { scroll: false });
		drag = null;
		container.classList.remove("is-sweeping");
		if (container.hasPointerCapture(e.pointerId)) container.releasePointerCapture(e.pointerId);
	}
	function tap(e) {
		if (lastPointer !== "touch" || e.target.closest(ignore)) return;
		const el = rowOf(e.target);
		if (!el) return;
		index();
		const key = el.dataset.key;
		if (selected.has(key)) selected.delete(key);
		else selected.add(key);
		anchor = key;
		focusRow(el, { scroll: false });
		changed();
	}

	// ----- keyboard -----
	function key(e) {
		const el = rowOf(e.target);
		if (!el || e.target !== el) return;
		index();
		const i = pos.get(el);
		const mod = e.ctrlKey || e.metaKey;
		const go = (j) => {
			e.preventDefault();
			const next = rows[Math.max(0, Math.min(rows.length - 1, j))];
			if (!next) return;
			if (e.shiftKey) {
				if (!anchor || !byKey.has(anchor)) anchor = el.dataset.key;
				sweep(new Set(), pos.get(next));
			} else if (!selected.size) anchor = next.dataset.key;
			focusRow(next);
		};
		switch (e.key) {
			case "ArrowDown": return go(i + 1);
			case "ArrowUp": return go(i - 1);
			case "Home": return go(0);
			case "End": return go(rows.length - 1);
			case "PageDown": return go(i + 10);
			case "PageUp": return go(i - 10);
			case " ":
				e.preventDefault();
				if (!e.repeat) onSpace?.(selected.size ? [...selected] : [el.dataset.key]);
				return;
			case "Escape":
				if (selected.size) { e.preventDefault(); e.stopPropagation(); clear(); }
				return;
			default:
				if (mod && e.key.toLowerCase() === "a") {
					e.preventDefault();
					const keys = groupKeys ? groupKeys(el) : rows.map((r) => r.dataset.key);
					selected.clear();
					for (const k of keys) selected.add(k);
					anchor = el.dataset.key;
					changed();
				}
		}
	}

	function clear() {
		if (!selected.size) return;
		selected.clear();
		changed();
	}
	// Whether keyboard focus was last on one of the rows. A re-render removes the
	// focused row without a focusout that names a new target, so only a move to
	// something else clears it.
	let rowFocused = false;
	const focusin = (e) => { rowFocused = !!rowOf(e.target) && e.target.matches(row); };
	const focusout = (e) => { if (e.relatedTarget && !(rowOf(e.relatedTarget) && e.relatedTarget.matches(row))) rowFocused = false; };
	const listeners = [["pointerdown", down], ["pointermove", move], ["pointerup", up], ["pointercancel", up], ["click", tap], ["keydown", key], ["focusin", focusin], ["focusout", focusout]];
	for (const [type, fn] of listeners) container.addEventListener(type, fn);

	return {
		selected,
		clear,
		has: (k) => selected.has(k),
		// Replace the selection (e.g. keep only keys that still match a filter).
		set(keys) {
			selected.clear();
			for (const k of keys) selected.add(k);
			changed();
		},
		// After the page re-renders rows: repaint selection, restore the tab stop
		// and, when focus was on a row that still exists, focus it again.
		refresh() {
			const active = document.activeElement;
			const hadFocus = rowFocused && (!active || active === document.body || !active.isConnected || (container.contains(active) && active.matches(row)));
			index();
			painted = new Set();
			for (const el of rows) el.setAttribute("tabindex", "-1");
			paint();
			const target = byKey.get(focusKey) ?? rows[0];
			target?.setAttribute("tabindex", "0");
			if (hadFocus && target?.dataset.key === focusKey && document.activeElement !== target) target.focus({ preventScroll: true });
		},
		// Return keyboard focus to the list (after an action-bar button).
		focus() {
			index();
			const el = byKey.get(focusKey) ?? rows[0];
			if (el) focusRow(el, { scroll: false });
		},
		destroy() {
			for (const [type, fn] of listeners) container.removeEventListener(type, fn);
		},
	};
}

// The bar that floats at the bottom of the page while rows are selected.
// `actions`: [{ label, icon, onClick(event), cls, title }]. update(n) shows or
// hides it; `onClear` runs from its Clear button.
export function selectionBar({ actions, onClear, noun = "selected" }) {
	const count = h("span", { class: "selbar-count", role: "status", "aria-live": "polite" });
	const bar = h("div", { class: "selbar", role: "toolbar", "aria-label": "Selected rows", hidden: true },
		count,
		h("span", { class: "selbar-sep", "aria-hidden": "true" }),
		actions.map((a) => h("button", { type: "button", class: `selbar-btn ${a.cls ?? ""}`, title: a.title, onClick: a.onClick }, a.icon ? icon(a.icon, 14) : null, h("span", null, a.label))),
		h("span", { class: "selbar-sep", "aria-hidden": "true" }),
		h("button", { type: "button", class: "selbar-btn ghost", title: "Clear the selection (Esc)", onClick: onClear }, icon("x", 14), h("span", null, "Clear")),
	);
	return {
		el: bar,
		update(n) {
			bar.hidden = !n;
			count.textContent = `${n.toLocaleString()} ${noun}`;
		},
	};
}
