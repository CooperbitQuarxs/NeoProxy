// Package neopiretry ports NeoPi's fetchWithRetry (packages/utils/src/fetch-retry.ts):
// bounded HTTP retries on transient statuses and network errors, honouring the
// server's retry hints up to a cap.
package neopiretry

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	quotaResetPattern      = regexp.MustCompile(`(?i)reset after (?:(\d+)h)?(?:(\d+)m)?(\d+(?:\.\d+)?)s`)
	pleaseRetryPattern     = regexp.MustCompile(`(?i)Please retry in ([0-9.]+)(ms|s)`)
	retryDelayFieldPattern = regexp.MustCompile(`(?i)"retryDelay":\s*"([0-9.]+)(ms|s)"`)
	tryAgainPattern        = regexp.MustCompile(`(?i)try again in\s+~?\s*([0-9.]+)\s*(ms|sec|s|minutes?|mins?|m|hours?|hrs?|h)\b`)
	willResetInPattern     = regexp.MustCompile(`(?i)(?:will\s+)?resets?\s+in\s+~?\s*([0-9.]+)\s*(ms|sec|s|minutes?|mins?|m|hours?|hrs?|h|days?|d)\b`)
	resetInHrMinPattern    = regexp.MustCompile(`(?i)resets?\s+in\s+~?\s*(\d+(?:\.\d+)?)\s*hr\s*(\d+(?:\.\d+)?)\s*min\b`)
	willResetAtPattern     = regexp.MustCompile(`(?i)(?:will\s+)?reset at\s+([0-9]{4}-[0-9]{2}-[0-9]{2}[ T][0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:?[0-9]{2})?)`)
	cnResetAtPattern       = regexp.MustCompile(`将在\s*([0-9]{4}-[0-9]{2}-[0-9]{2}\s+[0-9]{2}:[0-9]{2}:[0-9]{2})\s*重置`)
	retryAfterMsBody       = regexp.MustCompile(`(?i)\bretry-after-ms\s*[:=]\s*([0-9]+)\b`)
	retryAfterBody         = regexp.MustCompile(`(?i)retry-after\s*[:=]\s*([^\s,;]+)`)
	resetMsBody            = regexp.MustCompile(`(?i)x-ratelimit-reset-ms\s*[:=]\s*(\d+)`)
	resetBody              = regexp.MustCompile(`(?i)x-ratelimit-reset\s*[:=]\s*(\d+)`)
	offsetSuffix           = regexp.MustCompile(`(?i)(?:Z|[+-][0-9]{2}:?[0-9]{2})$`)
)

// jsNumber is JavaScript Number(text): whitespace-trimmed, empty is 0.
func jsNumber(text string) (float64, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, true
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || strings.ContainsAny(text, "_xXpP") {
		return 0, false
	}
	return value, !math.IsNaN(value)
}

// parseLeadingInt is Number.parseInt(text, 10).
func parseLeadingInt(text string) (int64, bool) {
	text = strings.TrimSpace(text)
	end := 0
	if end < len(text) && (text[end] == '-' || text[end] == '+') {
		end++
	}
	start := end
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == start {
		return 0, false
	}
	value, err := strconv.ParseInt(text[:end], 10, 64)
	return value, err == nil
}

// parseDate is Date.parse for the ISO-8601 and HTTP-date shapes servers send.
func parseDate(text string) (time.Time, bool) {
	if t, err := http.ParseTime(text); err == nil {
		return t, true
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999-0700", "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, text); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func unitToMs(unit string) (float64, bool) {
	switch strings.ToLower(unit) {
	case "ms":
		return 1, true
	case "s", "sec":
		return 1000, true
	case "m", "min", "mins", "minute", "minutes":
		return 60_000, true
	case "h", "hr", "hrs", "hour", "hours":
		return 3_600_000, true
	case "d", "day", "days":
		return 86_400_000, true
	}
	return 0, false
}

func ms(now, target time.Time) float64 { return float64(target.Sub(now)) / float64(time.Millisecond) }

// ExtractRetryHint is NeoPi's extractRetryHint: the server-suggested delay in
// milliseconds from response headers, else from the error body. ok is false
// when no signal is present; 0 means retry immediately.
func ExtractRetryHint(header http.Header, body string, now time.Time) (float64, bool) {
	if header != nil {
		if raw := header.Get("retry-after-ms"); raw != "" {
			if value, ok := jsNumber(raw); ok && !math.IsInf(value, 0) && value >= 0 {
				return value, true
			}
		}
		if raw := header.Get("retry-after"); raw != "" {
			if seconds, ok := jsNumber(raw); ok && !math.IsInf(seconds, 0) {
				return math.Max(0, seconds*1000), true
			}
			if date, ok := parseDate(raw); ok {
				return math.Max(0, ms(now, date)), true
			}
		}
		if raw := header.Get("x-ratelimit-reset-ms"); raw != "" {
			if value, ok := jsNumber(raw); ok && !math.IsInf(value, 0) && value > 0 {
				switch {
				case value > 1e12:
					if delta := value - float64(now.UnixMilli()); delta > 0 {
						return delta, true
					}
				case value > 1e9:
					if delta := value*1000 - float64(now.UnixMilli()); delta > 0 {
						return delta, true
					}
				default:
					return value, true
				}
			}
		}
		if raw := header.Get("x-ratelimit-reset"); raw != "" {
			if seconds, ok := parseLeadingInt(raw); ok {
				if delta := float64(seconds*1000 - now.UnixMilli()); delta > 0 {
					return delta, true
				}
			}
		}
		if raw := header.Get("x-ratelimit-reset-after"); raw != "" {
			if seconds, ok := jsNumber(raw); ok && !math.IsInf(seconds, 0) && seconds > 0 {
				return seconds * 1000, true
			}
		}
	}
	if body == "" {
		return 0, false
	}

	longest, haveLongest := 0.0, false
	naive, haveNaive := 0.0, false
	retryNow := false
	consider := func(value float64) {
		if value > 0 && (!haveLongest || value > longest) {
			longest, haveLongest = value, true
		}
	}
	considerClamped := func(value float64) {
		if value > 0 {
			consider(value)
		} else {
			retryNow = true
		}
	}

	if match := quotaResetPattern.FindStringSubmatch(body); match != nil {
		hours, _ := strconv.ParseFloat(orZero(match[1]), 64)
		minutes, _ := strconv.ParseFloat(orZero(match[2]), 64)
		if seconds, err := strconv.ParseFloat(match[3], 64); err == nil {
			consider(((hours*60+minutes)*60 + seconds) * 1000)
		}
	}
	for _, pattern := range []*regexp.Regexp{willResetAtPattern, cnResetAtPattern} {
		match := pattern.FindStringSubmatch(body)
		if match == nil || match[1] == "" {
			continue
		}
		normalized := strings.Replace(match[1], " ", "T", 1)
		hasOffset := offsetSuffix.MatchString(normalized)
		if !hasOffset {
			normalized += "Z"
		}
		if date, ok := parseDate(normalized); ok && date.After(now) {
			if hasOffset {
				consider(ms(now, date))
			} else if delta := ms(now, date); delta > 0 && (!haveNaive || delta > naive) {
				naive, haveNaive = delta, true
			}
		}
	}
	if match := resetInHrMinPattern.FindStringSubmatch(body); match != nil {
		hours, errH := strconv.ParseFloat(match[1], 64)
		minutes, errM := strconv.ParseFloat(match[2], 64)
		if errH == nil && errM == nil && hours >= 0 && minutes > 0 {
			consider(hours*3_600_000 + minutes*60_000)
		}
	}
	if match := willResetInPattern.FindStringSubmatch(body); match != nil {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil && value > 0 {
			if unit, ok := unitToMs(match[2]); ok {
				consider(value * unit)
			}
		}
	}
	if match := retryAfterMsBody.FindStringSubmatch(body); match != nil {
		if value, ok := jsNumber(match[1]); ok {
			considerClamped(value)
		}
	}
	for _, pattern := range []*regexp.Regexp{pleaseRetryPattern, retryDelayFieldPattern, tryAgainPattern} {
		if match := pattern.FindStringSubmatch(body); match != nil {
			if value, err := strconv.ParseFloat(match[1], 64); err == nil && value > 0 {
				if unit, ok := unitToMs(match[2]); ok {
					consider(value * unit)
				}
			}
		}
	}
	if match := retryAfterBody.FindStringSubmatch(body); match != nil {
		if seconds, ok := jsNumber(match[1]); ok && !math.IsInf(seconds, 0) {
			considerClamped(seconds * 1000)
		} else if date, ok := parseDate(match[1]); ok {
			considerClamped(ms(now, date))
		}
	}
	if match := resetMsBody.FindStringSubmatch(body); match != nil {
		if value, err := strconv.ParseFloat(match[1], 64); err == nil {
			if value > 1e12 {
				value -= float64(now.UnixMilli())
			}
			considerClamped(value)
		}
	}
	if match := resetBody.FindStringSubmatch(body); match != nil {
		if seconds, err := strconv.ParseFloat(match[1], 64); err == nil {
			if seconds > 1e9 {
				considerClamped(seconds*1000 - float64(now.UnixMilli()))
			} else {
				considerClamped(seconds * 1000)
			}
		}
	}
	switch {
	case haveLongest:
		return longest, true
	case haveNaive:
		return naive, true
	case retryNow:
		return 0, true
	}
	return 0, false
}

func orZero(text string) string {
	if text == "" {
		return "0"
	}
	return text
}

// IsRetryableStatus is NeoPi's isRetryableStatus: 408, 429 and every 5xx.
func IsRetryableStatus(status int) bool {
	return status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

// Options configures Do, mirroring FetchWithRetryOptions.
type Options struct {
	MaxAttempts int
	MaxDelay    time.Duration
	// DefaultDelay is the fallback schedule by zero-based attempt.
	DefaultDelay func(attempt int) time.Duration
	Now          func() time.Time
	Wait         func(ctx context.Context, delay time.Duration) error
}

// Sleep waits for delay or until ctx is done.
func Sleep(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Do is fetchWithRetry: it sends until a non-retryable status, the attempt
// budget, or a server hint beyond MaxDelay. The caller inspects the status.
func Do(ctx context.Context, send func(attempt int) (*http.Response, error), opts Options) (*http.Response, error) {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 5
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = time.Minute
	}
	if opts.DefaultDelay == nil {
		opts.DefaultDelay = func(attempt int) time.Duration { return 500 * time.Millisecond << attempt }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Wait == nil {
		opts.Wait = Sleep
	}
	fallback := func(attempt int) time.Duration { return min(opts.DefaultDelay(attempt), opts.MaxDelay) }
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		response, err := send(attempt)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if attempt+1 >= opts.MaxAttempts {
				return nil, err
			}
			if errWait := opts.Wait(ctx, fallback(attempt)); errWait != nil {
				return nil, errWait
			}
			continue
		}
		if !IsRetryableStatus(response.StatusCode) || attempt+1 >= opts.MaxAttempts {
			return response, nil
		}
		body, errRead := io.ReadAll(response.Body)
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(body))
		if errRead != nil {
			return response, nil
		}
		delay := fallback(attempt)
		if hint, ok := ExtractRetryHint(response.Header, string(body), opts.Now()); ok {
			hinted := time.Duration(hint * float64(time.Millisecond))
			if hinted > opts.MaxDelay {
				return response, nil
			}
			delay = hinted
		}
		if errWait := opts.Wait(ctx, delay); errWait != nil {
			return nil, errWait
		}
	}
}
