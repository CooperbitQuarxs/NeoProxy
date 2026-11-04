// Virtual models: names the proxy serves that are not a provider's model. A virtual
// model is a model plus properties that are always set (Fast mode, a context
// window, an effort limit, …), and optionally a chain of fallbacks. The pieces live
// in ./vm/: the catalogue, the editor, the stage card, the account settings, the
// Lua policy, the simulator and the access list.

import { catalogue } from "./vm/list.js";
import { editor } from "./vm/editor.js";

export async function render(ctx) {
	if (ctx.params[0]) return editor(ctx, ctx.params[0]);
	return catalogue(ctx);
}
