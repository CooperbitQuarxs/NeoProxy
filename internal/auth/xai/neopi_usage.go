package xai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Billing endpoints of NeoPi's xaiOauthUsageProvider (usage/xai-oauth.ts):
// legacy weekly credits first, then the unified-billing monthly quota.
const (
	BillingCreditsURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	BillingMonthlyURL = "https://cli-chat-proxy.grok.com/v1/billing"
)

// WeeklyUsage is the legacy SuperGrok weekly credits shape.
type WeeklyUsage struct {
	PeriodStart, PeriodEnd time.Time
	CreditUsagePercent     float64
	// Inferred marks a fresh period that omitted creditUsagePercent.
	Inferred bool
}

// MonthlyUsage is the unified-billing monthly included quota.
type MonthlyUsage struct {
	PeriodStart, PeriodEnd time.Time
	Used, Limit            float64
}

// OnDemand is the account's on-demand cap and spend.
type OnDemand struct{ Cap, Used float64 }

// Usage is one SuperGrok usage report.
type Usage struct {
	Weekly  *WeeklyUsage
	Monthly *MonthlyUsage
	// MonthlyAdvisory marks a monthly counter over its limit while weekly
	// credits are active: NeoPi shows it but does not gate dispatch on it.
	MonthlyAdvisory bool
	OnDemand        *OnDemand
}

// OnDemandHeadroom reports whether spent credits keep serving on the
// on-demand cap, in which case NeoPi does not gate the credential.
func (u *Usage) OnDemandHeadroom() bool {
	return u.OnDemand != nil && u.OnDemand.Used < u.OnDemand.Cap
}

func parseTimestamp(value any) (time.Time, bool) {
	text, ok := value.(string)
	if !ok || text == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	return parsed, err == nil
}

// toNumber is pi-catalog toNumber: finite numbers and numeric strings.
func toNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	}
	return 0, false
}

func parsePercent(value any) (float64, bool) {
	percent, ok := toNumber(value)
	return percent, ok && percent >= 0 && percent <= 100
}

func parseAmount(value any) (float64, bool) {
	record, ok := value.(map[string]any)
	if !ok {
		return 0, false
	}
	amount, ok := toNumber(record["val"])
	return amount, ok && amount >= 0
}

func parseWeekly(raw map[string]any, now time.Time) *WeeklyUsage {
	period, ok := raw["currentPeriod"].(map[string]any)
	if !ok {
		return nil
	}
	start, okStart := parseTimestamp(period["start"])
	end, okEnd := parseTimestamp(period["end"])
	kind, _ := period["type"].(string)
	if !okStart || !okEnd || !end.After(start) || !strings.Contains(strings.ToUpper(kind), "WEEK") {
		return nil
	}
	weekly := &WeeklyUsage{PeriodStart: start, PeriodEnd: end}
	if value, present := raw["creditUsagePercent"]; !present || value == nil {
		if !end.After(now) {
			return nil
		}
		weekly.Inferred = true
	} else if percent, valid := parsePercent(value); valid {
		weekly.CreditUsagePercent = percent
	} else {
		return nil
	}
	if products, present := raw["productUsage"]; present {
		if _, isArray := products.([]any); !isArray {
			return nil
		}
	}
	return weekly
}

func parseMonthly(raw map[string]any) *MonthlyUsage {
	start, okStart := parseTimestamp(raw["billingPeriodStart"])
	end, okEnd := parseTimestamp(raw["billingPeriodEnd"])
	if !okStart || !okEnd || !end.After(start) {
		return nil
	}
	limit, okLimit := parseAmount(raw["monthlyLimit"])
	used, okUsed := parseAmount(raw["used"])
	if !okLimit || limit <= 0 || !okUsed {
		return nil
	}
	return &MonthlyUsage{PeriodStart: start, PeriodEnd: end, Used: used, Limit: limit}
}

func parseOnDemand(raw map[string]any) *OnDemand {
	limit, okCap := parseAmount(raw["onDemandCap"])
	used, okUsed := parseAmount(raw["onDemandUsed"])
	if !okCap || limit <= 0 || !okUsed {
		return nil
	}
	return &OnDemand{Cap: limit, Used: used}
}

// confirmsNoMonthlyQuota: a zero monthly limit, or the credits shape served
// from the default endpoint with an inferred weekly percentage.
func confirmsNoMonthlyQuota(raw map[string]any, now time.Time) bool {
	if limit, ok := parseAmount(raw["monthlyLimit"]); ok {
		return limit == 0
	}
	weekly := parseWeekly(raw, now)
	return weekly != nil && weekly.Inferred
}

// fetchBillingConfig GETs one billing URL with the Grok CLI token-auth gate;
// any failure is "no payload".
func fetchBillingConfig(ctx context.Context, client *http.Client, url, accessToken string) map[string]any {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", "Bun/1.3.14")
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return nil
	}
	defer closeBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(decoded)
	if err != nil {
		return nil
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return nil
	}
	config, _ := payload["config"].(map[string]any)
	return config
}

// FetchUsage is xaiOauthUsageProvider.fetchUsage for an OAuth access token.
func FetchUsage(ctx context.Context, client *http.Client, accessToken string, now time.Time) (*Usage, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("xai usage: no access token")
	}
	credits := fetchBillingConfig(ctx, client, BillingCreditsURL, accessToken)
	var weekly *WeeklyUsage
	unified := false
	if credits != nil {
		weekly = parseWeekly(credits, now)
		unified = credits["isUnifiedBillingUser"] == true
	}
	var monthlyConfig map[string]any
	var monthly *MonthlyUsage
	if weekly == nil || unified {
		monthlyConfig = fetchBillingConfig(ctx, client, BillingMonthlyURL, accessToken)
		if monthlyConfig != nil {
			monthly = parseMonthly(monthlyConfig)
		}
	}
	effectiveWeekly := weekly
	if weekly != nil && weekly.Inferred && unified {
		if monthly != nil || monthlyConfig == nil || !confirmsNoMonthlyQuota(monthlyConfig, now) {
			effectiveWeekly = nil
		}
	}
	advisory := weekly != nil && weekly.Inferred && unified && monthly != nil && monthly.Used >= monthly.Limit
	if effectiveWeekly == nil && monthly == nil {
		return nil, errors.New("xai usage: billing reported no usable window")
	}
	usage := &Usage{Weekly: effectiveWeekly, Monthly: monthly, MonthlyAdvisory: advisory}
	// The first shape carrying an on-demand cap wins, as NeoPi dedupes by id.
	if effectiveWeekly != nil && credits != nil {
		usage.OnDemand = parseOnDemand(credits)
	}
	if usage.OnDemand == nil && monthly != nil {
		usage.OnDemand = parseOnDemand(monthlyConfig)
	}
	return usage, nil
}
