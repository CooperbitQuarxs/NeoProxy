package zai

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

// UsageURL is NeoPi's Z.ai quota endpoint (DEFAULT_ENDPOINT + QUOTA_PATH).
const UsageURL = "https://api.z.ai/api/monitor/usage/quota/limit"

// noCodingPlan is the message of an account without a GLM Coding Plan:
// nothing to meter, not a failure.
var noCodingPlan = regexp.MustCompile(`(?i)不存在\s*coding\s*plan|no\s+coding\s+plan`)

// Usage is NeoPi's Z.ai usage report (fetchZaiUsage).
type Usage struct {
	// Plan is the coding-plan tier ("lite", "pro", "max") when reported.
	Plan   string
	Limits []UsageLimit
}

// UsageLimit is one quota window.
type UsageLimit struct {
	// Kind is "tokens", "requests", "credits" or "features" (the Zread quota).
	Kind string
	// WindowID is buildZaiWindow's id: "5h", "1d", "1w", "1mo", ...
	WindowID   string
	DurationMs int64
	ResetsAt   time.Time
	// UsedFraction is nil when Z.ai reports neither a percentage nor absolutes.
	UsedFraction *float64
	// Shared limits gate every request on the account.
	Shared bool
}

// FetchUsage reads the account's quota windows with its API key. A nil report
// without error means the account has nothing NeoPi meters.
func FetchUsage(ctx context.Context, client *http.Client, key string) (*Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UsageURL, nil)
	if err != nil {
		return nil, err
	}
	// NeoPi sends the raw key, without a Bearer prefix, beside Bun's defaults.
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Authorization", key)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", openaiwire.NeoPiUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("z.ai usage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("z.ai usage: HTTP %d", resp.StatusCode)
	}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, fmt.Errorf("z.ai usage: %w", err)
	}
	defer func() { _ = decoded.Close() }()
	raw, err := io.ReadAll(io.LimitReader(decoded, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("z.ai usage: %w", err)
	}
	return ParseUsage(raw)
}

// ParseUsage is fetchZaiUsage's projection of a quota payload.
func ParseUsage(raw []byte) (*Usage, error) {
	if !gjson.ValidBytes(raw) {
		return nil, fmt.Errorf("z.ai usage: invalid JSON")
	}
	payload := gjson.ParseBytes(raw)
	if payload.Get("success").Type != gjson.True {
		if message := payload.Get("msg"); message.Type == gjson.String && noCodingPlan.MatchString(message.Str) {
			return nil, nil
		}
		return nil, fmt.Errorf("z.ai usage: response invalid (code %s)", payload.Get("code").Raw)
	}
	usage := &Usage{}
	if level := payload.Get("data.level"); level.Type == gjson.String {
		usage.Plan = level.Str
	}
	for _, item := range payload.Get("data.limits").Array() {
		limitType := item.Get("type")
		if !item.IsObject() || limitType.Type != gjson.String || limitType.Str == "" {
			continue
		}
		used, hasUsed := number(item.Get("currentValue"))
		limit, hasLimit := number(item.Get("usage"))
		percentage, hasPercentage := number(item.Get("percentage"))
		window := zaiWindow(item)
		var fraction *float64
		var kind string
		shared := true
		switch limitType.Str {
		case "TOKENS_LIMIT":
			kind = "tokens"
			fraction = usedFraction(used, hasUsed, limit, hasLimit, percentage, hasPercentage)
		case "TIME_LIMIT":
			kind = "requests"
			if zreadLimit(item) {
				kind, shared = "features", false
			}
			fraction = usedFraction(used, hasUsed, limit, hasLimit, percentage, hasPercentage)
		case "CREDIT_LIMIT":
			// `percentage` is server-rounded; prefer the exact ratio when absolutes exist.
			kind = "credits"
			absolute := hasUsed && hasLimit && limit > 0
			fraction = usedFraction(used, hasUsed, limit, hasLimit, percentage, hasPercentage && !absolute)
		default:
			continue
		}
		window.Kind, window.UsedFraction, window.Shared = kind, fraction, shared
		usage.Limits = append(usage.Limits, window)
	}
	if len(usage.Limits) == 0 {
		return nil, nil
	}
	return usage, nil
}

// usedFraction is buildUsageAmount: Z.ai's 0-100 percentage clamped to a
// fraction, else used/limit capped at 1.
func usedFraction(used float64, hasUsed bool, limit float64, hasLimit bool, percentage float64, hasPercentage bool) *float64 {
	if hasPercentage {
		value := math.Min(math.Max(percentage/100, 0), 1)
		return &value
	}
	if hasUsed && hasLimit && limit > 0 {
		value := math.Min(used/limit, 1)
		return &value
	}
	return nil
}

// zaiWindow is buildZaiWindow.
func zaiWindow(item gjson.Result) UsageLimit {
	count := 1.0
	if value, ok := number(item.Get("number")); ok && value > 0 {
		count = value
	}
	countText := strconv.FormatFloat(count, 'f', -1, 64)
	var window UsageLimit
	unit, hasUnit := number(item.Get("unit"))
	switch {
	case hasUnit && unit == 3:
		window.WindowID, window.DurationMs = countText+"h", int64(count*float64(time.Hour/time.Millisecond))
	case hasUnit && unit == 4:
		window.WindowID, window.DurationMs = countText+"d", int64(count*float64(24*time.Hour/time.Millisecond))
	case hasUnit && unit == 5:
		window.WindowID, window.DurationMs = countText+"mo", int64(count*float64(30*24*time.Hour/time.Millisecond))
	case hasUnit && unit == 6:
		window.WindowID, window.DurationMs = "1w", int64(7*24*time.Hour/time.Millisecond)
	case hasUnit:
		window.WindowID = countText + "u" + strconv.FormatFloat(unit, 'f', -1, 64)
	default:
		window.WindowID = "quota"
	}
	if reset, ok := number(item.Get("nextResetTime")); ok {
		if reset <= 1_000_000_000_000 {
			reset *= 1000
		}
		window.ResetsAt = time.UnixMilli(int64(reset))
	}
	return window
}

// zreadLimit is isZaiFeatureRequestLimit: the search/reader/zread tool quota.
func zreadLimit(item gjson.Result) bool {
	codes := map[string]bool{}
	for _, detail := range item.Get("usageDetails").Array() {
		if code := detail.Get("modelCode"); code.Type == gjson.String && code.Str != "" {
			codes[code.Str] = true
		}
	}
	return codes["search-prime"] && codes["web-reader"] && codes["zread"]
}

// number is toNumber: finite numbers and numeric strings.
func number(value gjson.Result) (float64, bool) {
	switch value.Type {
	case gjson.Number:
		return value.Num, !math.IsInf(value.Num, 0) && !math.IsNaN(value.Num)
	case gjson.String:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value.Str), 64)
		return parsed, err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
	}
	return 0, false
}
