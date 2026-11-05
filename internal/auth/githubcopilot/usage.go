package githubcopilot

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Usage is the report NeoPi's GitHub Copilot usage provider
// (pi-ai usage/github-copilot.ts) builds for a sign-in: the quota snapshots of
// /copilot_internal/user as monthly limits, labelled by the token's login.
type Usage struct {
	AccountID      string
	Plan           string
	QuotaResetDate string
	Limits         []UsageLimit
}

// UsageLimit is one UsageLimit of the report; nil amounts are NeoPi's
// undefined.
type UsageLimit struct {
	ID     string       `json:"id"`
	Label  string       `json:"label"`
	Scope  UsageScope   `json:"scope"`
	Window *UsageWindow `json:"window,omitempty"`
	Amount UsageAmount  `json:"amount"`
	Status string       `json:"status"`
	Notes  []string     `json:"notes,omitempty"`
}

// UsageScope names whose quota a limit meters.
type UsageScope struct {
	Provider  string `json:"provider"`
	AccountID string `json:"accountId,omitempty"`
	Tier      string `json:"tier,omitempty"`
	WindowID  string `json:"windowId,omitempty"`
}

// UsageWindow is the monthly window the quota resets with (ms since epoch).
type UsageWindow struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	ResetsAt int64  `json:"resetsAt"`
}

// UsageAmount is a limit's consumption in requests.
type UsageAmount struct {
	Used              *float64 `json:"used,omitempty"`
	Limit             *float64 `json:"limit,omitempty"`
	Remaining         *float64 `json:"remaining,omitempty"`
	UsedFraction      *float64 `json:"usedFraction,omitempty"`
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	Unit              string   `json:"unit"`
}

// usageAPIBaseURL is resolveGitHubApiBaseUrl for a sign-in: GitHub's API, or
// the Enterprise instance's API host.
func usageAPIBaseURL(enterpriseURL string) string {
	enterprise := strings.TrimSpace(enterpriseURL)
	switch {
	case enterprise == "":
		return "https://api.github.com"
	case strings.HasPrefix(enterprise, "http://") || strings.HasPrefix(enterprise, "https://"):
		return strings.TrimSuffix(enterprise, "/")
	case strings.HasPrefix(enterprise, "api."):
		return "https://" + enterprise
	}
	return "https://api." + enterprise
}

// FetchUsage is githubCopilotUsageProvider.fetchUsage for an OAuth sign-in:
// GET /copilot_internal/user with the GitHub token, normalized into the
// premium, chat and completions limits, labelled by the login /user reports
// (looked up with the refresh then the access token, which NeoPi stores alike;
// a failed lookup only leaves the label empty). A report refused with HTTP 401
// or 403 is an *Error carrying that status, after which NeoPi drops the last
// good report; any other failure is transient.
func FetchUsage(ctx context.Context, client *http.Client, credential *Credential) (*Usage, error) {
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return nil, fmt.Errorf("github-copilot usage: sign-in has no token")
	}
	base := usageAPIBaseURL(credential.EnterpriseURL)
	token := credential.AccessToken
	headers := GitHubHeaders()
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	headers.Set("Authorization", "Bearer "+token)
	body, err := usageGet(ctx, client, base+"/copilot_internal/user", headers)
	if err != nil {
		return nil, err
	}
	data := gjson.ParseBytes(body)
	if !gjson.ValidBytes(body) || !data.IsObject() {
		return nil, &Error{Status: http.StatusOK, Message: "Invalid Copilot usage response"}
	}
	// The report already landed; the login only labels it.
	account := usageLogin(ctx, client, base, token)
	if account == "" {
		account = usageLogin(ctx, client, base, token)
	}
	usage := &Usage{AccountID: account}
	if plan := data.Get("copilot_plan"); plan.Type == gjson.String {
		usage.Plan = plan.String()
	}
	if reset := data.Get("quota_reset_date"); reset.Type == gjson.String {
		usage.QuotaResetDate = reset.String()
	}
	var window *UsageWindow
	if resetsAt, ok := parseUsageTimestamp(usage.QuotaResetDate); ok {
		window = &UsageWindow{ID: "monthly", Label: "Monthly", ResetsAt: resetsAt}
	}
	snapshots := data.Get("quota_snapshots")
	for _, quota := range []struct {
		key, label, path string
		always           bool
	}{{"premium", "Premium Requests", "premium_interactions", true}, {"chat", "Chat Requests", "chat", false}, {"completions", "Completions", "completions", false}} {
		detail, ok := parseQuotaDetail(snapshots.Get(quota.path))
		if !ok || !quota.always && detail.unlimited {
			continue
		}
		usage.Limits = append(usage.Limits, quotaLimit(quota.key, quota.label, detail, usage.Plan, window, account))
	}
	return usage, nil
}

// usageGet is fetchJson's request: a non-2xx answer is an *Error with its
// status.
func usageGet(ctx context.Context, client *http.Client, url string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header = headers
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github-copilot usage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("github-copilot usage: read %s: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("%s: %s", resp.Status, body)}
	}
	return body, nil
}

// usageLogin is resolveGitHubUsername, empty when the lookup fails or names
// no login.
func usageLogin(ctx context.Context, client *http.Client, base, token string) string {
	headers := http.Header{}
	headers.Set("Accept", "application/vnd.github+json")
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("X-GitHub-Api-Version", "2022-11-28")
	body, err := usageGet(ctx, client, base+"/user", headers)
	if err != nil {
		return ""
	}
	if login := gjson.GetBytes(body, "login"); gjson.ValidBytes(body) && login.Type == gjson.String {
		return login.String()
	}
	return ""
}

type quotaDetail struct {
	entitlement, remaining, overageCount float64
	unlimited                            bool
}

// parseQuotaDetail is parseQuotaDetail: entitlement, remaining,
// percent_remaining and unlimited are required.
func parseQuotaDetail(value gjson.Result) (quotaDetail, bool) {
	if !value.IsObject() {
		return quotaDetail{}, false
	}
	entitlement, okEntitlement := usageNumber(value.Get("entitlement"))
	remaining, okRemaining := usageNumber(value.Get("remaining"))
	_, okPercent := usageNumber(value.Get("percent_remaining"))
	unlimited := value.Get("unlimited")
	if !okEntitlement || !okRemaining || !okPercent || unlimited.Type != gjson.True && unlimited.Type != gjson.False {
		return quotaDetail{}, false
	}
	overage, _ := usageNumber(value.Get("overage_count"))
	return quotaDetail{entitlement: entitlement, remaining: remaining, overageCount: overage, unlimited: unlimited.Type == gjson.True}, true
}

// quotaLimit is buildLimitFromQuota.
func quotaLimit(key, label string, quota quotaDetail, plan string, window *UsageWindow, account string) UsageLimit {
	limit := UsageLimit{ID: "copilot:" + key, Label: label, Scope: UsageScope{Provider: Provider, AccountID: account, Tier: plan}, Window: window,
		Amount: UsageAmount{Unit: "requests"}}
	if window != nil {
		limit.Scope.WindowID = window.ID
	}
	if !quota.unlimited {
		used, total := math.Max(0, quota.entitlement-quota.remaining), quota.entitlement
		remaining := math.Max(0, total-used)
		limit.Amount.Used, limit.Amount.Limit, limit.Amount.Remaining = &used, &total, &remaining
		if total > 0 {
			usedFraction, remainingFraction := used/total, remaining/total
			limit.Amount.UsedFraction, limit.Amount.RemainingFraction = &usedFraction, &remainingFraction
		}
	}
	// deriveStatus.
	switch fraction := limit.Amount.RemainingFraction; {
	case quota.unlimited:
		limit.Status = "ok"
	case fraction == nil:
		limit.Status = "unknown"
	case *fraction <= 0:
		limit.Status = "exhausted"
	case *fraction <= 0.1:
		limit.Status = "warning"
	default:
		limit.Status = "ok"
	}
	if quota.unlimited {
		limit.Notes = append(limit.Notes, "Unlimited")
	}
	if quota.overageCount > 0 {
		limit.Notes = append(limit.Notes, "Overage requests: "+strconv.FormatFloat(quota.overageCount, 'f', -1, 64))
	}
	return limit
}

// usageNumber is toNumber: a finite number, or a numeric string.
func usageNumber(value gjson.Result) (float64, bool) {
	var number float64
	switch value.Type {
	case gjson.Number:
		number = value.Float()
	case gjson.String:
		text := strings.TrimSpace(value.String())
		if text == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

// parseUsageTimestamp is parseIsoTimestamp for the ISO forms GitHub reports:
// a date (UTC midnight) or an RFC 3339 instant.
func parseUsageTimestamp(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UnixMilli(), true
		}
	}
	return 0, false
}
