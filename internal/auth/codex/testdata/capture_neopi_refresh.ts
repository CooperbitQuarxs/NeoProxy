// Runs NeoPi's OWN refresh lifecycle against synthetic credential stores only.
// No provider request or real credential store is reachable from these scenarios.
const root = process.env.NEOPI_SOURCE_MODULES;
if (!root) throw new Error("NEOPI_SOURCE_MODULES is required");
const { OAuthRefresher } = await import(root + "/pi-ai/src/auth/refresh.ts");
const { serializeCredential } = await import(root + "/pi-ai/src/auth/sqlite-credential-store.ts");
const { isDefinitiveOAuthFailure } = await import(root + "/pi-ai/src/error/auth-classify.ts");
const provider = "openai-codex";
const now = 1800000000000;
const nativeNow = Date.now;
Date.now = () => now;
const original = { type: "oauth", access: "original-access-fake", refresh: "original-refresh-fake", expires: now - 1, accountId: "account-fake", email: "capture@example.invalid" };
const minted = { access: "minted-access-fake", refresh: "minted-refresh-fake", expires: now + 3600000 };
const peer = { ...original, access: "peer-access-fake", refresh: "peer-refresh-fake", expires: now + 7200000 };
const jwt = (expires: number) => "eyJhbGciOiJub25lIn0." + Buffer.from(JSON.stringify({ exp: expires / 1000 })).toString("base64url") + ".fake";
function fixture(spec: any) {
 let rows: any[] = spec.missing ? [] : [{ id: 1, provider, credential: structuredClone(spec.current ?? original) }];
 let cached = structuredClone(rows);
 let refreshCalls = 0;
 let disabled = false;
 const store: any = {
  listAuthCredentials: () => structuredClone(rows),
  tryUpdateAuthCredentialIfMatches(id: number, expected: string, credential: any) {
   const row = rows.find(row => row.id === id);
   if (!row || serializeCredential(provider, row.credential).data !== expected) return false;
   row.credential = structuredClone(credential);
   return true;
  },
  tryDisableAuthCredentialIfMatches(id: number, expected: string) {
   const row = rows.find(row => row.id === id);
   if (!row || serializeCredential(provider, row.credential).data !== expected) return false;
   rows = rows.filter(row => row.id !== id);
   disabled = true;
   return true;
  },
  tryAcquireCredentialRefreshLease() {
   if (spec.afterLease) rows[0].credential = structuredClone(spec.afterLease);
   return true;
  },
  getCredentialRefreshLeaseExpiresAt: () => undefined,
  releaseCredentialRefreshLease() {},
  renewCredentialRefreshLease: () => true,
 };
 const pool: any = {
  replace(_provider: string, entries: any[]) { cached = structuredClone(entries); },
  entries: () => cached,
  providers: () => [provider],
  async reload() { cached = structuredClone(rows); },
  reset() {}, emitDisabled() {}, bump() {},
  disableIfMatches(_provider: string, id: number, attempted: any) {
   return store.tryDisableAuthCredentialIfMatches(id, serializeCredential(provider, attempted).data);
  },
  replaceById(_provider: string, id: number, credential: any) {
   const index = rows.findIndex(row => row.id === id);
   if (index === -1) return -1;
   rows[index].credential = structuredClone(credential);
   cached = structuredClone(rows);
   return index;
  },
 };
 const refresh = async () => {
  refreshCalls++;
  if (spec.duringRefresh === "remove") rows = [];
  else if (spec.duringRefresh) rows[0].credential = structuredClone(spec.duringRefresh);
  if (spec.error) throw new Error(spec.error);
  return structuredClone(spec.minted ?? minted);
 };
 const refresher = new OAuthRefresher({ store, pool, override: refresh, overrideMints: true });
 return { store, pool, refresher, refresh, snapshot: () => ({ refreshCalls, disabled, stored: rows[0]?.credential ?? null }) };
}
const scenarios: any[] = [];
try {
 for (const spec of [
  { name: "fresh-outside-skew", current: { ...original, expires: now + 60001 } },
  { name: "exact-sixty-second-boundary", current: { ...original, expires: now + 60000 } },
  { name: "inside-refresh-skew", current: { ...original, expires: now + 59999 } },
  { name: "expired" },
  { name: "stored-fresh-beats-expired-jwt", current: { ...original, access: jwt(now - 1000), expires: now + 3600000 } },
  { name: "stored-expired-beats-fresh-jwt", current: { ...original, access: jwt(now + 3600000) } },
  { name: "missing-row", missing: true },
  { name: "force-fresh", current: { ...original, expires: now + 3600000 }, force: true },
  { name: "adopt-peer", current: peer, observed: original },
  { name: "adopt-peer-even-forced", current: peer, observed: original, force: true },
  { name: "refresh-stale-peer", current: { ...peer, expires: now + 45000 }, observed: original },
  { name: "adopt-peer-after-lease", observed: original, afterLease: peer, force: true },
  { name: "cas-lost-to-peer", duringRefresh: peer },
  { name: "cas-lost-to-expiry-edit", duringRefresh: { ...original, expires: now + 7200000 } },
  { name: "cas-lost-to-email-edit", force: true, current: { ...original, expires: now + 3600000 }, duringRefresh: { ...original, expires: now + 3600000, email: "changed@example.invalid" } },
  { name: "cas-lost-to-removal", duringRefresh: "remove" },
  { name: "definitive-failure", error: "invalid_grant" },
  { name: "definitive-failure-peer-wins", error: "invalid_grant", duringRefresh: peer },
  { name: "definitive-failure-expiry-edit-wins", error: "invalid_grant", duringRefresh: { ...original, expires: now + 7200000 } },
  { name: "definitive-failure-email-edit-wins", error: "invalid_grant", force: true, current: { ...original, expires: now + 3600000 }, duringRefresh: { ...original, expires: now + 3600000, email: "changed@example.invalid" } },
  { name: "definitive-failure-removal-wins", error: "invalid_grant", duringRefresh: "remove" },
  { name: "transient-failure-peer-wins", error: "temporarily_unavailable", duringRefresh: peer },
  { name: "transient-failure", error: "temporarily_unavailable" },
  { name: "keep-on-transient-failure", error: "temporarily_unavailable", keep: true },
  { name: "not-refreshable", canRefresh: false },
  { name: "preserve-subtype-metadata", current: { ...original, apiEndpoint: "https://example.invalid/v1", authorizedAt: now - 86400000 } },
 ]) {
  const state = fixture(spec);
  let result: any = null, error: string | null = null;
  try {
   result = await state.refresher.refreshStored(provider, {
    credentialId: 1, credentialFromRow: (row: any) => row,
    observedCredential: spec.observed, forceRefresh: spec.force,
    canRefresh: () => spec.canRefresh !== false, keepCredentialOnRefreshFailure: spec.keep,
    refresh: state.refresh, isDefinitiveFailure: (error: any) => isDefinitiveOAuthFailure(String(error)),
   });
   result = { ...result, credential: result.credential ?? null };
  } catch (caught) { error = String(caught); }
  scenarios.push({ ...spec, refreshError: spec.error ?? null, result, error, ...state.snapshot() });
 }
 for (const spec of [
  { name: "minted-short-lived", minted: { ...minted, expires: now + 30000 } },
  { name: "minted-already-expired", minted: { ...minted, expires: now } },
 ]) {
  const state = fixture(spec);
  let result: any = null, error: string | null = null;
  try { result = await state.refresher.refresh(provider, original, 1); }
  catch (caught) { error = String(caught); }
  scenarios.push({ ...spec, api: "refresh", result, error, ...state.snapshot() });
 }
 {
  const state = fixture({});
  const [first, second] = await Promise.all([state.refresher.refresh(provider, original, 1), state.refresher.refresh(provider, original, 1)]);
  scenarios.push({ name: "credential-singleflight", api: "refresh", results: [first, second], ...state.snapshot() });
 }
 {
  const state = fixture({});
  await state.refresher.refresh(provider, original, 1);
  const result = await state.refresher.refresh(provider, { ...original, expires: 0 }, 1, undefined, "auth-recovery");
  scenarios.push({ name: "auth-recovery-reuses-local-mint", api: "refresh", result, ...state.snapshot() });
 }
 {
  const state = fixture({});
  await state.refresher.refreshById(1);
  await state.refresher.refreshById(1);
  scenarios.push({ name: "explicit-force-remints", api: "refreshById", ...state.snapshot() });
 }
 {
  const state = fixture({});
  await state.refresher.refreshById(1);
  await state.refresher.refreshById(1, undefined, { reuseRecentMint: true });
  scenarios.push({ name: "explicit-recovery-reuses-mint", api: "refreshById", ...state.snapshot() });
 }
 {
  const state = fixture({});
  const abort = new AbortController(); abort.abort();
  let error: string | null = null;
  try { await state.refresher.refreshStored(provider, { credentialId: 1, credentialFromRow: (row: any) => row, refresh: state.refresh, signal: abort.signal }); }
  catch (caught) { error = String(caught); }
  scenarios.push({ name: "cancel-before-ownership", error, ...state.snapshot() });
 }
 const failureClassification = [
  "invalid_grant", "invalid_token", "unauthorized_client", "grant revoked",
  "unrevoked grant", "refresh token expired", "refresh_token has expired",
  "HTTP 401 unauthorized", "HTTP 401 forbidden", "HTTP 401 network error",
  "HTTP 503 invalid_grant", "HTTP 401 gateway 502", "HTTP 403 forbidden",
  "token refresh failed HTTP 400", "revoked via cloudflare", "HTTP 401 permission_denied",
  "HTTP 401 EAI_AGAIN", "HTTP 401 timeout", "HTTP 401 rate limit 429", "error 1401",
 ].map(message => ({ message, definitive: isDefinitiveOAuthFailure(message) }));
 console.log(JSON.stringify({ source: "NeoPi OWN OAuthRefresher and definitive-failure classifier", now, original, minted, peer, normalizations: [], failureClassification, scenarios }, null, 2));
} finally { Date.now = nativeNow; }
