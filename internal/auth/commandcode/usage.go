// Package commandcode is NeoPi's Command Code account surface beside
// inference: the usage report of ai/src/usage/commandcode.ts, read from the
// account routes the Command Code CLI calls with the same Provider API key.
package commandcode

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

const (
	// Provider is NeoPi's provider id.
	Provider = "commandcode"
	// defaultOrigin is DEFAULT_ORIGIN, the host the account routes sit at.
	defaultOrigin = "https://api.commandcode.ai"
	whoamiPath    = "/alpha/whoami"
	creditsPath   = "/alpha/billing/credits"
	// maxBody bounds an account route's answer.
	maxBody = 1 << 20
)

// window is one WINDOWS entry: the credit window's field and limit identity.
type window struct {
	key, id    string
	durationMs int64
}

var windows = []window{
	{key: "fiveHour", id: "5h", durationMs: int64(5 * time.Hour / time.Millisecond)},
	{key: "weekly", id: "7d", durationMs: int64(7 * 24 * time.Hour / time.Millisecond)},
}

// Usage is NeoPi's Command Code usage report: the plan's credit windows (a
// pay-as-you-go account has none) and the credit balance.
type Usage struct {
	Limits []UsageLimit
	// Balance is the remaining credits: monthly, purchased and free.
	Balance   float64
	AccountID string
	Email     string
	OrgID     string
	OrgName   string
	PlanType  string
}

// UsageLimit is one credit window.
type UsageLimit struct {
	// ID is "commandcode:5h" or "commandcode:7d"; WindowID is "5h" or "7d".
	ID         string
	WindowID   string
	DurationMs int64
	// ResetsAt is zero when the window reports no usable reset.
	ResetsAt time.Time
	Used     float64
	// Cap and UsedFraction are nil when the window reports no positive cap.
	Cap          *float64
	UsedFraction *float64
	// Status is exhausted for an exceeded window, else usageStatus of the
	// fraction: ok, warning (90%), exhausted, or unknown without one.
	Status string
}

// UsageError is a revoked key (401): NeoPi throws so the cached report is
// purged rather than served again.
type UsageError struct {
	Status int
	Reason string
}

func (e *UsageError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("Command Code usage endpoint returned %d %s", e.Status, e.Reason))
}

// FetchUsage reads the key's credit windows and balance from the account
// routes at the origin of baseURL (the inference base carries /provider). A
// nil report without error is NeoPi's null: any failure but a 401, an
// identity without a user id, or credits without a balance.
func FetchUsage(ctx context.Context, client *http.Client, baseURL, key string) (*Usage, error) {
	if key == "" {
		return nil, nil
	}
	origin := resolveOrigin(baseURL)
	whoami, err := getJSON(ctx, client, origin+whoamiPath, key)
	if err != nil || !whoami.Exists() {
		return nil, err
	}
	userID := nonEmpty(whoami.Get("user.id"))
	if !whoami.Get("user").IsObject() || userID == "" {
		return nil, nil
	}
	org := whoami.Get("org")
	var orgID, orgName string
	if org.IsObject() {
		orgID, orgName = nonEmpty(org.Get("id")), nonEmpty(org.Get("login"))
	}
	creditsURL := origin + creditsPath
	if orgID != "" {
		creditsURL += "?orgId=" + encodeURIComponent(orgID)
	}
	body, err := getJSON(ctx, client, creditsURL, key)
	if err != nil || !body.Exists() {
		return nil, err
	}
	credits := body.Get("credits")
	if !credits.IsObject() {
		return nil, nil
	}
	monthly, okMonthly := finite(credits.Get("monthlyCredits"))
	purchased, okPurchased := finite(credits.Get("purchasedCredits"))
	free, okFree := finite(credits.Get("freeCredits"))
	if !okMonthly && !okPurchased && !okFree {
		return nil, nil
	}
	usage := &Usage{Balance: monthly + purchased + free, AccountID: userID, Email: nonEmpty(whoami.Get("user.email")),
		OrgID: orgID, OrgName: orgName, PlanType: nonEmpty(credits.Get("planId"))}
	windowLimits := body.Get("windowLimits")
	for _, spec := range windows {
		if !windowLimits.IsObject() {
			break
		}
		if limit, ok := buildWindowLimit(spec, windowLimits.Get(spec.key)); ok {
			usage.Limits = append(usage.Limits, limit)
		}
	}
	return usage, nil
}

// buildWindowLimit is buildWindowLimit: a window with a finite used amount.
func buildWindowLimit(spec window, raw gjson.Result) (UsageLimit, bool) {
	if !raw.IsObject() {
		return UsageLimit{}, false
	}
	used, ok := finite(raw.Get("used"))
	if !ok {
		return UsageLimit{}, false
	}
	limit := UsageLimit{ID: Provider + ":" + spec.id, WindowID: spec.id, DurationMs: spec.durationMs, Used: used}
	if capValue, okCap := finite(raw.Get("cap")); okCap {
		limit.Cap = &capValue
		if capValue > 0 {
			fraction := used / capValue
			limit.UsedFraction = &fraction
		}
	}
	if resetsAt, okReset := positiveTimestamp(raw.Get("resetAt")); okReset {
		limit.ResetsAt = time.UnixMilli(resetsAt)
	}
	limit.Status = usageStatus(limit.UsedFraction)
	if raw.Get("exceeded").Type == gjson.True {
		limit.Status = "exhausted"
	}
	return limit, true
}

// resolveOrigin is resolveOrigin: the configured base's origin (URL.origin:
// lowercased, without a default port), else the default; a blank or
// unparseable base is not configured.
func resolveOrigin(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return defaultOrigin
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return defaultOrigin
	}
	scheme, host := strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := parsed.Port(); port != "" && !(scheme == "https" && port == "443" || scheme == "http" && port == "80") {
		host += ":" + port
	}
	return scheme + "://" + host
}

// finite is finiteNumber: a JSON number that is finite.
func finite(value gjson.Result) (float64, bool) {
	if value.Type != gjson.Number || math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
		return 0, false
	}
	return value.Float(), true
}

// positiveTimestamp is parsePositiveTimestamp: a positive epoch number (or
// numeric string) in seconds or milliseconds, as milliseconds.
func positiveTimestamp(value gjson.Result) (int64, bool) {
	var parsed float64
	switch value.Type {
	case gjson.Number:
		parsed = value.Float()
	case gjson.String:
		text := strings.TrimSpace(value.String())
		if text == "" {
			return 0, false
		}
		number, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, false
		}
		parsed = number
	default:
		return 0, false
	}
	if math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed <= 0 {
		return 0, false
	}
	if parsed < 1_000_000_000_000 {
		parsed *= 1000
	}
	return int64(parsed), true
}

// usageStatus is usage/shared.ts usageStatus.
func usageStatus(fraction *float64) string {
	switch {
	case fraction == nil:
		return "unknown"
	case *fraction >= 1:
		return "exhausted"
	case *fraction >= 0.9:
		return "warning"
	}
	return "ok"
}

func nonEmpty(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return value.String()
}

// encodeURIComponent escapes all but A-Z a-z 0-9 - _ . ! ~ * ' ( ).
func encodeURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9', strings.IndexByte("-_.!~*'()", b) >= 0:
			out.WriteByte(b)
		default:
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}

// getJSON is getJson: the answer's data object (or the answer itself) for a
// 2xx JSON object; a missing result for any other answer or failure, except
// a 401, which is a UsageError.
func getJSON(ctx context.Context, client *http.Client, target, key string) (gjson.Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return gjson.Result{}, nil
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return gjson.Result{}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return gjson.Result{}, &UsageError{Status: resp.StatusCode, Reason: strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return gjson.Result{}, nil
	}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return gjson.Result{}, nil
	}
	defer func() { _ = decoded.Close() }()
	body, err := io.ReadAll(io.LimitReader(decoded, maxBody))
	if err != nil || !gjson.ValidBytes(body) {
		return gjson.Result{}, nil
	}
	payload := gjson.ParseBytes(body)
	if !payload.IsObject() {
		return gjson.Result{}, nil
	}
	if data := payload.Get("data"); data.IsObject() {
		return data, nil
	}
	return payload, nil
}
