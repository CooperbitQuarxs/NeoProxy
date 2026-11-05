// The NeoPi model fields the Go Cursor catalog pipeline reads
// (neopicatalog.Spec), with JS key presence kept: absent keys and undefined
// values are omitted alike. Shared by the cursor catalog export and the
// discovery capture.
export const specRecord = (m: any) => {
	if (m.compatConfig !== undefined) throw new Error(`${m.id} carries compat config`);
	if (m.kind !== undefined && m.kind !== "chat") throw new Error(`${m.id} is a ${m.kind} row`);
	const cost: any = { input: m.cost.input, output: m.cost.output, cacheRead: m.cost.cacheRead, cacheWrite: m.cost.cacheWrite };
	if (m.cost.longContext !== undefined) cost.longContext = m.cost.longContext;
	if (m.cost.timeBased !== undefined) {
		const { offPeakMultiplier, peakWindows, effectiveRates, ...rest } = m.cost.timeBased;
		if (peakWindows?.length || Object.keys(rest).length > 0) throw new Error(`${m.id} has a recurring tariff`);
		cost.timeBased = { offPeakMultiplier, effectiveRates };
	}
	const record: any = {
		id: m.id,
		name: m.name,
		api: m.api,
		provider: m.provider,
		baseUrl: m.baseUrl,
		reasoning: m.reasoning,
		input: m.input,
		cost,
		contextWindow: m.contextWindow,
		maxTokens: m.maxTokens,
	};
	for (const key of [
		"requestModelId",
		"thinking",
		"isProviderDefault",
		"cursorMaxMode",
		"cursorMaxModeRoutes",
		"cursorModelParameters",
		"cursorModelRoutes",
		"requiresCursorToolSchemaProjection",
	]) {
		if (m[key] !== undefined) record[key] = m[key];
	}
	return record;
};
