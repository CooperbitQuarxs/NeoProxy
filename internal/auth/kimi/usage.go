package kimi

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

// UsageURL is NeoPi's Kimi Code usage endpoint (DEFAULT_BASE_URL + usages).
const UsageURL = APIBaseURL + "/v1/usages"

const (
	minuteMs = int64(time.Minute / time.Millisecond)
	hourMs   = int64(time.Hour / time.Millisecond)
	dayMs    = 24 * hourMs
)

// Usage is NeoPi's Kimi Code usage report (kimiUsageProvider.fetchUsage).
type Usage struct {
	Limits []UsageLimit
}

// UsageLimit is one reported limit, in the payload's order.
type UsageLimit struct {
	Label string
	// WindowID is the canonical window ("5h", "7d", "1d", ...), the aggregate
	// key ("limit_month_total"), "default", or "" when the row has no window.
	WindowID   string
	DurationMs int64
	ResetsAt   time.Time
	// UsedFraction is nil when the row reports no limit to measure against.
	UsedFraction *float64
}

// FetchUsage reads the account's usage with its access token and the Kimi CLI
// identity headers. A nil report without error is an account without a Kimi
// Code plan: nothing to meter.
func FetchUsage(ctx context.Context, client *http.Client, authDir, token string, now time.Time) (*Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UsageURL, nil)
	if err != nil {
		return nil, err
	}
	for name, values := range CommonHeaders(authDir) {
		req.Header[name] = values
	}
	// NeoPi's fetch adds Bun's defaults beside the identity headers.
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Connection", "keep-alive")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kimi usage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kimi usage: HTTP %d", resp.StatusCode)
	}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, fmt.Errorf("kimi usage: %w", err)
	}
	defer func() { _ = decoded.Close() }()
	raw, err := io.ReadAll(io.LimitReader(decoded, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("kimi usage: %w", err)
	}
	return ParseUsage(raw, now)
}

// ParseUsage is parseUsagePayload plus toUsageLimit's window and amount.
func ParseUsage(raw []byte, now time.Time) (*Usage, error) {
	if !gjson.ValidBytes(raw) {
		return nil, fmt.Errorf("kimi usage: invalid JSON")
	}
	payload := gjson.ParseBytes(raw)
	if !payload.IsObject() {
		return nil, fmt.Errorf("kimi usage: response invalid")
	}
	nowMs := now.UnixMilli()
	var rows []usageRow
	if usage := payload.Get("usage"); usage.IsObject() {
		if row, ok := buildUsageRow(usage, "Weekly limit", nowMs); ok {
			// The aggregate quota resets weekly but reports no duration.
			row.window = &usageWindow{id: "7d", resetsAt: row.resetsAt}
			rows = append(rows, row)
		}
	}
	if total := payload.Get("totalQuota"); total.IsObject() {
		if row, ok := buildUsageRow(total, "Total quota", nowMs); ok {
			row.window = buildWindow(objectOrEmpty(total.Get("window")), nowMs)
			rows = append(rows, row)
		}
	}
	if limits := payload.Get("limits"); limits.IsArray() {
		for idx, item := range limits.Array() {
			if !item.IsObject() {
				continue
			}
			detail := item
			if d := item.Get("detail"); d.IsObject() {
				detail = d
			}
			windowData := objectOrEmpty(item.Get("window"))
			label := firstString(item.Get("name"), item.Get("title"), item.Get("scope"), detail.Get("name"), detail.Get("title"))
			if label == "" {
				duration, _ := number(windowData.Get("duration"))
				label = formatDurationLabel(duration, jsString(windowData.Get("timeUnit")))
			}
			if label == "" {
				label = "Limit #" + strconv.Itoa(idx+1)
			}
			if row, ok := buildUsageRow(detail, label, nowMs); ok {
				row.window = buildWindow(windowData, nowMs)
				rows = append(rows, row)
			}
		}
	}
	if usages := payload.Get("usages"); usages.IsObject() {
		usages.ForEach(func(key, aggregate gjson.Result) bool {
			if key.Str == "limit_5h" || !aggregate.IsObject() {
				return true
			}
			if row, ok := buildAggregateUsageRow(key.Str, aggregate, nowMs); ok {
				rows = append(rows, row)
			}
			return true
		})
	}
	if len(rows) == 0 {
		// `{}` is Kimi's answer for an account without a Kimi Code plan.
		if len(payload.Map()) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("kimi usage: response invalid")
	}
	usage := &Usage{Limits: make([]UsageLimit, 0, len(rows))}
	for _, row := range rows {
		usage.Limits = append(usage.Limits, row.limit())
	}
	return usage, nil
}

type usageWindow struct {
	id         string
	durationMs int64
	resetsAt   int64
}

type usageRow struct {
	label                       string
	used, limitValue, remaining *float64
	resetsAt                    int64
	window                      *usageWindow
}

// limit is toUsageLimit: the window (falling back to the row's reset) and
// buildUsageAmount's used fraction.
func (row usageRow) limit() UsageLimit {
	out := UsageLimit{Label: row.label}
	window := row.window
	if window != nil && window.resetsAt == 0 && row.resetsAt != 0 {
		copied := *window
		copied.resetsAt = row.resetsAt
		window = &copied
	}
	if window == nil && row.resetsAt != 0 {
		window = &usageWindow{id: "default", resetsAt: row.resetsAt}
	}
	if window != nil {
		out.WindowID, out.DurationMs = window.id, window.durationMs
		if window.resetsAt != 0 {
			out.ResetsAt = time.UnixMilli(window.resetsAt)
		}
	}
	if row.limitValue != nil && row.used != nil && *row.limitValue > 0 {
		fraction := math.Min(math.Max(*row.used / *row.limitValue, 0), 1)
		out.UsedFraction = &fraction
	}
	return out
}

func buildUsageRow(data gjson.Result, defaultLabel string, nowMs int64) (usageRow, bool) {
	limit := optionalNumber(data.Get("limit"))
	used := optionalNumber(data.Get("used"))
	remaining := optionalNumber(data.Get("remaining"))
	if used == nil && remaining != nil && limit != nil {
		value := *limit - *remaining
		used = &value
	}
	if used == nil && limit == nil {
		return usageRow{}, false
	}
	label := firstString(data.Get("name"), data.Get("title"))
	if label == "" {
		label = defaultLabel
	}
	return usageRow{label: label, used: used, limitValue: limit, remaining: remaining, resetsAt: parseResetTime(data, nowMs)}, true
}

func buildAggregateUsageRow(key string, data gjson.Result, nowMs int64) (usageRow, bool) {
	ratio, ok := number(data.Get("used_ratio"))
	if !ok {
		return usageRow{}, false
	}
	fraction := math.Min(math.Max(ratio, 0), 1)
	label := key
	switch key {
	case "limit_month_total":
		label = "Monthly total"
	case "limit_month_code":
		label = "Monthly code"
	}
	used, limit, remaining := fraction*100, 100.0, (1-fraction)*100
	resetsAt := parseResetTime(data, nowMs)
	return usageRow{label: label, used: &used, limitValue: &limit, remaining: &remaining, resetsAt: resetsAt, window: &usageWindow{id: key, resetsAt: resetsAt}}, true
}

func buildWindow(data gjson.Result, nowMs int64) *usageWindow {
	duration, hasDuration := number(data.Get("duration"))
	timeUnit := jsString(data.Get("timeUnit"))
	label := ""
	if hasDuration && timeUnit != "" {
		label = formatDurationLabel(duration, timeUnit)
	}
	resetsAt := parseResetTime(data, nowMs)
	if !hasDuration && label == "" && resetsAt == 0 {
		return nil
	}
	window := &usageWindow{id: "default", resetsAt: resetsAt}
	if hasDuration {
		upper := strings.ToUpper(timeUnit)
		switch {
		case strings.Contains(upper, "MINUTE"):
			window.durationMs = int64(duration * float64(minuteMs))
		case strings.Contains(upper, "HOUR"):
			window.durationMs = int64(duration * float64(hourMs))
		case strings.Contains(upper, "DAY"):
			window.durationMs = int64(duration * float64(dayMs))
		case strings.Contains(upper, "WEEK"):
			window.durationMs = int64(duration * 7 * float64(dayMs))
		case strings.Contains(upper, "SECOND"):
			window.durationMs = int64(duration * 1000)
		default:
			return window
		}
		window.id = canonicalWindowID(window.durationMs)
	}
	return window
}

// canonicalWindowID names a span the way status lines and ranking match it.
func canonicalWindowID(durationMs int64) string {
	switch {
	case durationMs > 0 && durationMs%dayMs == 0:
		return strconv.FormatInt(durationMs/dayMs, 10) + "d"
	case durationMs > 0 && durationMs%hourMs == 0:
		return strconv.FormatInt(durationMs/hourMs, 10) + "h"
	}
	if minutes := int64(math.Round(float64(durationMs) / float64(minuteMs))); minutes > 0 {
		return strconv.FormatInt(minutes, 10) + "m"
	}
	return "default"
}

func formatDurationLabel(duration float64, timeUnit string) string {
	upper := strings.ToUpper(timeUnit)
	text := strconv.FormatFloat(duration, 'f', -1, 64)
	switch {
	case strings.Contains(upper, "MINUTE"):
		if duration >= 60 && math.Mod(duration, 60) == 0 {
			return strconv.FormatFloat(duration/60, 'f', -1, 64) + "h limit"
		}
		return text + "m limit"
	case strings.Contains(upper, "HOUR"):
		return text + "h limit"
	case strings.Contains(upper, "DAY"):
		return text + "d limit"
	case strings.Contains(upper, "SECOND"):
		return text + "s limit"
	}
	return ""
}

// parseResetTime reads an absolute reset (ISO string, epoch seconds or
// milliseconds) or a relative one in seconds; 0 when absent.
func parseResetTime(data gjson.Result, nowMs int64) int64 {
	for _, key := range []string{"reset_at", "resetAt", "reset_time", "resetTime"} {
		value := data.Get(key)
		if value.Type == gjson.String && strings.TrimSpace(value.Str) != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value.Str)); err == nil {
				return parsed.UnixMilli()
			}
		}
		if value.Type == gjson.Number && !math.IsInf(value.Num, 0) && !math.IsNaN(value.Num) {
			if value.Num > 1_000_000_000_000 {
				return int64(value.Num)
			}
			return int64(value.Num * 1000)
		}
	}
	for _, key := range []string{"reset_in", "resetIn", "ttl", "window"} {
		if seconds, ok := number(data.Get(key)); ok {
			return nowMs + int64(seconds*1000)
		}
	}
	return 0
}

func objectOrEmpty(value gjson.Result) gjson.Result {
	if value.IsObject() {
		return value
	}
	return gjson.Parse("{}")
}

func firstString(values ...gjson.Result) string {
	for _, value := range values {
		if value.Type == gjson.String && value.Str != "" {
			return value.Str
		}
	}
	return ""
}

// jsString is String(value || "") for the string-typed fields NeoPi reads.
func jsString(value gjson.Result) string {
	if value.Type == gjson.String {
		return value.Str
	}
	return ""
}

func optionalNumber(value gjson.Result) *float64 {
	if n, ok := number(value); ok {
		return &n
	}
	return nil
}

// number is toNumber: finite numbers and numeric strings.
func number(value gjson.Result) (float64, bool) {
	switch value.Type {
	case gjson.Number:
		return value.Num, !math.IsInf(value.Num, 0) && !math.IsNaN(value.Num)
	case gjson.String:
		trimmed := strings.TrimSpace(value.Str)
		if trimmed == "" {
			return 0, false
		}
		n, err := strconv.ParseFloat(trimmed, 64)
		return n, err == nil && !math.IsInf(n, 0) && !math.IsNaN(n)
	}
	return 0, false
}
