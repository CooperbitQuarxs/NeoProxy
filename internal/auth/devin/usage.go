package devin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Usage is NeoPi's Devin usage report (devinUsageProvider.fetchUsage over the
// GetUserStatus seat-management RPC), in NeoPi's report shape.
type Usage struct {
	Provider string        `json:"provider"`
	Limits   []UsageLimit  `json:"limits"`
	Notes    []string      `json:"notes,omitempty"`
	Metadata UsageMetadata `json:"metadata"`
	// PlanStatus is the raw status the conductor's quota signals read; nil
	// when the response carried none.
	PlanStatus *devinwire.PlanStatus `json:"-"`
}

// UsageMetadata is the report's account identity and plan facts.
type UsageMetadata struct {
	Source            string  `json:"source"`
	Email             string  `json:"email,omitempty"`
	AccountID         string  `json:"accountId,omitempty"`
	OrgID             string  `json:"orgId,omitempty"`
	OrgName           string  `json:"orgName,omitempty"`
	PlanType          string  `json:"planType,omitempty"`
	PlanEnd           float64 `json:"planEnd,omitempty"`
	OverageBalanceUSD float64 `json:"overageBalanceUsd,omitempty"`
}

// UsageLimit is one credit bucket or quota window.
type UsageLimit struct {
	ID     string      `json:"id"`
	Label  string      `json:"label"`
	Scope  UsageScope  `json:"scope"`
	Window UsageWindow `json:"window"`
	Amount UsageAmount `json:"amount"`
	// Status is usageStatus: ok, warning, exhausted, or unknown without a fraction.
	Status string `json:"status"`
}

// UsageScope names the account a limit belongs to.
type UsageScope struct {
	Provider  string `json:"provider"`
	AccountID string `json:"accountId,omitempty"`
	OrgID     string `json:"orgId,omitempty"`
	Tier      string `json:"tier,omitempty"`
	WindowID  string `json:"windowId"`
}

// UsageWindow is a limit's period; ResetsAt is epoch milliseconds.
type UsageWindow struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	DurationMs float64 `json:"durationMs,omitempty"`
	ResetsAt   float64 `json:"resetsAt,omitempty"`
}

// UsageAmount is a limit's consumption. Credit buckets without a monthly
// grant carry no limit and no fraction.
type UsageAmount struct {
	Used              float64  `json:"used"`
	Limit             *float64 `json:"limit,omitempty"`
	Remaining         float64  `json:"remaining"`
	UsedFraction      *float64 `json:"usedFraction,omitempty"`
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	Unit              string   `json:"unit"`
}

const (
	dayMs  = 24 * 60 * 60 * 1000
	weekMs = 7 * dayMs
)

// teamsTierNames is TeamsTier's enum names, for plans that ship no plan_name.
var teamsTierNames = map[int32]string{
	1: "TEAMS", 2: "PRO", 3: "ENTERPRISE_SAAS", 4: "HYBRID", 5: "ENTERPRISE_SELF_HOSTED", 6: "WAITLIST_PRO",
	7: "TEAMS_ULTIMATE", 8: "PRO_ULTIMATE", 9: "TRIAL", 10: "ENTERPRISE_SELF_SERVE", 11: "ENTERPRISE_SAAS_POOLED",
	12: "DEVIN_ENTERPRISE", 14: "DEVIN_TEAMS", 15: "DEVIN_TEAMS_V2", 16: "DEVIN_PRO", 17: "DEVIN_MAX", 18: "MAX",
	19: "DEVIN_FREE", 20: "DEVIN_TRIAL",
}

// FetchUsage reads the account's plan, credits and quota windows from
// GetUserStatus with the Devin CLI identity, as NeoPi does. Seat management
// lives on the Cascade backend, never on api.devin.ai.
func FetchUsage(ctx context.Context, client *http.Client, baseURL, token string) (*Usage, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("devin usage: credential has no session token")
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = devinwire.DefaultBaseURL
	}
	body := devinwire.EncodeUserStatusRequest(devinwire.CLIMetadata(token, ""))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+devinwire.UserStatusPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("User-Agent", devinwire.BunUserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("devin usage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("devin usage: GetUserStatus returned HTTP %d", resp.StatusCode)
	}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, fmt.Errorf("devin usage: %w", err)
	}
	defer func() { _ = decoded.Close() }()
	payload, err := io.ReadAll(io.LimitReader(decoded, devinwire.MaxFramePayload))
	if err != nil {
		return nil, fmt.Errorf("devin usage: %w", err)
	}
	response, ok := devinwire.DecodeUnary(payload, devinwire.DecodeUserStatus)
	if !ok {
		return nil, fmt.Errorf("devin usage: GetUserStatus response did not decode")
	}
	usage := BuildUsage(response)
	if usage == nil {
		return nil, fmt.Errorf("devin usage: GetUserStatus response carried no user status")
	}
	return usage, nil
}

// BuildUsage is buildDevinReport; nil when the response has no user status.
func BuildUsage(response devinwire.UserStatusResponse) *Usage {
	status := response.UserStatus
	if status == nil {
		return nil
	}
	planStatus := status.PlanStatus
	// GetUserStatusResponse.plan_info is authoritative; the nested copy only
	// fills in for older servers.
	plan := response.PlanInfo
	if plan == nil && planStatus != nil {
		plan = planStatus.PlanInfo
	}
	email := strings.TrimSpace(status.Email)
	accountID := strings.TrimSpace(status.UserID)
	orgID, orgName, planName := "", "", ""
	tier := status.TeamsTier
	if plan != nil {
		if plan.Devin != nil {
			orgID = strings.TrimSpace(plan.Devin.OrgID)
			orgName = strings.TrimSpace(plan.Devin.AccountDisplayName)
		}
		planName = strings.TrimSpace(plan.PlanName)
		tier = plan.TeamsTier
	}
	if orgID == "" {
		orgID = strings.TrimSpace(status.TeamID)
	}
	if planName == "" {
		planName = teamsTierLabel(tier)
	}
	scope := UsageScope{Provider: "devin", AccountID: accountID, OrgID: orgID, Tier: planName}

	usage := &Usage{Provider: "devin", Limits: []UsageLimit{}, PlanStatus: planStatus,
		Metadata: UsageMetadata{Source: "seat-management", Email: email, AccountID: accountID, OrgID: orgID, OrgName: orgName, PlanType: planName}}
	if planStatus != nil {
		var monthlyPrompt, monthlyFlow, monthlyFlex int32
		if plan != nil {
			monthlyPrompt, monthlyFlow, monthlyFlex = plan.MonthlyPromptCredits, plan.MonthlyFlowCredits, plan.MonthlyFlexCreditPurchaseAmount
		}
		window := planWindow(planStatus)
		for _, bucket := range []struct {
			id, label              string
			limit, used, available int32
		}{
			{"prompt", "Prompt Credits", monthlyPrompt, planStatus.UsedPromptCredits, planStatus.AvailablePromptCredits},
			{"flow", "Flow Credits", monthlyFlow, planStatus.UsedFlowCredits, planStatus.AvailableFlowCredits},
			{"flex", "Flex Credits", monthlyFlex, planStatus.UsedFlexCredits, planStatus.AvailableFlexCredits},
		} {
			if limit, ok := creditLimit(bucket.id, bucket.label, bucket.limit, bucket.used, bucket.available, window, scope); ok {
				usage.Limits = append(usage.Limits, limit)
			}
		}
		quotaBilled := plan != nil && plan.BillingStrategy == devinwire.BillingStrategyQuota
		for _, quota := range []struct {
			id, label, windowID string
			durationMs          float64
			remainingPercent    int32
			resetAtUnix         int64
			hidden              bool
		}{
			{"daily", "Daily Quota", "1d", dayMs, planStatus.DailyQuotaRemainingPercent, planStatus.DailyQuotaResetAtUnix, plan != nil && plan.HideDailyQuota},
			{"weekly", "Weekly Quota", "7d", weekMs, planStatus.WeeklyQuotaRemainingPercent, planStatus.WeeklyQuotaResetAtUnix, plan != nil && plan.HideWeeklyQuota},
		} {
			// Credit-billed plans leave the percents at their defaults, which
			// would read as fully consumed: only dated or quota-billed windows count.
			if quota.hidden || (quota.resetAtUnix <= 0 && !quotaBilled) {
				continue
			}
			remaining := math.Max(0, math.Min(100, float64(quota.remainingPercent)))
			used := 100 - remaining
			usedFraction, remainingFraction, limit := used/100, remaining/100, 100.0
			window := UsageWindow{ID: quota.windowID, Label: quota.label, DurationMs: quota.durationMs, ResetsAt: positiveTimestampMs(quota.resetAtUnix)}
			limitScope := scope
			limitScope.WindowID = quota.windowID
			usage.Limits = append(usage.Limits, UsageLimit{ID: "devin:quota:" + quota.id, Label: quota.label, Scope: limitScope, Window: window,
				Amount: UsageAmount{Used: used, Limit: &limit, Remaining: remaining, UsedFraction: &usedFraction, RemainingFraction: &remainingFraction, Unit: "percent"},
				Status: usageStatus(&usedFraction)})
		}
		if !planStatus.PlanEnd.IsZero() {
			if end := timestampMs(planStatus); end > 0 {
				usage.Metadata.PlanEnd = end
			}
		}
		if overage := float64(planStatus.OverageBalanceMicros) / 1_000_000; overage != 0 {
			usage.Metadata.OverageBalanceUSD = overage
			usage.Notes = []string{fmt.Sprintf("Overage balance: $%.2f", overage)}
		}
	}
	return usage
}

// teamsTierLabel is TEAMS_TIER_DEVIN_PRO -> "Devin Pro".
func teamsTierLabel(tier int32) string {
	name := teamsTierNames[tier]
	words := strings.Split(strings.ToLower(name), "_")
	for i, word := range words {
		if word != "" {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

// timestampMs is plan_end in epoch milliseconds, keeping sub-millisecond nanos.
func timestampMs(planStatus *devinwire.PlanStatus) float64 {
	end := planStatus.PlanEnd
	return float64(end.Unix())*1000 + float64(end.Nanosecond())/1_000_000
}

// planWindow is the billing cycle every credit bucket shares; plan_end resets it.
func planWindow(planStatus *devinwire.PlanStatus) UsageWindow {
	window := UsageWindow{ID: "monthly", Label: "Plan Period"}
	start, hasStart := 0.0, !planStatus.PlanStart.IsZero()
	if hasStart {
		start = float64(planStatus.PlanStart.Unix())*1000 + float64(planStatus.PlanStart.Nanosecond())/1_000_000
	}
	if !planStatus.PlanEnd.IsZero() {
		end := timestampMs(planStatus)
		if hasStart && end > start {
			window.DurationMs = end - start
		}
		if end > 0 {
			window.ResetsAt = end
		}
	}
	return window
}

// creditLimit is devinCreditLimit; credits carry no catalog unit.
func creditLimit(id, label string, grant, used, available int32, window UsageWindow, scope UsageScope) (UsageLimit, bool) {
	usedCredits := math.Max(0, float64(used))
	remaining := math.Max(0, float64(available))
	if grant <= 0 && usedCredits == 0 && remaining == 0 {
		return UsageLimit{}, false
	}
	amount := UsageAmount{Used: usedCredits, Remaining: remaining, Unit: "unknown"}
	if grant > 0 {
		limit := float64(grant)
		usedFraction := usedCredits / limit
		remainingFraction := math.Max(0, 1-usedFraction)
		amount.Limit, amount.UsedFraction, amount.RemainingFraction = &limit, &usedFraction, &remainingFraction
	}
	scope.WindowID = window.ID
	return UsageLimit{ID: "devin:credits:" + id, Label: label, Scope: scope, Window: window, Amount: amount, Status: usageStatus(amount.UsedFraction)}, true
}

// usageStatus maps a used fraction to NeoPi's status thresholds.
func usageStatus(usedFraction *float64) string {
	switch {
	case usedFraction == nil:
		return "unknown"
	case *usedFraction >= 1:
		return "exhausted"
	case *usedFraction >= 0.9:
		return "warning"
	}
	return "ok"
}

// positiveTimestampMs is parsePositiveTimestamp: epoch seconds or
// milliseconds, 0 when not positive.
func positiveTimestampMs(value int64) float64 {
	if value <= 0 {
		return 0
	}
	if value < 1_000_000_000_000 {
		return float64(value) * 1000
	}
	return float64(value)
}
