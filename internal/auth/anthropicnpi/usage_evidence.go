package anthropicnpi

import (
	"math"
	"regexp"
	"strings"
	"time"
)

// Extra spend and saved resets make a usage response authoritative even when
// it has no account-wide windows. They must not trigger retries, host fallback,
// or a fictitious shared budget in NeoProxy's percent-only provider windows.
func usageDollars(amount, exponent, currency any, requireCurrency bool) (float64, bool) {
	n, ok := amount.(float64)
	e, expOK := exponent.(float64)
	if !ok || !expOK || n < 0 || e < 0 || math.Trunc(n) != n || math.Trunc(e) != e || n > 9007199254740991 || e > 9007199254740991 {
		return 0, false
	}
	if currency == nil {
		if requireCurrency {
			return 0, false
		}
	} else if code, ok := currency.(string); !ok || strings.ToUpper(code) != "USD" {
		return 0, false
	}
	divisor := math.Pow(10, e)
	if math.IsInf(divisor, 0) {
		return 0, false
	}
	value := n / divisor
	return value, !math.IsNaN(value) && !math.IsInf(value, 0)
}

func hasExtraUsage(body map[string]any) bool {
	var used, limit float64
	var ok bool
	if spend := body["spend"]; spend != nil {
		row, valid := spend.(map[string]any)
		if !valid || row["enabled"] != true {
			return false
		}
		cap, present := row["limit"]
		amount, valid := row["used"].(map[string]any)
		if !present || !valid {
			return false
		}
		used, ok = usageDollars(amount["amount_minor"], amount["exponent"], amount["currency"], true)
		if !ok {
			return false
		}
		if cap == nil {
			return true
		}
		amount, valid = cap.(map[string]any)
		if !valid {
			return false
		}
		limit, ok = usageDollars(amount["amount_minor"], amount["exponent"], amount["currency"], true)
	} else {
		row, valid := body["extra_usage"].(map[string]any)
		if !valid || row["is_enabled"] != true {
			return false
		}
		cap, present := row["monthly_limit"]
		if !present {
			return false
		}
		if currency, exists := row["currency"]; exists && currency == nil {
			return false
		}
		exponent, present := row["decimal_places"]
		if !present {
			exponent = float64(2)
		}
		used, ok = usageDollars(row["used_credits"], exponent, row["currency"], false)
		if !ok {
			return false
		}
		if cap == nil {
			return true
		}
		limit, ok = usageDollars(cap, exponent, row["currency"], false)
	}
	if !ok || limit <= 0 {
		return false
	}
	return !math.IsInf(used/limit, 0) && !math.IsInf(math.Max(0, limit-used)/limit, 0)
}

var resetGrantID = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
var resetWindowID = regexp.MustCompile(`^[a-z0-9_-]{1,80}$`)

func resetOptionalText(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}
func resetOptionalTime(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(text))
	return err == nil
}
func resetOptionalBool(row map[string]any, key string) bool {
	value, exists := row[key]
	if !exists {
		return true
	}
	_, ok := value.(bool)
	return ok
}
func resetInteger(value any) bool {
	n, ok := value.(float64)
	return ok && n >= 0 && !math.IsInf(n, 0) && math.Trunc(n) == n
}
func resetWindows(value any) bool {
	list, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		id, ok := item.(string)
		if !ok || !resetWindowID.MatchString(id) {
			return false
		}
	}
	return true
}

func hasInlineResetInventory(body map[string]any, now time.Time) bool {
	cedar, hasCedar := body["cedar_ember"].(map[string]any)
	if hasCedar {
		if _, ok := cedar["eligible"].(bool); !ok {
			return false
		}
		if !resetOptionalText(cedar["ineligible_reason"]) || !resetOptionalTime(cedar["cooldown_until"]) || !resetOptionalBool(cedar, "at_limit") {
			return false
		}
		if value, exists := cedar["exhausted"]; exists && !resetWindows(value) {
			return false
		}
		next := cedar["next_grant_id"]
		if next != nil {
			if id, ok := next.(string); !ok || !resetGrantID.MatchString(id) {
				return false
			}
		}
		grants, ok := cedar["grants"].([]any)
		if _, exists := cedar["grants"]; exists && !ok {
			return false
		}
		available := false
		for _, value := range grants {
			grant, ok := value.(map[string]any)
			if !ok {
				return false
			}
			id, ok := grant["id"].(string)
			if !ok || !resetGrantID.MatchString(id) || !resetInteger(grant["resets_left"]) {
				return false
			}
			if value, exists := grant["resets_total"]; exists && !resetInteger(value) {
				return false
			}
			// Unlike optional reason strings, an empty grant label is accepted.
			if label := grant["label"]; label != nil {
				if _, ok := label.(string); !ok {
					return false
				}
			}
			if !resetOptionalTime(grant["starts_at"]) || !resetOptionalTime(grant["ends_at"]) || !resetWindows(grant["clears"]) || !resetWindows(grant["blocking"]) {
				return false
			}
			for _, key := range []string{"paused", "usable_now", "use_requires_limit"} {
				if !resetOptionalBool(grant, key) {
					return false
				}
			}
			if value, exists := grant["percent_used"]; exists {
				percents, ok := value.(map[string]any)
				if !ok {
					return false
				}
				for id, percent := range percents {
					if !resetWindowID.MatchString(id) || !resetInteger(percent) || percent.(float64) > 100 {
						return false
					}
				}
			}
			expired := false
			if until, ok := grant["ends_at"].(string); ok {
				at, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(until))
				expired = !at.After(now)
			}
			available = available || (!expired && grant["resets_left"].(float64) > 0) || (next == id && cedar["eligible"] == true)
		}
		if available {
			return true
		}
	}
	if juniper, ok := body["juniper_tide"].(map[string]any); ok {
		if _, ok := juniper["eligible"].(bool); !ok {
			return false
		}
		if !resetOptionalText(juniper["ineligible_reason"]) || !resetOptionalTime(juniper["next_available_at"]) || !resetOptionalTime(juniper["weekly_resets_at"]) || !resetOptionalBool(juniper, "available") {
			return false
		}
		if arm := juniper["arm"]; arm != nil && arm != "control" && arm != "reset" {
			return false
		}
		if value, exists := juniper["resets_per_week"]; exists && !resetInteger(value) {
			return false
		}
		return true
	}
	return hasCedar
}
