package neopool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Native OAuth subscriptions (Claude, Codex/ChatGPT) report their own 5h and
// weekly usage. NeoProxy asks the provider with the account's own access token,
// keeps the answer in memory and shows it as Account.ProviderWindows. The token
// is read from the engine auth (Metadata["access_token"], the same credential
// the executors send); nothing is persisted and tokens are never logged.
const (
	// quotaPollInterval is how often the background poller re-reads every account.
	quotaPollInterval = 5 * time.Minute
	// quotaKeepLastGood is how long the last successful reading is still shown
	// after the provider stops answering; later the windows become unknown.
	quotaKeepLastGood   = 30 * time.Minute
	quotaRequestTimeout = 15 * time.Second
	quotaIdleTimeout    = 90 * time.Second
	quotaMaxBody        = 1 << 20

	claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"

	quotaSourceReported    = "provider-reported"
	quotaSourceUnavailable = "provider-unavailable"
)

type quotaEntry struct {
	windows []Window // last good reading
	goodAt  time.Time
	failing bool
	gen     uint64 // request generation that produced this entry
}

// quotaProber caches provider-reported windows by engine auth ID.
type quotaProber struct {
	mu        sync.Mutex
	entries   map[string]quotaEntry
	gen       uint64
	claudeURL string
	codexURL  string
	clientFor func(*coreauth.Auth) *http.Client
}

func newQuotaProber() *quotaProber {
	clients := &quotaClients{clients: map[string]*http.Client{}}
	return &quotaProber{entries: map[string]quotaEntry{}, claudeURL: claudeUsageURL, codexURL: codexUsageURL, clientFor: clients.client}
}

// quotaClients keeps one client per proxy setting, so repeated polls reuse
// their connections instead of leaving a new transport's idle ones behind.
type quotaClients struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

// client never follows redirects with a bearer. Only an explicit http(s)
// proxy on the auth is honoured; otherwise the environment decides.
func (c *quotaClients) client(a *coreauth.Auth) *http.Client {
	proxy := strings.TrimSpace(a.ProxyURL)
	key, proxyFunc := "", http.ProxyFromEnvironment
	switch {
	case strings.EqualFold(proxy, "direct") || strings.EqualFold(proxy, "none"):
		key, proxyFunc = "direct", nil
	case proxy != "":
		if u, err := url.Parse(proxy); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			key, proxyFunc = "proxy:"+u.String(), http.ProxyURL(u)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients[key]; ok {
		return client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = proxyFunc
	transport.IdleConnTimeout = quotaIdleTimeout
	client := &http.Client{Timeout: quotaRequestTimeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.clients[key] = client
	return client
}

// quotaProvider reports which usage endpoint serves this credential, or ""
// when it is not a native OAuth sign-in of a provider that has one.
func quotaProvider(a *coreauth.Auth) string {
	if a == nil || a.Attributes["neopi_source_id"] != "" || a.AuthKind() != coreauth.AuthKindOAuth {
		return ""
	}
	if kind := accessKind(a); kind != "subscription" && kind != "imported" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(a.Provider)) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	}
	return ""
}

func quotaAccessToken(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	token, _ := a.Metadata["access_token"].(string)
	return strings.TrimSpace(token)
}

// codexChatGPTAccountID is the chatgpt_account_id claim of the access token,
// falling back to the account_id the sign-in stored.
func codexChatGPTAccountID(a *coreauth.Auth, token string) string {
	if claims, err := codex.ParseJWTToken(token); err == nil && claims.GetAccountID() != "" {
		return claims.GetAccountID()
	}
	id, _ := a.Metadata["account_id"].(string)
	return strings.TrimSpace(id)
}

func (q *quotaProber) get(ctx context.Context, client *http.Client, endpoint string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid usage request")
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("usage endpoint unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage endpoint returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, quotaMaxBody+1))
	if err != nil || len(data) > quotaMaxBody {
		return nil, fmt.Errorf("usage response unreadable or too large")
	}
	return data, nil
}

func percentWindow(kind string, percent float64, startsAt, resetsAt time.Time) Window {
	percent = math.Min(100, math.Max(0, percent))
	w := Window{Kind: kind, Known: true, Source: quotaSourceReported, Unit: "percent", Capacity: 100, UsedTokens: int64(math.Round(percent)), Used: percent / 100}
	if !resetsAt.IsZero() {
		w.ResetsAt = resetsAt.UnixMilli()
		w.StartsAt = startsAt.UnixMilli()
	}
	return w
}

// parseClaudeUsage reads GET /api/oauth/usage: five_hour and seven_day carry
// utilization (0..100) and resets_at (RFC3339); either may be null.
func parseClaudeUsage(data []byte) ([]Window, error) {
	// Only the two windows matter; the response carries many other keys whose
	// shapes change between releases, so they are left as raw JSON.
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("invalid usage response")
	}
	type usageWindow struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    *string  `json:"resets_at"`
	}
	out := []Window{}
	for _, spec := range []struct {
		field, kind string
		length      time.Duration
	}{{"five_hour", "5h", 5 * time.Hour}, {"seven_day", "week", 7 * 24 * time.Hour}} {
		raw, ok := body[spec.field]
		if !ok {
			continue
		}
		var v usageWindow
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &v); err != nil {
				continue
			}
		}
		// A null window means nothing was used in it yet: 0%, reset unknown.
		var used float64
		var resets time.Time
		if v.Utilization != nil {
			used = *v.Utilization
		}
		if v.ResetsAt != nil {
			if t, err := time.Parse(time.RFC3339Nano, *v.ResetsAt); err == nil {
				resets = t
			}
		}
		var starts time.Time
		if !resets.IsZero() {
			starts = resets.Add(-spec.length)
		}
		out = append(out, percentWindow(spec.kind, used, starts, resets))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("usage response has no windows")
	}
	return out, nil
}

type codexUsageWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds int64    `json:"limit_window_seconds"`
	ResetAfterSeconds  *int64   `json:"reset_after_seconds"`
	ResetAt            int64    `json:"reset_at"`
}

// parseCodexUsage reads GET /wham/usage: rate_limit.primary_window and
// secondary_window. A window of at most five hours is the 5h window, a longer
// one the weekly window; without a length the primary is 5h, the secondary week.
func parseCodexUsage(data []byte, now time.Time) ([]Window, error) {
	var body struct {
		RateLimit struct {
			Primary   *codexUsageWindow `json:"primary_window"`
			Secondary *codexUsageWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("invalid usage response")
	}
	out := []Window{}
	seen := map[string]bool{}
	for i, v := range []*codexUsageWindow{body.RateLimit.Primary, body.RateLimit.Secondary} {
		if v == nil || v.UsedPercent == nil {
			continue
		}
		kind, length := "5h", 5*time.Hour
		if v.LimitWindowSeconds > 0 {
			length = time.Duration(v.LimitWindowSeconds) * time.Second
			if length > 5*time.Hour {
				kind = "week"
			}
		} else if i == 1 {
			kind, length = "week", 7*24*time.Hour
		}
		if seen[kind] {
			continue
		}
		seen[kind] = true
		var resets time.Time
		switch {
		case v.ResetAt > 0:
			resets = time.Unix(v.ResetAt, 0)
		case v.ResetAfterSeconds != nil && *v.ResetAfterSeconds >= 0:
			resets = now.Add(time.Duration(*v.ResetAfterSeconds) * time.Second)
		}
		out = append(out, percentWindow(kind, *v.UsedPercent, resets.Add(-length), resets))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("usage response has no windows")
	}
	return out, nil
}

// fetch asks the provider for the account's current windows.
func (q *quotaProber) fetch(ctx context.Context, a *coreauth.Auth, now time.Time) ([]Window, error) {
	token := quotaAccessToken(a)
	if token == "" {
		return nil, fmt.Errorf("sign-in has no access token")
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	client := q.clientFor(a)
	switch quotaProvider(a) {
	case "claude":
		headers["anthropic-beta"] = "oauth-2025-04-20"
		headers["User-Agent"] = "claude-code/2.1.70"
		data, err := q.get(ctx, client, q.claudeURL, headers)
		if err != nil {
			return nil, err
		}
		return parseClaudeUsage(data)
	case "codex":
		if id := codexChatGPTAccountID(a, token); id != "" {
			headers["chatgpt-account-id"] = id
		}
		data, err := q.get(ctx, client, q.codexURL, headers)
		if err != nil {
			return nil, err
		}
		return parseCodexUsage(data, now)
	}
	return nil, fmt.Errorf("provider has no usage endpoint")
}

// refresh fetches and caches one account. A failure keeps the last good reading.
// A result older than the stored one (a slow poll overtaken by a manual refresh)
// is dropped, so a newer reading is never overwritten.
func (q *quotaProber) refresh(ctx context.Context, a *coreauth.Auth, now time.Time) error {
	q.mu.Lock()
	q.gen++
	gen := q.gen
	q.mu.Unlock()
	windows, err := q.fetch(ctx, a, now)
	q.mu.Lock()
	entry := q.entries[a.ID]
	if entry.gen > gen {
		q.mu.Unlock()
		return err
	}
	wasFailing := entry.failing
	if err == nil {
		entry = quotaEntry{windows: windows, goodAt: now, gen: gen}
	} else {
		entry.failing = true
		entry.gen = gen
	}
	q.entries[a.ID] = entry
	q.mu.Unlock()
	if err != nil {
		entryLog := log.WithField("account", AccountID(a.ID)).WithField("provider", a.Provider).WithError(err)
		if wasFailing {
			entryLog.Debug("NeoProxy quota: provider usage still unavailable")
		} else {
			entryLog.Warn("NeoProxy quota: provider usage unavailable")
		}
	}
	return err
}

// windows returns what the snapshot shows for a credential: nil when nothing
// was asked yet, the last good reading for 30 minutes after a failure, and
// unknown windows afterwards.
func (q *quotaProber) windows(a *coreauth.Auth, now time.Time) []Window {
	if quotaProvider(a) == "" {
		return nil
	}
	q.mu.Lock()
	entry, ok := q.entries[a.ID]
	q.mu.Unlock()
	if !ok {
		return nil
	}
	if !entry.goodAt.IsZero() && now.Sub(entry.goodAt) < quotaKeepLastGood {
		return append([]Window(nil), entry.windows...)
	}
	kinds := []string{"5h", "week"}
	if len(entry.windows) > 0 {
		kinds = kinds[:0]
		for _, w := range entry.windows {
			kinds = append(kinds, w.Kind)
		}
	}
	out := make([]Window, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, Window{Kind: kind, Known: false, Source: quotaSourceUnavailable, Unit: "percent", Capacity: 100})
	}
	return out
}

// forget drops accounts the engine no longer has.
func (q *quotaProber) forget(live map[string]bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id := range q.entries {
		if !live[id] {
			delete(q.entries, id)
		}
	}
}

// RefreshQuota reads one credential's provider usage now. It reports false when
// the credential has no provider usage endpoint.
func (s *Store) RefreshQuota(ctx context.Context, a *coreauth.Auth) (bool, error) {
	if quotaProvider(a) == "" {
		return false, nil
	}
	err := s.quota.refresh(ctx, a, s.now())
	s.notifyChanged()
	return true, err
}

func (s *Store) notifyChanged() {
	s.mu.Lock()
	s.notifyLocked()
	s.mu.Unlock()
}

func (s *Store) refreshAllQuota(ctx context.Context) {
	live := map[string]bool{}
	for _, a := range s.manager.List() {
		if ctx.Err() != nil {
			return
		}
		if quotaProvider(a) == "" {
			continue
		}
		live[a.ID] = true
		_ = s.quota.refresh(ctx, a, s.now())
	}
	s.quota.forget(live)
	s.notifyChanged()
}

// pollQuota refreshes every native OAuth account every quotaPollInterval until
// ctx is cancelled.
func (s *Store) pollQuota(ctx context.Context) {
	ticker := time.NewTicker(quotaPollInterval)
	defer ticker.Stop()
	for {
		s.refreshAllQuota(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// accessQuotaRefresh is POST /v8/neo/access/:id/quota/refresh: read the
// provider's usage for one sign-in now and return the updated item.
func (h *HTTP) accessQuotaRefresh(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	a, owner, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	if p.Role != neo.RoleAdmin && !(owner == p.UserID && p.Role == neo.RoleOwner) {
		poolError(c, http.StatusForbidden, "Only the contributor or an administrator can refresh this usage.")
		return
	}
	supported, err := h.pool.RefreshQuota(c.Request.Context(), a)
	if !supported {
		poolError(c, http.StatusConflict, "The provider does not report usage for this kind of access.")
		return
	}
	if err != nil {
		poolError(c, http.StatusBadGateway, fmt.Sprintf("The provider did not report usage (%s). NeoProxy keeps the last reading for 30 minutes and retries every 5 minutes.", err.Error()))
		return
	}
	h.respondItem(c, p, a.ID, http.StatusOK)
}
