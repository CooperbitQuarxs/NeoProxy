package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
)

// NeoPiUserAgent is pi-utils USER_AGENT (`${APP_NAME}/${VERSION}`) of the
// NeoPi build this port follows.
const NeoPiUserAgent = "npi/18.10.3"

// DefaultBackendURL is pi-catalog CODEX_BASE_URL.
const DefaultBackendURL = "https://chatgpt.com/backend-api"

const usageMaxBody = 1 << 20

// UsageWindow is one /wham/usage window (ParsedUsageWindow). Nil fields were
// absent or not numeric.
type UsageWindow struct {
	UsedPercent        *float64
	LimitWindowSeconds *float64
	ResetAfterSeconds  *float64
	ResetAt            *float64
}

// ResetTime is resolveResetTime: reset_at in seconds or milliseconds, else
// reset_after_seconds from now.
func (w *UsageWindow) ResetTime(now time.Time) (time.Time, bool) {
	if w.ResetAt != nil {
		ms := *w.ResetAt
		if ms <= 1_000_000_000_000 {
			ms *= 1000
		}
		if !math.IsInf(ms, 0) {
			return time.UnixMilli(int64(ms)), true
		}
	}
	if w.ResetAfterSeconds != nil {
		return now.Add(time.Duration(*w.ResetAfterSeconds * float64(time.Second))), true
	}
	return time.Time{}, false
}

// UsageMeter is a separately metered allowance from additional_rate_limits,
// such as Spark. It never gates other models.
type UsageMeter struct {
	LimitName      string
	MeteredFeature string
	Allowed        *bool
	LimitReached   *bool
	Primary        *UsageWindow
	Secondary      *UsageWindow
}

// Usage is NeoPi's ParsedUsage of GET /wham/usage.
type Usage struct {
	PlanType     string
	Allowed      *bool
	LimitReached *bool
	Primary      *UsageWindow
	Secondary    *UsageWindow
	Additional   []UsageMeter
	// CreditOverage is true when paid credits still fund requests after the
	// plan windows report limit_reached; the account keeps serving.
	CreditOverage bool
}

// UsageURL is the wham/usage endpoint for a credential's base URL
// (normalizeCodexBaseUrl): only the canonical ChatGPT origins are honoured,
// since streaming proxies do not serve the account API.
func UsageURL(baseURL string) string {
	base := DefaultBackendURL
	if trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/"); trimmed != "" {
		if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" {
			if host := strings.ToLower(parsed.Host); host == "chatgpt.com" || host == "chat.openai.com" {
				base = parsed.Scheme + "://" + parsed.Host + "/backend-api"
			}
		}
	}
	return base + "/wham/usage"
}

// FetchUsage reads the account's usage like openaiCodexUsageProvider. A nil
// result with no error means the payload carried no usage.
func FetchUsage(ctx context.Context, client *http.Client, endpoint, token, accountID string) (*Usage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid usage request")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", NeoPiUserAgent)
	if accountID != "" {
		request.Header.Set("ChatGPT-Account-Id", accountID)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("usage endpoint unreachable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("usage endpoint returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, usageMaxBody+1))
	if err != nil || len(data) > usageMaxBody {
		return nil, fmt.Errorf("usage response unreadable or too large")
	}
	var payload any
	if err = json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("invalid usage response")
	}
	return ParseUsage(payload), nil
}

// ParseUsage is parseUsagePayload.
func ParseUsage(payload any) *Usage {
	body, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	rateLimit, hasRateLimit := body["rate_limit"].(map[string]any)
	var additional []UsageMeter
	if raw, ok := body["additional_rate_limits"].([]any); ok {
		for _, item := range raw {
			if meter, ok := parseUsageMeter(item); ok {
				additional = append(additional, meter)
			}
		}
	}
	if !hasRateLimit && len(additional) == 0 {
		return nil
	}
	usage := &Usage{Additional: additional}
	usage.PlanType, _ = body["plan_type"].(string)
	if hasRateLimit {
		usage.Allowed = jsBool(rateLimit["allowed"])
		usage.LimitReached = jsBool(rateLimit["limit_reached"])
		usage.Primary = parseUsageWindow(rateLimit["primary_window"])
		usage.Secondary = parseUsageWindow(rateLimit["secondary_window"])
	}
	usage.CreditOverage = hasPlanCreditOverage(body, usage.LimitReached)
	if usage.Primary == nil && usage.Secondary == nil && usage.Allowed == nil && usage.LimitReached == nil && len(usage.Additional) == 0 {
		return nil
	}
	return usage
}

func parseUsageWindow(value any) *UsageWindow {
	body, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	window := &UsageWindow{
		UsedPercent:        jsNumber(body["used_percent"]),
		LimitWindowSeconds: jsNumber(body["limit_window_seconds"]),
		ResetAfterSeconds:  jsNumber(body["reset_after_seconds"]),
		ResetAt:            jsNumber(body["reset_at"]),
	}
	if window.UsedPercent == nil && window.LimitWindowSeconds == nil && window.ResetAfterSeconds == nil && window.ResetAt == nil {
		return nil
	}
	return window
}

func parseUsageMeter(value any) (UsageMeter, bool) {
	body, ok := value.(map[string]any)
	if !ok {
		return UsageMeter{}, false
	}
	rateLimit, ok := body["rate_limit"].(map[string]any)
	if !ok {
		return UsageMeter{}, false
	}
	meter := UsageMeter{
		Allowed:      jsBool(rateLimit["allowed"]),
		LimitReached: jsBool(rateLimit["limit_reached"]),
		Primary:      parseUsageWindow(rateLimit["primary_window"]),
		Secondary:    parseUsageWindow(rateLimit["secondary_window"]),
	}
	meter.LimitName, _ = body["limit_name"].(string)
	meter.MeteredFeature, _ = body["metered_feature"].(string)
	if meter.Primary == nil && meter.Secondary == nil && meter.Allowed == nil && meter.LimitReached == nil {
		return UsageMeter{}, false
	}
	return meter, true
}

// hasPlanCreditOverage: credits fund overage only of a spent plan window,
// unless the overage limit or the spend control was reached.
func hasPlanCreditOverage(body map[string]any, planLimitReached *bool) bool {
	if planLimitReached == nil || !*planLimitReached {
		return false
	}
	credits, ok := body["credits"].(map[string]any)
	if !ok {
		return false
	}
	if credits["unlimited"] != true && credits["has_credits"] != true {
		return false
	}
	if credits["overage_limit_reached"] == true {
		return false
	}
	spendControl, _ := body["spend_control"].(map[string]any)
	return spendControl == nil || spendControl["reached"] != true
}

func jsNumber(value any) *float64 {
	if number, ok := jsjson.ToNumber(value); ok {
		return &number
	}
	return nil
}

func jsBool(value any) *bool {
	if flag, ok := value.(bool); ok {
		return &flag
	}
	return nil
}
