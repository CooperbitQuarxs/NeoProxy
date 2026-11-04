// Exports the NeoPi collapse data Devin discovery reads, as
// internal/registry/models/devin_collapse.json: the reviewed devin variant
// table (reviewedCollapseTable("devin")) and the taxonomy vocabulary
// deriveThinkingPairFamilies pairs `X` / `X-thinking` siblings with.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { reviewedCollapseTable } = await import(root + "/pi-catalog/src/compat/collapse.ts");
const rules = (await import(root + "/pi-catalog/src/compat/rules.json")).default;

const table = reviewedCollapseTable("devin");
if (!table) throw new Error("no reviewed devin collapse table");
for (const family of table.families) {
	for (const key of ["retiredMembers", "extraAliases", "suppressWhenOff", "preserveAbsentEffortRoutes"]) {
		if ((family as any)[key] !== undefined) throw new Error(`devin family ${family.id} uses ${key}, which the Go port does not implement`);
	}
	if (family.thinking?.effortBudgets !== undefined) throw new Error(`devin family ${family.id} carries effortBudgets`);
}
if (table.templates?.length) throw new Error("devin collapse table has revision templates");

const { collapse } = rules.taxonomy;
console.log(
	JSON.stringify(
		{
			source: "NeoPi OWN reviewedCollapseTable(devin) + rules.taxonomy.collapse",
			families: table.families,
			pairTokens: collapse.pairTokens,
			suffixes: collapse.suffixes.map((rule: any) => ({
				suffix: rule.suffix,
				...(rule.thinking === true ? { thinking: true } : {}),
				...(rule.exceptBarePrefix !== undefined ? { exceptBarePrefix: rule.exceptBarePrefix } : {}),
			})),
			effortAliases: collapse.effortFamilies
				.filter((family: any) => family.provider === "devin")
				.flatMap((family: any) => family.aliases),
		},
		null,
		2,
	),
);
