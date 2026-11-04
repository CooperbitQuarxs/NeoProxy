package neopool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// AccessItem is one piece of provider access in the unified pool view. It never
// contains tokens or keys; Label is an email, a provider label or a key suffix.
type AccessItem struct {
	ID            string          `json:"id"`
	Provider      string          `json:"provider"`
	Kind          string          `json:"kind"`
	Label         string          `json:"label"`
	OwnerID       string          `json:"ownerId"`
	Plan          AccessPlan      `json:"plan"`
	Health        string          `json:"health"`
	HealthMessage string          `json:"healthMessage"`
	Refresh       string          `json:"refresh"`
	ImportedFrom  *ImportedFrom   `json:"importedFrom,omitempty"`
	NeoPiSource   *NeoPiSourceRef `json:"neopiSource,omitempty"`
	Models        []string        `json:"models"`
	// Windows are the configured local token meters (source "configured-metered");
	// virtual model stages only draw from accounts with a window of their kind.
	// Configure them with PUT /v8/neo/vault/accounts/:id/windows/:kind.
	Windows []Window `json:"windows"`
	// ProviderWindows are provider-reported quota windows (NeoPi gateways).
	ProviderWindows []Window    `json:"providerWindows"`
	Usage           AccessUsage `json:"usage"`
	CreatedAt       int64       `json:"createdAt"`
	Actions         []string    `json:"actions"`
}

type ImportedFrom struct {
	Profile      string `json:"profile"`
	CredentialID string `json:"credentialId"`
	ImportedAt   int64  `json:"importedAt"`
}

type NeoPiSourceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AccessUsage: Buckets are the engine's in-memory 10-minute request buckets
// (oldest first, T = bucket start in epoch ms). Requests24h counts pool ledger
// attempts in the last 24 hours, raised to the bucket total when the engine saw
// more traffic than the ledger (legacy routes). Failed24h is only known for the
// bucket window, which the engine keeps in memory since its last restart.
type AccessUsage struct {
	Requests24h int64               `json:"requests24h"`
	Failed24h   int64               `json:"failed24h"`
	LastUsedAt  int64               `json:"lastUsedAt"`
	Buckets     []AccessUsageBucket `json:"buckets"`
}

type AccessUsageBucket struct {
	T    int64 `json:"t"`
	OK   int64 `json:"ok"`
	Fail int64 `json:"fail"`
}

type AccessPerson struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Name   string `json:"name"`
}

type AccessTotals struct {
	MonthlyUSD float64 `json:"monthlyUSD"`
	Items      int     `json:"items"`
	Healthy    int     `json:"healthy"`
}

type AccessList struct {
	Items     []AccessItem   `json:"items"`
	Providers []ProviderInfo `json:"providers"`
	People    []AccessPerson `json:"people"`
	Totals    AccessTotals   `json:"totals"`
}

// AccessError is a user-facing failure: Message says what happened and how to fix it.
type AccessError struct {
	Status  int
	Message string
	// keyRejected marks an upstream 401/403 on a key, so a provider with
	// regional endpoints can try the next region.
	keyRejected bool
}

func (e *AccessError) Error() string { return e.Message }

func accessErr(status int, format string, args ...any) *AccessError {
	return &AccessError{Status: status, Message: fmt.Sprintf(format, args...)}
}

const accessSchema = `CREATE TABLE IF NOT EXISTS neo_access (
 account_id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, plan_id TEXT, monthly_usd REAL, label TEXT,
 disabled INTEGER NOT NULL DEFAULT 0, detached INTEGER NOT NULL DEFAULT 0);`

// accessRow is the durable dashboard state of one item. Engine credentials and
// their ownership stay where they are (engine store, neo_accounts).
type accessRow struct {
	CreatedAt  int64
	PlanID     sql.NullString
	MonthlyUSD sql.NullFloat64
	Label      sql.NullString
	Disabled   bool
	Detached   bool
}

func (s *Store) accessRows(ctx context.Context) (map[string]accessRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_id,created_at,plan_id,monthly_usd,label,disabled,detached FROM neo_access`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]accessRow{}
	for rows.Next() {
		var id string
		var r accessRow
		if err = rows.Scan(&id, &r.CreatedAt, &r.PlanID, &r.MonthlyUSD, &r.Label, &r.Disabled, &r.Detached); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// ensureAccessRow records when NeoProxy first saw an item. The engine's own
// CreatedAt is reused for credential files, which predate this table.
func (s *Store) ensureAccessRow(ctx context.Context, id string, a *coreauth.Auth) (int64, error) {
	created := s.now().UnixMilli()
	if a != nil && !a.CreatedAt.IsZero() && a.AuthSourceKind() == coreauth.AuthSourceFile && a.CreatedAt.UnixMilli() < created {
		created = a.CreatedAt.UnixMilli()
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO neo_access(account_id,created_at) VALUES(?,?) ON CONFLICT(account_id) DO NOTHING`, id, created); err != nil {
		return 0, err
	}
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT created_at FROM neo_access WHERE account_id=?`, id).Scan(&at)
	return at, err
}

type ledgerUsage struct {
	requests24h int64
	lastUsedAt  int64
}

func (s *Store) ledgerUsage(ctx context.Context) (map[string]ledgerUsage, error) {
	since := s.now().Add(-24 * time.Hour).UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT account_id,COALESCE(sum(CASE WHEN created_at>=? THEN 1 ELSE 0 END),0),COALESCE(max(created_at),0) FROM neo_pool_attempts WHERE state!='released' GROUP BY account_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ledgerUsage{}
	for rows.Next() {
		var id string
		var u ledgerUsage
		if err = rows.Scan(&id, &u.requests24h, &u.lastUsedAt); err != nil {
			return nil, err
		}
		out[id] = u
	}
	return out, rows.Err()
}

func (s *Store) people(ctx context.Context, p neo.Principal) ([]AccessPerson, error) {
	query := `SELECT id,handle,name FROM neo_users WHERE role!='service'`
	args := []any{}
	if p.Role != neo.RoleAdmin {
		query += ` AND id=?`
		args = append(args, p.UserID)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY handle`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccessPerson{}
	for rows.Next() {
		var v AccessPerson
		if err = rows.Scan(&v.ID, &v.Handle, &v.Name); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// accessKind classifies by source first, then by credential contents, so an
// uploaded file is a subscription or API key by what it holds.
func accessKind(a *coreauth.Auth) string {
	switch {
	case a.Attributes["neopi_source_id"] != "":
		return "neopi"
	case func() bool { _, ok := coreauth.ImportedFromNeoPi(a); return ok }():
		return "imported"
	case a.AuthSourceKind() == coreauth.AuthSourceConfig:
		return "apiKey"
	}
	switch a.AuthKind() {
	case coreauth.AuthKindOAuth:
		return "subscription"
	case coreauth.AuthKindAPIKey:
		return "apiKey"
	}
	return "file"
}

func engineDisabled(a *coreauth.Auth) bool {
	if a.Disabled || a.Status == coreauth.StatusDisabled {
		return true
	}
	for _, rule := range strings.Split(a.Attributes["excluded_models"], ",") {
		if strings.TrimSpace(rule) == "*" {
			return true
		}
	}
	return false
}

func accessLabel(provider, kind string, a *coreauth.Auth) string {
	if kind == "neopi" && strings.TrimSpace(a.Label) != "" {
		return a.Label
	}
	if kind != "apiKey" && a.AuthKind() == coreauth.AuthKindOAuth {
		if _, email := a.AccountInfo(); email != "" {
			return email
		}
	}
	if email := metaString(a, "email"); email != "" {
		return email
	}
	name := provider
	if p, ok := catalogProvider(provider); ok {
		name = p.Name
	}
	if kind == "apiKey" {
		key := strings.TrimSpace(a.Attributes["api_key"])
		if provider == "custom" {
			name = firstNonEmpty(a.Attributes["compat_name"], name)
		}
		if len(key) >= 16 {
			return name + " key ending in " + key[len(key)-4:]
		}
		return name + " key"
	}
	if label := strings.TrimSpace(a.Label); label != "" && label != a.Provider {
		return label
	}
	return name + " account"
}

func accessHealth(a *coreauth.Auth, state string, cooldownUntil int64, disabled bool, now time.Time) (string, string) {
	if disabled {
		return "disabled", "Paused. NeoProxy does not send requests to it until it is resumed."
	}
	if coreauth.RefreshLockExpired(a, now) {
		return "expired", coreauth.RefreshLockedMessage
	}
	message := ""
	if a.LastError != nil {
		message = strings.TrimSpace(a.LastError.Message)
	}
	if message == "" {
		message = strings.TrimSpace(a.StatusMessage)
	}
	if len(message) > 240 {
		message = message[:240]
	}
	switch state {
	case "cooldown":
		until := ""
		if cooldownUntil > 0 {
			until = " until " + time.UnixMilli(cooldownUntil).UTC().Format(time.RFC3339)
		}
		if a.Quota.Exceeded {
			return "limited", "The provider's usage limit was reached; NeoProxy waits" + until + "."
		}
		return "cooling", "Paused after errors; NeoProxy retries" + until + "."
	case "expired":
		if message == "" {
			message = "The sign-in is no longer valid. Sign in again to restore it."
		}
		return "expired", message
	case "disabled":
		return "disabled", "Paused. NeoProxy does not send requests to it until it is resumed."
	}
	if a.Status == coreauth.StatusError {
		if message == "" {
			message = "The last request failed."
		}
		return "error", message
	}
	return "healthy", ""
}

func accessUsage(a *coreauth.Auth, ledger ledgerUsage, now time.Time) AccessUsage {
	u := AccessUsage{Requests24h: ledger.requests24h, LastUsedAt: ledger.lastUsedAt, Buckets: []AccessUsageBucket{}}
	snapshot := a.RecentRequestsSnapshot(now)
	current := now.Unix() / 600
	var total, lastBucket int64
	for i, b := range snapshot {
		t := (current - int64(len(snapshot)-1-i)) * 600 * 1000
		u.Buckets = append(u.Buckets, AccessUsageBucket{T: t, OK: b.Success, Fail: b.Failed})
		total += b.Success + b.Failed
		u.Failed24h += b.Failed
		if b.Success+b.Failed > 0 {
			lastBucket = t
		}
	}
	if total > u.Requests24h {
		u.Requests24h = total
	}
	if u.LastUsedAt == 0 {
		u.LastUsedAt = lastBucket
	}
	return u
}

// credentialFilePath returns the engine auth file behind a sign-in or file item,
// or "" when the item has none NeoProxy may refresh or hand out.
func credentialFilePath(a *coreauth.Auth, kind string) string {
	if (kind != "subscription" && kind != "file") || a.AuthSourceKind() != coreauth.AuthSourceFile || coreauth.IsRefreshLocked(a) {
		return ""
	}
	return strings.TrimSpace(a.Attributes["path"])
}

func accessActions(p neo.Principal, kind string, owned, fileBacked bool) []string {
	out := []string{}
	admin := p.Role == neo.RoleAdmin
	if admin {
		out = append(out, "reassign", "setPlan")
	}
	if admin || (owned && p.Role == neo.RoleOwner && kind != "neopi") {
		out = append(out, "disable", "remove")
	}
	if fileBacked && (admin || (owned && p.Role == neo.RoleOwner)) {
		out = append(out, "refresh")
	}
	if fileBacked && admin {
		out = append(out, "download")
	}
	if kind == "imported" && (admin || (owned && p.Role == neo.RoleOwner)) {
		out = append(out, "reimport", "signInSeparately")
	}
	return out
}

// buildAccessItem projects one engine credential. account may be nil when the
// credential has not reached the pool snapshot yet (immediately after creation).
func (h *HTTP) buildAccessItem(p neo.Principal, a *coreauth.Auth, account *Account, row accessRow, ledger ledgerUsage, now time.Time) AccessItem {
	kind := accessKind(a)
	provider := accessProviderID(a, kind)
	item := AccessItem{ID: AccountID(a.ID), Provider: provider, Kind: kind, Models: []string{}, Windows: []Window{}, ProviderWindows: []Window{}, CreatedAt: row.CreatedAt}
	state, cooldown := "", int64(0)
	if account != nil {
		item.OwnerID = account.OwnerID
		item.Models = account.ModelIDs
		if account.Windows != nil {
			item.Windows = account.Windows
		}
		if account.ProviderWindows != nil {
			item.ProviderWindows = account.ProviderWindows
		}
		state, cooldown = account.State, account.CooldownUntil
	} else {
		st, next := coreauth.CredentialHealth(a, "", now)
		state = st
		if !next.IsZero() {
			cooldown = next.UnixMilli()
		}
	}
	item.Label = accessLabel(provider, kind, a)
	if row.Label.Valid && strings.TrimSpace(row.Label.String) != "" {
		item.Label = row.Label.String
	}
	override := planOverride{}
	if row.PlanID.Valid {
		override.PlanID = &row.PlanID.String
	}
	if row.MonthlyUSD.Valid {
		override.MonthlyUSD = &row.MonthlyUSD.Float64
	}
	item.Plan = resolvePlan(provider, kind, a, override)
	item.Health, item.HealthMessage = accessHealth(a, state, cooldown, engineDisabled(a) || row.Disabled, now)
	switch kind {
	case "neopi":
		item.Refresh = "neopi"
		item.NeoPiSource = &NeoPiSourceRef{ID: a.Attributes["neopi_source_id"], Name: a.Attributes["neopi_source_label"]}
	case "imported":
		item.Refresh = "locked"
		if prov, ok := coreauth.ImportedFromNeoPi(a); ok {
			item.ImportedFrom = &ImportedFrom{Profile: prov.Profile, CredentialID: prov.CredentialID, ImportedAt: prov.ImportedAt}
		}
	case "subscription":
		item.Refresh = "auto"
	default:
		item.Refresh = "none"
	}
	item.Usage = accessUsage(a, ledger, now)
	item.Actions = accessActions(p, kind, item.OwnerID != "" && item.OwnerID == p.UserID, credentialFilePath(a, kind) != "")
	return item
}

// visibleTo: admins see every item; everyone else sees what they contribute.
func visibleTo(p neo.Principal, ownerID string) bool {
	return p.Role == neo.RoleAdmin || (ownerID != "" && ownerID == p.UserID)
}

// AccessItems lists every piece of provider access the principal may see.
func (h *HTTP) AccessItems(ctx context.Context, p neo.Principal) ([]AccessItem, error) {
	snapshot, err := h.pool.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := h.pool.accessRows(ctx)
	if err != nil {
		return nil, err
	}
	ledger, err := h.pool.ledgerUsage(ctx)
	if err != nil {
		return nil, err
	}
	now := h.pool.now()
	items := make([]AccessItem, 0, len(snapshot.Accounts))
	for i := range snapshot.Accounts {
		account := &snapshot.Accounts[i]
		if !visibleTo(p, account.OwnerID) {
			continue
		}
		a, ok := h.pool.manager.GetByID(account.EngineAuthID)
		if !ok || a == nil {
			continue
		}
		row, exists := rows[account.ID]
		if !exists {
			if row.CreatedAt, err = h.pool.ensureAccessRow(ctx, account.ID, a); err != nil {
				return nil, err
			}
		}
		if row.Detached {
			continue
		}
		items = append(items, h.buildAccessItem(p, a, account, row, ledger[account.ID], now))
	}
	return items, nil
}

// AccessList is the full GET /v8/neo/access payload.
func (h *HTTP) AccessList(ctx context.Context, p neo.Principal) (AccessList, error) {
	items, err := h.AccessItems(ctx, p)
	if err != nil {
		return AccessList{}, err
	}
	people, err := h.pool.people(ctx, p)
	if err != nil {
		return AccessList{}, err
	}
	out := AccessList{Items: items, Providers: providerList(items), People: people}
	for _, item := range items {
		out.Totals.Items++
		out.Totals.MonthlyUSD += item.Plan.MonthlyUSD
		if item.Health == "healthy" {
			out.Totals.Healthy++
		}
	}
	return out, nil
}

// engineAuthForAccess resolves an access item ID to its engine credential.
func (h *HTTP) engineAuthForAccess(id string) (*coreauth.Auth, bool) {
	if h.pool.manager == nil || !strings.HasPrefix(id, "acct_") {
		return nil, false
	}
	for _, a := range h.pool.manager.List() {
		if a != nil && AccountID(a.ID) == id {
			return a, true
		}
	}
	return nil, false
}

// AccessItemByEngineID renders one credential for the principal, also when it
// is not yet part of the pool snapshot. ErrUnavailable hides other people's items.
func (h *HTTP) AccessItemByEngineID(ctx context.Context, p neo.Principal, engineAuthID string) (AccessItem, error) {
	items, err := h.AccessItems(ctx, p)
	if err != nil {
		return AccessItem{}, err
	}
	id := AccountID(engineAuthID)
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	a, ok := h.pool.manager.GetByID(engineAuthID)
	if !ok || a == nil {
		return AccessItem{}, ErrUnavailable
	}
	var owner sql.NullString
	if err = h.pool.db.QueryRowContext(ctx, `SELECT owner_id FROM neo_accounts WHERE id=?`, id).Scan(&owner); err != nil && err != sql.ErrNoRows {
		return AccessItem{}, err
	}
	if !visibleTo(p, owner.String) {
		return AccessItem{}, ErrUnavailable
	}
	created, err := h.pool.ensureAccessRow(ctx, id, a)
	if err != nil {
		return AccessItem{}, err
	}
	rows, err := h.pool.accessRows(ctx)
	if err != nil {
		return AccessItem{}, err
	}
	row := rows[id]
	row.CreatedAt = created
	item := h.buildAccessItem(p, a, nil, row, ledgerUsage{}, h.pool.now())
	item.OwnerID = owner.String
	item.Actions = accessActions(p, item.Kind, owner.String != "" && owner.String == p.UserID, credentialFilePath(a, item.Kind) != "")
	return item, nil
}
