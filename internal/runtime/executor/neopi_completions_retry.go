package executor

import (
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	completionQuotaReset    = regexp.MustCompile(`(?i)reset after (?:(\d+)h)?(?:(\d+)m)?(\d+(?:\.\d+)?)s`)
	completionResetAt       = regexp.MustCompile(`(?i)(?:will\s+)?reset at\s+([0-9]{4}-[0-9]{2}-[0-9]{2}[ T][0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:?[0-9]{2})?)|将在\s*([0-9]{4}-[0-9]{2}-[0-9]{2}\s+[0-9]{2}:[0-9]{2}:[0-9]{2})\s*重置`)
	completionCompoundReset = regexp.MustCompile(`(?i)resets?\s+in\s+~?\s*(\d+(?:\.\d+)?)\s*hr\s*(\d+(?:\.\d+)?)\s*min\b`)
	completionResetIn       = regexp.MustCompile(`(?i)(?:will\s+)?resets?\s+in\s+~?\s*([0-9.]+)\s*(ms|sec|s|minutes?|mins?|m|hours?|hrs?|h|days?|d)\b`)
	completionRetryMS       = regexp.MustCompile(`(?i)\bretry-after-ms\s*[:=]\s*([0-9]+)\b`)
	completionPleaseRetry   = regexp.MustCompile(`(?i)please retry in ([0-9.]+)(ms|s)`)
	completionRetryDelay    = regexp.MustCompile(`(?i)"retryDelay":\s*"([0-9.]+)(ms|s)"`)
	completionTryAgain      = regexp.MustCompile(`(?i)try again in\s+~?\s*([0-9.]+)\s*(ms|sec|s|minutes?|mins?|m|hours?|hrs?|h)\b`)
	completionRetryAfter    = regexp.MustCompile(`(?i)retry-after\s*[:=]\s*([^\s,;]+)`)
	completionResetMS       = regexp.MustCompile(`(?i)x-ratelimit-reset-ms\s*[:=]\s*(\d+)`)
	completionReset         = regexp.MustCompile(`(?i)x-ratelimit-reset\s*[:=]\s*(\d+)`)
)

// neoPiCompletionsRetryHint ports fetch-retry.extractRetryHint. Header hints
// have precedence; otherwise the longest unambiguous body hint wins.
func neoPiCompletionsRetryHint(headers http.Header, body []byte, now time.Time) (time.Duration, bool) {
	number := func(s string) (float64, bool) {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
	}
	milliseconds := func(v float64) time.Duration {
		if v >= float64(math.MaxInt64)/float64(time.Millisecond) {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(v * float64(time.Millisecond))
	}
	if v, ok := number(headers.Get("retry-after-ms")); ok && v >= 0 {
		return milliseconds(v), true
	}
	if raw := headers.Get("retry-after"); raw != "" {
		if v, ok := number(raw); ok {
			return milliseconds(math.Max(0, v*1000)), true
		}
		if at, err := http.ParseTime(raw); err == nil {
			return max(0, at.Sub(now)), true
		}
	}
	if v, ok := number(headers.Get("x-ratelimit-reset-ms")); ok && v > 0 {
		if v > 1e12 {
			v -= float64(now.UnixMilli())
		} else if v > 1e9 {
			v = v*1000 - float64(now.UnixMilli())
		}
		if v > 0 {
			return milliseconds(v), true
		}
	}
	if v, ok := number(headers.Get("x-ratelimit-reset")); ok {
		if v = math.Trunc(v)*1000 - float64(now.UnixMilli()); v > 0 {
			return milliseconds(v), true
		}
	}
	if v, ok := number(headers.Get("x-ratelimit-reset-after")); ok && v > 0 {
		return milliseconds(v * 1000), true
	}
	var longest, naive float64
	found, retryNow := false, false
	consider := func(v float64, clamp bool) {
		if v > 0 {
			longest = math.Max(longest, v)
			found = true
		} else if clamp {
			retryNow = true
		}
	}
	parse := func(s []byte) float64 { v, _ := number(string(s)); return v }
	units := func(unit string) float64 {
		switch strings.ToLower(unit) {
		case "ms":
			return 1
		case "s", "sec":
			return 1000
		case "m", "min", "mins", "minute", "minutes":
			return 60000
		case "h", "hr", "hrs", "hour", "hours":
			return 3600000
		case "d", "day", "days":
			return 86400000
		}
		return 0
	}
	if m := completionQuotaReset.FindSubmatch(body); m != nil {
		consider(((parse(m[1])*60+parse(m[2]))*60+parse(m[3]))*1000, false)
	}
	for _, m := range completionResetAt.FindAllSubmatch(body, -1) {
		stamp := string(m[1])
		if stamp == "" {
			stamp = string(m[2])
		}
		stamp = strings.Replace(stamp, " ", "T", 1)
		zone := strings.HasSuffix(strings.ToUpper(stamp), "Z") || strings.ContainsAny(stamp[10:], "+-")
		if !zone {
			stamp += "Z"
		}
		if at, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			v := float64(at.Sub(now)) / float64(time.Millisecond)
			if zone {
				consider(v, false)
			} else if v > naive {
				naive = v
			}
		}
	}
	if m := completionCompoundReset.FindSubmatch(body); m != nil {
		consider(parse(m[1])*3600000+parse(m[2])*60000, false)
	}
	for _, re := range []*regexp.Regexp{completionResetIn, completionPleaseRetry, completionRetryDelay, completionTryAgain} {
		if m := re.FindSubmatch(body); m != nil {
			consider(parse(m[1])*units(string(m[2])), false)
		}
	}
	if m := completionRetryMS.FindSubmatch(body); m != nil {
		consider(parse(m[1]), true)
	}
	if m := completionRetryAfter.FindSubmatch(body); m != nil {
		if v, ok := number(string(m[1])); ok {
			consider(v*1000, true)
		} else if at, err := http.ParseTime(string(m[1])); err == nil {
			consider(float64(at.Sub(now))/float64(time.Millisecond), true)
		}
	}
	if m := completionResetMS.FindSubmatch(body); m != nil {
		v := parse(m[1])
		if v > 1e12 {
			v -= float64(now.UnixMilli())
		}
		consider(v, true)
	}
	if m := completionReset.FindSubmatch(body); m != nil {
		v := parse(m[1]) * 1000
		if v > 1e12 {
			v -= float64(now.UnixMilli())
		}
		consider(v, true)
	}
	if found {
		return milliseconds(longest), true
	}
	if naive > 0 {
		return milliseconds(naive), true
	}
	return 0, retryNow
}
