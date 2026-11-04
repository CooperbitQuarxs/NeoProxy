package anthropicnpi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
)

const UsageURL = "https://api.anthropic.com/api/oauth/usage"
const UsageBetas = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advanced-tool-use-2025-11-20,effort-2025-11-24,extended-cache-ttl-2025-04-11"

type UsageBucket struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

// Usage contains the shared subscription gates, not model-scoped or monetary
// limits: those must never be applied to all models as an account-wide cap.
type Usage struct {
	FiveHour *UsageBucket `json:"five_hour"`
	SevenDay *UsageBucket `json:"seven_day"`
}

// Usage projects NeoPi's subscription usage read into the shared 5h/week gates.
// A refused credential or transient failure never moves to another host. Only
// an absent endpoint permits the same canonical fallback as claude-api.ts.
func (c *Client) Usage(ctx context.Context, token, baseURL string) (*Usage, error) {
	return c.usage(ctx, token, baseURL, waitUsageRetry, time.Now)
}

func usageEndpoints(baseURL string) []string {
	endpoint := UsageURL
	if parsed, err := url.Parse(strings.TrimSpace(baseURL)); err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		path := strings.TrimRight(parsed.Path, "/")
		lower := strings.ToLower(path)
		for _, suffix := range []string{"/api/oauth", "/v1"} {
			if strings.HasSuffix(lower, suffix) {
				path = path[:len(path)-len(suffix)]
				break
			}
		}
		parsed.Path, parsed.RawPath, parsed.RawQuery, parsed.Fragment = path+"/api/oauth/usage", "", "", ""
		endpoint = parsed.String()
	}
	if endpoint == UsageURL {
		return []string{endpoint}
	}
	return []string{endpoint, UsageURL}
}

func usageHeaders(request *http.Request, token string) {
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Accept-Encoding", "gzip, compress, deflate, br")
	request.Header.Set("anthropic-beta", UsageBetas)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connection", "keep-alive")
	request.Header.Set("User-Agent", "claude-cli/"+WireVersion()+" (external, cli)")
	request.Header.Set("Authorization", "Bearer "+token)
}

func (c *Client) usage(ctx context.Context, token, baseURL string, wait func(context.Context, time.Duration) error, now func() time.Time) (*Usage, error) {
	var best *Usage
	for _, endpoint := range usageEndpoints(baseURL) {
		payload, absent, err := c.usagePayload(ctx, token, endpoint, wait, now)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if payload != nil {
			usage := ParseUsage(payload)
			if usage.FiveHour != nil || usage.SevenDay != nil {
				best = usage
			}
			if hasUsageData(payload) {
				return usage, nil
			}
		}
		if !absent {
			if best != nil {
				return best, nil
			}
			if err != nil {
				return nil, err
			}
			break
		}
	}
	if best != nil {
		return best, nil
	}
	return nil, errors.New("Claude usage response has no shared windows")
}

func (c *Client) usagePayload(ctx context.Context, token, endpoint string, wait func(context.Context, time.Duration) error, now func() time.Time) (map[string]any, bool, error) {
	var last map[string]any
	var lastErr error
	absent := false
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, false, errors.New("invalid Claude usage endpoint")
		}
		usageHeaders(request, token)
		response, err := c.HTTP.Do(request)
		retryAfter := ""
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			lastErr = errors.New("Claude usage endpoint unreachable")
		} else {
			status := response.StatusCode
			if status < 200 || status >= 300 {
				retryAfter = response.Header.Get("Retry-After")
				response.Body.Close()
				lastErr = fmt.Errorf("Claude usage endpoint returned HTTP %d", status)
				switch status {
				case 404, 405, 410, 501:
					return nil, true, lastErr
				}
				if status != 408 && status < 500 {
					// In particular 429 must wait for the next poll, not deepen the throttle.
					return nil, false, lastErr
				}
			} else {
				var value any
				err = decodeJSON(response, &value)
				response.Body.Close()
				if errors.Is(err, io.EOF) {
					return last, true, nil
				}
				if err != nil {
					lastErr = errors.New("Claude usage response is not valid JSON")
					if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "json") {
						return last, true, lastErr
					}
				} else if body, ok := value.(map[string]any); ok {
					last = body
					if hasUsageData(body) {
						return body, false, nil
					}
					absent = !looksLikeUsage(body)
				} else {
					absent = true
				}
			}
		}
		if attempt < 2 {
			if err = wait(ctx, usageRetryDelay(attempt, retryAfter, now())); err != nil {
				return nil, false, err
			}
		}
	}
	return last, absent, lastErr
}

func waitUsageRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var usageRetryNumber = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?`)

func usageRetryDelay(attempt int, retryAfter string, now time.Time) time.Duration {
	delay := 500 * time.Millisecond * time.Duration(1<<attempt)
	if seconds, err := strconv.ParseFloat(usageRetryNumber.FindString(strings.TrimSpace(retryAfter)), 64); err == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
		return max(delay, time.Duration(seconds*float64(time.Second)))
	}
	if at, err := http.ParseTime(retryAfter); err == nil {
		return max(delay, at.Sub(now))
	}
	return delay
}

func usageNumber(value any) *float64 {
	if number, ok := jsjson.ToNumber(value); ok {
		return &number
	}
	return nil
}

func parseUsageBucket(value any, percentKey string) *UsageBucket {
	body, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	bucket := &UsageBucket{Utilization: usageNumber(body[percentKey])}
	if text, ok := body["resets_at"].(string); ok {
		if reset, err := time.Parse(time.RFC3339Nano, text); err == nil {
			bucket.ResetsAt = reset.UTC().Format(time.RFC3339Nano)
		}
	}
	if bucket.Utilization == nil && bucket.ResetsAt == "" {
		return nil
	}
	return bucket
}

// ParseUsage follows NeoPi's legacy-bucket precedence and generic limits[]
// fallback. Null or missing buckets are unknown, never zero utilization.
func ParseUsage(body map[string]any) *Usage {
	usage := &Usage{FiveHour: parseUsageBucket(body["five_hour"], "utilization"), SevenDay: parseUsageBucket(body["seven_day"], "utilization")}
	limits, _ := body["limits"].([]any)
	for _, raw := range limits {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		bucket := parseUsageBucket(entry, "percent")
		switch entry["kind"] {
		case "session":
			if usage.FiveHour == nil {
				usage.FiveHour = bucket
			}
		case "weekly_all":
			if usage.SevenDay == nil {
				usage.SevenDay = bucket
			}
		}
	}
	return usage
}

func hasUsageData(body map[string]any) bool {
	for _, name := range []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet"} {
		if bucket := parseUsageBucket(body[name], "utilization"); bucket != nil && bucket.Utilization != nil {
			return true
		}
	}
	limits, _ := body["limits"].([]any)
	for _, raw := range limits {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, ok = entry["kind"].(string); !ok {
			continue
		}
		if bucket := parseUsageBucket(entry, "percent"); bucket != nil && bucket.Utilization != nil {
			return true
		}
	}
	return hasExtraUsage(body) || hasInlineResetInventory(body, time.Now())
}

func looksLikeUsage(body map[string]any) bool {
	for _, key := range []string{"five_hour", "seven_day", "limits", "extra_usage", "spend", "cedar_ember", "juniper_tide"} {
		if _, ok := body[key]; ok {
			return true
		}
	}
	return false
}
