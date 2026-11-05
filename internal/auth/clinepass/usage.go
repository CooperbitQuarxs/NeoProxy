// Package clinepass is NeoPi's ClinePass account surface beside inference:
// the plan quota report of ai/src/usage/cline-pass.ts. A ClinePass key reads
// its own quota; account OAuth is not required.
package clinepass

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

const (
	// Provider is NeoPi's provider id.
	Provider = "cline-pass"
	// BaseURL is CLINEPASS_API_BASE_URL.
	BaseURL = "https://api.cline.bot/api/v1"
	// usageLimitsPath is USAGE_LIMITS_PATH, the dashboard quota route.
	usageLimitsPath = "/users/me/plan/usage-limits"
	// accountPath is ACCOUNT_PATH, read for the account identity.
	accountPath = "/users/me"
	// maxBody bounds a usage or account answer.
	maxBody = 1 << 20
)

// window is one WINDOW_CONFIG entry.
type window struct {
	id         string
	durationMs int64
}

var windows = map[string]window{
	"five_hour": {id: "5h", durationMs: int64(5 * time.Hour / time.Millisecond)},
	"weekly":    {id: "7d", durationMs: int64(7 * 24 * time.Hour / time.Millisecond)},
	"monthly":   {id: "30d", durationMs: int64(30 * 24 * time.Hour / time.Millisecond)},
}

// Usage is NeoPi's ClinePass usage report.
type Usage struct {
	Limits []UsageLimit
	// Email and AccountID identify the account when /users/me answered.
	Email     string
	AccountID string
}

// UsageLimit is one plan window, in percent of the plan's allowance.
type UsageLimit struct {
	// ID is "cline-pass:<window>".
	ID string
	// WindowID is "5h", "7d" or "30d".
	WindowID   string
	DurationMs int64
	// ResetsAt is zero when the window reports no parseable reset.
	ResetsAt time.Time
	// Used is percentUsed floored at zero; UsedFraction is Used/100.
	Used         float64
	UsedFraction float64
	// Status is usageStatus: ok, warning (90%) or exhausted.
	Status string
}

// UsageError is a rejected key (401/403): NeoPi throws so the credential is
// flagged bad rather than unknown.
type UsageError struct {
	Status int
	Reason string
}

func (e *UsageError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("ClinePass usage endpoint returned %d %s", e.Status, e.Reason))
}

// FetchUsage reads the key's plan quota. A nil report without error means
// ClinePass reported nothing NeoPi meters: an account without a plan (404 "no
// plan history"), a transient failure or a report without a usable window.
func FetchUsage(ctx context.Context, client *http.Client, base, key string) (*Usage, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = BaseURL
	}
	status, reason, body, err := get(ctx, client, base+usageLimitsPath, key)
	if err != nil {
		return nil, nil
	}
	if status < 200 || status >= 300 {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, &UsageError{Status: status, Reason: reason}
		}
		// An account that never subscribed answers 404 "no plan history found
		// for user"; NeoPi only logs it differently from other failures.
		return nil, nil
	}
	payload := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !payload.IsObject() {
		return nil, nil
	}
	data := payload
	if inner := payload.Get("data"); inner.IsObject() {
		data = inner
	}
	limits := data.Get("limits")
	if !limits.IsArray() {
		return nil, nil
	}
	usage := &Usage{}
	for _, raw := range limits.Array() {
		if limit, ok := parseLimit(raw); ok {
			usage.Limits = append(usage.Limits, limit)
		}
	}
	if len(usage.Limits) == 0 {
		return nil, nil
	}
	usage.Email, usage.AccountID = accountIdentity(ctx, client, base+accountPath, key)
	return usage, nil
}

// parseLimit is parseLimit: a known window with a finite percentUsed.
func parseLimit(raw gjson.Result) (UsageLimit, bool) {
	kind := raw.Get("type")
	percent := raw.Get("percentUsed")
	if !raw.IsObject() || kind.Type != gjson.String || percent.Type != gjson.Number {
		return UsageLimit{}, false
	}
	config, ok := windows[kind.String()]
	if !ok || math.IsNaN(percent.Float()) || math.IsInf(percent.Float(), 0) {
		return UsageLimit{}, false
	}
	used := math.Max(0, percent.Float())
	fraction := used / 100
	limit := UsageLimit{ID: Provider + ":" + config.id, WindowID: config.id, DurationMs: config.durationMs, Used: used, UsedFraction: fraction, Status: usageStatus(fraction)}
	if resets := raw.Get("resetsAt"); resets.Type == gjson.String {
		if parsed, err := parseResetTime(resets.String()); err == nil {
			limit.ResetsAt = parsed
		}
	}
	return limit, true
}

// parseResetTime is Date.parse for the ISO 8601 stamps the quota route sends.
func parseResetTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

// usageStatus is usage/shared.ts usageStatus.
func usageStatus(fraction float64) string {
	switch {
	case fraction >= 1:
		return "exhausted"
	case fraction >= 0.9:
		return "warning"
	}
	return "ok"
}

// accountIdentity is fetchAccountIdentity: the account's email and id, or
// nothing when the route fails.
func accountIdentity(ctx context.Context, client *http.Client, url, key string) (email, accountID string) {
	status, _, body, err := get(ctx, client, url, key)
	if err != nil || status < 200 || status >= 300 || !gjson.ValidBytes(body) {
		return "", ""
	}
	payload := gjson.ParseBytes(body)
	if !payload.IsObject() {
		return "", ""
	}
	data := payload
	if inner := payload.Get("data"); inner.IsObject() {
		data = inner
	}
	return nonEmpty(data.Get("email")), nonEmpty(data.Get("id"))
}

func nonEmpty(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.String())
}

// get sends NeoPi's quota request: the Cline client identity, JSON accept and
// the key as a bearer token.
func get(ctx context.Context, client *http.Client, url, key string) (status int, reason string, body []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, "", nil, err
	}
	for name, value := range openaiwire.ClinePassClientHeaders("") {
		req.Header.Set(name, value)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Connection", "keep-alive")
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return 0, "", nil, err
	}
	defer func() { _ = decoded.Close() }()
	body, err = io.ReadAll(io.LimitReader(decoded, maxBody))
	if err != nil {
		return 0, "", nil, err
	}
	return resp.StatusCode, strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode))), body, nil
}
