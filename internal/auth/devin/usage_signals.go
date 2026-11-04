package devin

import (
	"fmt"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ApplyUsage records a usage report on a: the account identity NeoPi's
// report carries, and the quota signals the conductor's Devin quota
// cooldown and restoration read ("55%" percents, RFC 3339 instants).
func ApplyUsage(a *coreauth.Auth, usage *Usage, now time.Time) {
	if a == nil || usage == nil {
		return
	}
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	if a.Attributes == nil {
		a.Attributes = map[string]string{}
	}
	for key, value := range map[string]string{
		"email":    usage.Metadata.Email,
		"user_id":  usage.Metadata.AccountID,
		"org_id":   usage.Metadata.OrgID,
		"org_name": usage.Metadata.OrgName,
		"plan":     usage.Metadata.PlanType,
	} {
		if value != "" {
			a.Metadata[key] = value
			a.Attributes[key] = value
		}
	}
	if a.Quota.Signals == nil {
		a.Quota.Signals = map[string]string{}
	}
	if usage.Metadata.PlanType != "" {
		a.Quota.Signals["plan"] = usage.Metadata.PlanType
	}
	if status := usage.PlanStatus; status != nil {
		a.Quota.Signals["daily_quota_remaining_percent"] = fmt.Sprintf("%d%%", status.DailyQuotaRemainingPercent)
		a.Quota.Signals["weekly_quota_remaining_percent"] = fmt.Sprintf("%d%%", status.WeeklyQuotaRemainingPercent)
		for key, unix := range map[string]int64{"daily_quota_reset_at": status.DailyQuotaResetAtUnix, "weekly_quota_reset_at": status.WeeklyQuotaResetAtUnix} {
			if ms := positiveTimestampMs(unix); ms > 0 {
				a.Quota.Signals[key] = time.UnixMilli(int64(ms)).UTC().Format(time.RFC3339)
			}
		}
		for key, instant := range map[string]time.Time{"plan_start": status.PlanStart, "plan_end": status.PlanEnd} {
			if !instant.IsZero() {
				a.Quota.Signals[key] = instant.UTC().Format(time.RFC3339)
			}
		}
	}
	a.Quota.ObservedAt = now
}
