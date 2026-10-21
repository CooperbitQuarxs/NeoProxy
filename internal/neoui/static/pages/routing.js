import { h } from "../lib/dom.js";
import { pageHead, empty } from "../lib/components.js";

export async function render({ root }) {
	root.append(pageHead({ title: "routing" }), empty({ title: "Not built yet", hint: "This page is being designed." }));
}
