// Direct model access, as the backend decides it (internal/neo AllowsDirect):
// a provider in providerAccess serves every exposed model unless modelAccess
// has an entry for it; then only the models matching one of its patterns. A
// pattern is an id or a prefix ending in "*". Key `models` narrow further and
// match full "<provider>/<model>" ids.

export function matchModel(patterns, id) {
	for (const p of patterns ?? []) if (p.endsWith("*") ? id.startsWith(p.slice(0, -1)) : p === id) return true;
	return false;
}

// The id modelAccess patterns are matched against: the model id without its
// "<provider>/" prefix.
export const modelName = (m) => (m.id.startsWith(`${m.providerId}/`) ? m.id.slice(m.providerId.length + 1) : m.id);

export function allowsDirect(user, m, keyModels) {
	if (m.virtualOnly || !user.providerAccess?.includes(m.providerId)) return false;
	const patterns = user.modelAccess?.[m.providerId];
	if (patterns && !matchModel(patterns, modelName(m))) return false;
	return !keyModels?.length || matchModel(keyModels, m.id);
}

// The exposed provider models a person may call directly.
export const directModelsFor = (user, models, providerId) => models.filter((m) => m.exposed && (!providerId || m.providerId === providerId) && allowsDirect(user, m));
