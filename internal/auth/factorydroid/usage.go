package factorydroid

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

// Usage is NeoPi's Factory Droid usage report (parseFactoryDroidUsage over
// GET /api/billing/limits).
type Usage struct {
	Provider string       `json:"provider"`
	Limits   []UsageLimit `json:"limits"`
	Notes    []string     `json:"notes,omitempty"`
}

// UsageLimit is one pool window or the extra usage balance.
type UsageLimit struct {
	ID     string       `json:"id"`
	Label  string       `json:"label"`
	Scope  UsageScope   `json:"scope"`
	Window *UsageWindow `json:"window,omitempty"`
	Amount UsageAmount  `json:"amount"`
	// Status is usageStatus: ok, warning or exhausted.
	Status string `json:"status"`
}

// UsageScope names the provider and window a limit belongs to.
type UsageScope struct {
	Provider string `json:"provider"`
	WindowID string `json:"windowId,omitempty"`
}

// UsageWindow is a pool window; ResetsAt (epoch milliseconds) is set only
// while the window is active.
type UsageWindow struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	DurationMs float64  `json:"durationMs"`
	ResetsAt   *float64 `json:"resetsAt,omitempty"`
}

// UsageAmount is a limit's consumption, in percent for pool windows and USD
// for the extra balance.
type UsageAmount struct {
	Used              float64 `json:"used"`
	Limit             float64 `json:"limit"`
	Remaining         float64 `json:"remaining"`
	UsedFraction      float64 `json:"usedFraction"`
	RemainingFraction float64 `json:"remainingFraction"`
	Unit              string  `json:"unit"`
}

const (
	hourMs = 60 * 60 * 1000
	dayMs  = 24 * hourMs
)

var usageWindows = []struct {
	key, id, label string
	durationMs     float64
}{
	{"fiveHour", "5h", "5 Hour", 5 * hourMs},
	{"weekly", "weekly", "Weekly", 7 * dayMs},
	{"monthly", "monthly", "Monthly", 30 * dayMs},
}

var usagePools = []struct{ key, label string }{
	{"standard", "Standard credits"},
	{"core", "Droid Core"},
}

// usageStatus maps a used fraction to the standard quota thresholds.
func usageStatus(usedFraction float64) string {
	switch {
	case usedFraction >= 1:
		return "exhausted"
	case usedFraction >= 0.9:
		return "warning"
	}
	return "ok"
}

// ParseUsage is parseFactoryDroidUsage: per pool (Standard credits, Droid
// Core) and per window (5h, weekly, monthly) percent-used limits, plus the
// extra usage balance when funded. A window Factory left lapsed (its end in
// the past) reads as 0% used with no reset, as the droid CLI shows it. ok is
// false when the payload carries no usage.
func ParseUsage(payload []byte, fetchedAt time.Time) (*Usage, bool) {
	root := gjson.ParseBytes(payload)
	if !root.IsObject() {
		return nil, false
	}
	if billing := root.Get("usesTokenRateLimitsBilling"); billing.Type == gjson.False {
		return &Usage{Provider: Provider, Limits: []UsageLimit{},
			Notes: []string{"This Factory account does not use token-rate-limit billing; no quota windows are exposed."}}, true
	}
	limits := root.Get("limits")
	if !limits.IsObject() {
		return nil, false
	}
	now := float64(fetchedAt.UnixMilli())
	usage := &Usage{Provider: Provider, Limits: []UsageLimit{}}
	for _, pool := range usagePools {
		poolValue := limits.Get(pool.key)
		if !poolValue.IsObject() {
			continue
		}
		for _, def := range usageWindows {
			windowValue := poolValue.Get(def.key)
			if !windowValue.IsObject() {
				continue
			}
			usedPercent, ok := jsjson.ToNumber(windowValue.Get("usedPercent").Value())
			if !ok {
				continue
			}
			window := &UsageWindow{ID: pool.key + "-" + def.id, Label: pool.label + " " + def.label, DurationMs: def.durationMs}
			used := 0.0
			if end, isTime := parseISOTimestamp(windowValue.Get("windowEnd")); isTime && end >= now {
				window.ResetsAt = &end
				used = usedPercent
			}
			usedFraction := math.Min(1, math.Max(0, used/100))
			usage.Limits = append(usage.Limits, UsageLimit{
				ID:     "factory-droid:" + pool.key + ":" + def.id,
				Label:  pool.label + " " + def.label + " window",
				Scope:  UsageScope{Provider: Provider, WindowID: window.ID},
				Window: window,
				Amount: UsageAmount{Used: used, Limit: 100, Remaining: math.Max(0, 100-used), UsedFraction: usedFraction,
					RemainingFraction: math.Max(0, 1-usedFraction), Unit: "percent"},
				Status: usageStatus(usedFraction),
			})
		}
	}
	if cents, ok := jsjson.ToNumber(root.Get("extraUsageBalanceCents").Value()); ok && cents > 0 {
		balance := cents / 100
		usage.Limits = append(usage.Limits, UsageLimit{
			ID:     "factory-droid:extra-balance",
			Label:  "Extra usage balance",
			Scope:  UsageScope{Provider: Provider},
			Amount: UsageAmount{Used: 0, Limit: balance, Remaining: balance, UsedFraction: 0, RemainingFraction: 1, Unit: "usd"},
			Status: "ok",
		})
	}
	if len(usage.Limits) == 0 {
		return nil, false
	}
	return usage, true
}

// parseISOTimestamp is parseIsoTimestamp: a non-empty string Date.parse reads.
func parseISOTimestamp(value gjson.Result) (float64, bool) {
	if value.Type != gjson.String || value.String() == "" {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String())
	if err != nil {
		return 0, false
	}
	return float64(parsed.UnixMilli()), true
}

// FetchUsage is fetchFactoryDroidUsageReport: the account's billing limits
// on its residency host with droid's client identity. A 401 or 403 means the
// credential lost access (an *Error carrying the status); any other failure
// leaves quota unknown.
func FetchUsage(ctx context.Context, client *http.Client, credential *Credential, now time.Time) (*Usage, error) {
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return nil, errors.New("factory-droid usage: account has no access token")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBaseURL(credential.Region)+"/api/billing/limits", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	request.Header.Set("Connection", "keep-alive")
	request.Header.Set("User-Agent", BunUserAgent)
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential.AccessToken))
	for name, values := range ClientHeaders(credential.OrgID) {
		request.Header[name] = values
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, &Error{Status: response.StatusCode, Message: strings.TrimSpace(fmt.Sprintf("Factory Droid usage endpoint returned %d %s", response.StatusCode, http.StatusText(response.StatusCode)))}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("factory-droid usage: billing limits answered %d", response.StatusCode)
	}
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = decoded.Close() }()
	body, err := io.ReadAll(decoded)
	if err != nil {
		return nil, err
	}
	usage, ok := ParseUsage(body, now)
	if !ok {
		return nil, errors.New("factory-droid usage: billing limits carry no usage")
	}
	return usage, nil
}
