package umans

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

// usagePath is USAGE_PATH.
const usagePath = "/v1/usage"

// Usage is NeoPi's Umans usage report (fetchUmansUsage).
type Usage struct {
	// Plan is the plan's display name when reported.
	Plan   string
	Limits []UsageLimit
	// Deprioritized is usage.priority.low: requests are deprioritized after a
	// rate-limit burst.
	Deprioritized bool
}

// UsageLimit is one usage row: umans:requests (single row), the
// umans:requests:soft / umans:requests:hard pair, or umans:concurrency.
type UsageLimit struct {
	ID string
	// WindowID is "5h" for request rows and "concurrency" for the
	// instantaneous concurrency row.
	WindowID   string
	DurationMs int64
	// ResetsAt is the rolling window epoch's resets_at, zero when absent.
	ResetsAt     time.Time
	Used         *float64
	Limit        *float64
	UsedFraction *float64
	// Status is NeoPi's row status: ok, warning, exhausted, or "" when it
	// has no fraction. A soft-cap row never reaches exhausted.
	Status string
}

// UsageError is a rejected key (401/403): NeoPi throws so the credential is
// flagged bad rather than unknown.
type UsageError struct{ Status int }

func (e *UsageError) Error() string {
	return fmt.Sprintf("Umans usage endpoint returned %d %s", e.Status, http.StatusText(e.Status))
}

// UsageBaseURL is normalizeBaseUrl: the account's base without trailing
// slashes or /v1, any path prefix kept, else the default endpoint.
func UsageBaseURL(base string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	if len(trimmed) >= 3 && strings.EqualFold(trimmed[len(trimmed)-3:], "/v1") {
		trimmed = trimmed[:len(trimmed)-3]
	}
	if trimmed == "" {
		return BaseURL
	}
	return trimmed
}

// FetchUsage reads the key's GET /v1/usage. A nil report without error means
// Umans reported nothing NeoPi meters (or answered a transient failure).
func FetchUsage(ctx context.Context, client *http.Client, base, key string) (*Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UsageBaseURL(base)+usagePath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", openaiwire.NeoPiUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("umans usage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The request advertises Bun's encodings, so Go's transport leaves the
	// body compressed; fetch's response.json() decodes it.
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, fmt.Errorf("umans usage: %w", err)
	}
	defer func() { _ = decoded.Close() }()
	data, err := io.ReadAll(io.LimitReader(decoded, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("umans usage: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, &UsageError{Status: resp.StatusCode}
		}
		return nil, nil
	}
	payload := gjson.ParseBytes(data)
	if !payload.IsObject() {
		return nil, nil
	}
	limits := requestLimits(payload)
	if concurrency := concurrencyLimit(payload); concurrency != nil {
		limits = append(limits, *concurrency)
	}
	if len(limits) == 0 {
		return nil, nil
	}
	return &Usage{Plan: payload.Get("plan.display_name").String(), Limits: limits, Deprioritized: payload.Get("usage.priority.low").Type == gjson.True}, nil
}

// finite is toFiniteNumber: a JSON number, else nil.
func finite(value gjson.Result) *float64 {
	if value.Type != gjson.Number {
		return nil
	}
	number := value.Float()
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return nil
	}
	return &number
}

// amount is buildUsageAmount's usedFraction: used/limit capped at 1.
func amount(used, limit *float64) *float64 {
	if used == nil || limit == nil || *limit <= 0 {
		return nil
	}
	fraction := math.Min(*used / *limit, 1)
	return &fraction
}

// usageStatus is usageStatus; softStatus is softCapStatus.
func usageStatus(fraction *float64) string {
	switch {
	case fraction == nil:
		return ""
	case *fraction >= 1:
		return "exhausted"
	case *fraction >= 0.9:
		return "warning"
	}
	return "ok"
}

func softStatus(fraction *float64) string {
	switch {
	case fraction == nil:
		return ""
	case *fraction >= 0.9:
		return "warning"
	}
	return "ok"
}

// requestLimits is buildRequestsLimits: weighted counters drive the soft cap,
// raw counters the burst ceiling; without a burst ceiling (or weighted
// counters) one row carries the authoritative counter and can exhaust.
func requestLimits(payload gjson.Result) []UsageLimit {
	limit := finite(payload.Get("limits.requests.limit"))
	hardCap := finite(payload.Get("limits.requests.hard_cap"))
	windowSeconds := finite(payload.Get("limits.requests.window_seconds"))
	rawUsed := finite(payload.Get("usage.requests_in_window"))
	weightedUsed := finite(payload.Get("usage.weighted_in_window"))
	if limit == nil && rawUsed == nil && weightedUsed == nil {
		return nil
	}
	durationMs := int64(5 * time.Hour / time.Millisecond)
	if windowSeconds != nil && *windowSeconds != 0 {
		durationMs = int64(*windowSeconds * 1000)
	}
	var resetsAt time.Time
	if raw := payload.Get("window.resets_at").String(); raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			resetsAt = parsed
		}
	}
	row := func(id string, used, limit *float64, soft bool) UsageLimit {
		fraction := amount(used, limit)
		status := usageStatus(fraction)
		if soft {
			status = softStatus(fraction)
		}
		return UsageLimit{ID: id, WindowID: "5h", DurationMs: durationMs, ResetsAt: resetsAt, Used: used, Limit: limit, UsedFraction: fraction, Status: status}
	}
	if weightedUsed == nil || hardCap == nil {
		used := rawUsed
		if weightedUsed != nil {
			used = weightedUsed
		}
		return []UsageLimit{row("umans:requests", used, limit, false)}
	}
	limits := []UsageLimit{row("umans:requests:soft", weightedUsed, limit, true)}
	if rawUsed != nil {
		limits = append(limits, row("umans:requests:hard", rawUsed, hardCap, false))
	}
	return limits
}

// concurrencyLimit is buildConcurrencyLimit: instantaneous, not windowed.
func concurrencyLimit(payload gjson.Result) *UsageLimit {
	limit := finite(payload.Get("limits.concurrency.limit"))
	used := finite(payload.Get("usage.concurrent_sessions"))
	if limit == nil && used == nil {
		return nil
	}
	fraction := amount(used, limit)
	return &UsageLimit{ID: "umans:concurrency", WindowID: "concurrency", Used: used, Limit: limit, UsedFraction: fraction, Status: usageStatus(fraction)}
}
