package cursor

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// Port of pi-ai usage/cursor.ts: cursorUsageProvider.fetchUsage and its
// parsers, in NeoPi's report shape.

const (
	// usageSummaryURL is the cursor.com dashboard summary the web session reads.
	usageSummaryURL = "https://cursor.com/api/usage-summary"
	// planInfoURL is CURSOR_PLAN_INFO_URL: the X Subscription's weekly reset.
	planInfoURL = "https://api2.cursor.sh/aiserver.v1.DashboardService/GetPlanInfo"
)

const dayMs = 24 * 60 * 60 * 1000

// Usage is a Cursor usage report.
type Usage struct {
	Provider string        `json:"provider"`
	Limits   []UsageLimit  `json:"limits"`
	Metadata UsageMetadata `json:"metadata"`
}

// UsageMetadata is the report's account identity.
type UsageMetadata struct {
	Email     string `json:"email,omitempty"`
	AccountID string `json:"accountId,omitempty"`
}

// UsageLimit is one meter.
type UsageLimit struct {
	ID     string      `json:"id"`
	Label  string      `json:"label"`
	Scope  UsageScope  `json:"scope"`
	Window UsageWindow `json:"window"`
	Amount UsageAmount `json:"amount"`
	// Status is usageStatus; "" when the meter carries no fraction.
	Status string `json:"status,omitempty"`
}

// UsageScope names the meter's provider and window.
type UsageScope struct {
	Provider string `json:"provider"`
	WindowID string `json:"windowId"`
}

// UsageWindow is a meter's period; ResetsAt is epoch milliseconds.
type UsageWindow struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	DurationMs float64 `json:"durationMs,omitempty"`
	ResetsAt   float64 `json:"resetsAt,omitempty"`
}

// UsageAmount is a meter's consumption; fields NeoPi leaves undefined are nil.
type UsageAmount struct {
	Used              float64  `json:"used"`
	Limit             *float64 `json:"limit,omitempty"`
	Remaining         *float64 `json:"remaining,omitempty"`
	UsedFraction      *float64 `json:"usedFraction,omitempty"`
	RemainingFraction *float64 `json:"remainingFraction,omitempty"`
	Unit              string   `json:"unit"`
}

func number(v float64) *float64 { return &v }

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

type object = *cursorwire.JSONObject

func field(o object, key string) any {
	if o == nil {
		return nil
	}
	value, _ := o.Get(key)
	return value
}

func record(o object, key string) object {
	inner, _ := field(o, key).(object)
	return inner
}

func toNumber(value any) (float64, bool) { return jsjson.ToNumber(value) }

// parseTimestamp tolerates epoch seconds, milliseconds and ISO strings.
func parseTimestamp(value any) (float64, bool) {
	if numeric, ok := toNumber(value); ok {
		if numeric < 1_000_000_000_000 {
			return numeric * 1000, true
		}
		return numeric, true
	}
	text, ok := value.(string)
	if !ok || text == "" {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999Z07:00", "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return float64(parsed.UnixMilli()), true
		}
	}
	return 0, false
}

// deriveResetsAt is deriveResetsAt: a cycle end, else a cycle start plus a
// calendar month.
func deriveResetsAt(payload object) (float64, bool) {
	for _, key := range []string{"billingCycleEnd", "endOfMonth", "resetsAt", "nextReset"} {
		if parsed, ok := parseTimestamp(field(payload, key)); ok {
			return parsed, true
		}
	}
	for _, key := range []string{"startOfMonth", "billingCycleStart", "startOfBillingCycle"} {
		if parsed, ok := parseTimestamp(field(payload, key)); ok {
			// new Date(ms) truncates to whole milliseconds before the month step.
			start := time.UnixMilli(int64(parsed)).UTC()
			return float64(start.AddDate(0, 1, 0).UnixMilli()), true
		}
	}
	return 0, false
}

func monthlyWindow(payload object) UsageWindow {
	window := UsageWindow{ID: "monthly", Label: "Monthly"}
	if resetsAt, ok := deriveResetsAt(payload); ok {
		window.ResetsAt = resetsAt
	}
	return window
}

func limitFor(id, label string, window UsageWindow, amount UsageAmount) UsageLimit {
	return UsageLimit{ID: id, Label: label, Scope: UsageScope{Provider: "cursor", WindowID: window.ID}, Window: window, Amount: amount,
		Status: usageStatus(amount.UsedFraction)}
}

// parseCentsBucket is parseCursorCentsBucket.
func parseCentsBucket(bucket object) *UsageAmount {
	if field(bucket, "enabled") == false {
		return nil
	}
	used, hasUsed := toNumber(field(bucket, "used"))
	remaining, hasRemaining := toNumber(field(bucket, "remaining"))
	validUsed := hasUsed && used >= 0
	validRemaining := hasRemaining && remaining >= 0
	if field(bucket, "limit") == nil {
		if !validUsed {
			return nil
		}
		return &UsageAmount{Used: used / 100, Unit: "usd"}
	}
	limit, hasLimit := toNumber(field(bucket, "limit"))
	if !hasLimit || limit <= 0 {
		return nil
	}
	var spent float64
	switch {
	case hasUsed && used > 0:
		spent = used
	case validRemaining && remaining < limit:
		spent = math.Max(0, limit-remaining)
	case validUsed:
		spent = used
	default:
		return nil
	}
	left := math.Max(0, limit-spent)
	return &UsageAmount{Used: spent / 100, Limit: number(limit / 100), Remaining: number(left / 100), UsedFraction: number(spent / limit),
		RemainingFraction: number(left / limit), Unit: "usd"}
}

// pushPlanRails is pushCursorPlanRails over parseCursorPlanDashboardAmounts.
func pushPlanRails(limits []UsageLimit, bucket object, window UsageWindow) []UsageLimit {
	if field(bucket, "enabled") == false {
		return limits
	}
	limitCents, hasLimit := toNumber(field(bucket, "limit"))
	var limitUSD *float64
	if hasLimit && limitCents > 0 {
		limitUSD = number(limitCents / 100)
	}
	fromPercent := func(percent float64, withLimit bool) UsageAmount {
		fraction := math.Max(0, percent) / 100
		if withLimit && limitUSD != nil {
			spent := *limitUSD * fraction
			return UsageAmount{Used: spent, Limit: number(*limitUSD), Remaining: number(math.Max(0, *limitUSD-spent)), UsedFraction: number(fraction),
				RemainingFraction: number(math.Max(0, 1-fraction)), Unit: "usd"}
		}
		return UsageAmount{Used: fraction * 100, UsedFraction: number(fraction), Unit: "percent"}
	}
	auto, hasAuto := toNumber(field(bucket, "autoPercentUsed"))
	api, hasAPI := toNumber(field(bucket, "apiPercentUsed"))
	if hasAuto {
		limits = append(limits, limitFor("cursor:usd:individual-auto", "Cursor Models", window, fromPercent(auto, false)))
	}
	if hasAPI {
		limits = append(limits, limitFor("cursor:usd:individual-api", "Other Models", window, fromPercent(api, true)))
	}
	if !hasAuto && !hasAPI {
		if total, ok := toNumber(field(bucket, "totalPercentUsed")); ok {
			limits = append(limits, limitFor("cursor:usd:individual-plan", "Personal Usage", window, fromPercent(total, true)))
		} else if cents := parseCentsBucket(bucket); cents != nil {
			limits = append(limits, limitFor("cursor:usd:individual-plan", "Personal Usage", window, *cents))
		}
	}
	return limits
}

// noPrepaidCredit is NO_PREPAID_CREDIT.
func noPrepaidCredit() UsageAmount {
	return UsageAmount{Used: 0, Limit: number(0), Remaining: number(0), UsedFraction: number(1), RemainingFraction: number(0), Unit: "usd"}
}

func isUnifiedMembership(payload object) bool {
	membership, ok := field(payload, "membershipType").(string)
	return ok && strings.HasPrefix(membership, "unified_")
}

// parseUnifiedPool is parseUnifiedPool.
func parseUnifiedPool(plan object) *UsageAmount {
	if field(plan, "enabled") == false {
		return nil
	}
	limitCents, hasLimit := toNumber(field(plan, "limit"))
	usedCents, hasUsed := toNumber(field(plan, "used"))
	percent, hasPercent := toNumber(field(plan, "totalPercentUsed"))
	if !hasLimit || limitCents <= 0 {
		if !hasPercent {
			return nil
		}
		return &UsageAmount{Used: math.Max(0, percent), UsedFraction: number(math.Max(0, percent) / 100), Unit: "percent"}
	}
	fraction := 0.0
	if hasPercent {
		fraction = math.Max(fraction, percent/100)
	}
	if hasUsed {
		fraction = math.Max(fraction, usedCents/limitCents)
	}
	limit := limitCents / 100
	spent := math.Min(limit, limit*fraction)
	return &UsageAmount{Used: spent, Limit: number(limit), Remaining: number(limit - spent), UsedFraction: number(fraction),
		RemainingFraction: number(math.Max(0, 1-fraction)), Unit: "usd"}
}

// parseUnifiedUsage is parseUnifiedUsage.
func parseUnifiedUsage(individual object, weeklyResetsAt float64, hasWeeklyReset bool) []UsageLimit {
	var limits []UsageLimit
	if plan := record(individual, "plan"); plan != nil {
		if pool := parseUnifiedPool(plan); pool != nil {
			weekly := UsageWindow{ID: "weekly", Label: "Weekly", DurationMs: 7 * dayMs}
			if hasWeeklyReset {
				weekly.ResetsAt = weeklyResetsAt
			}
			limits = append(limits, limitFor("cursor:usd:individual-auto", "Cursor Models", weekly, *pool))
		}
	}
	if onDemand := record(individual, "onDemand"); onDemand != nil {
		var credit *UsageAmount
		if limit, ok := toNumber(field(onDemand, "limit")); field(onDemand, "enabled") == false || (ok && limit == 0) {
			amount := noPrepaidCredit()
			credit = &amount
		} else {
			credit = parseCentsBucket(onDemand)
		}
		if credit != nil {
			prepaid := UsageWindow{ID: "prepaid", Label: "Prepaid credit"}
			limits = append(limits, limitFor("cursor:usd:individual-api", "Other Models (prepaid credit)", prepaid, *credit))
		}
	}
	return limits
}

// parseIndividualUsage is parseCursorIndividualUsage over /api/usage-summary.
func parseIndividualUsage(payload object, weeklyResetsAt float64, hasWeeklyReset bool) []UsageLimit {
	individual := record(payload, "individualUsage")
	if individual == nil {
		return nil
	}
	window := monthlyWindow(payload)
	if isUnifiedMembership(payload) {
		return parseUnifiedUsage(individual, weeklyResetsAt, hasWeeklyReset)
	}
	var limits []UsageLimit
	usedOverall := false
	if overall := record(individual, "overall"); overall != nil {
		if amount := parseCentsBucket(overall); amount != nil {
			usedOverall = true
			limits = append(limits, limitFor("cursor:usd:individual-overall", "Personal Usage", window, *amount))
		}
	}
	if plan := record(individual, "plan"); !usedOverall && plan != nil {
		limits = pushPlanRails(limits, plan, window)
	}
	if onDemand := record(individual, "onDemand"); onDemand != nil {
		if amount := parseCentsBucket(onDemand); amount != nil && amount.Limit != nil && *amount.Limit > 0 {
			limits = append(limits, limitFor("cursor:usd:individual-ondemand", "On-Demand Usage", window, *amount))
		}
	}
	return limits
}

// parseLegacyUsage is parseCursorUsage over /auth/usage: one meter per
// request-count or spend bucket.
func parseLegacyUsage(payload object) []UsageLimit {
	if payload == nil {
		return nil
	}
	window := monthlyWindow(payload)
	first := func(value object, keys ...string) (float64, bool) {
		for _, key := range keys {
			if parsed, ok := toNumber(field(value, key)); ok {
				return parsed, true
			}
		}
		return 0, false
	}
	var limits []UsageLimit
	for _, key := range payload.Keys() {
		value := record(payload, key)
		if value == nil {
			continue
		}
		used, ok := first(value, "numRequests", "used", "amountUsed", "usdUsed")
		if !ok {
			continue
		}
		limit, hasLimit := first(value, "maxRequestUsage", "limit", "amountLimit", "usdLimit")
		lower := strings.ToLower(key)
		isUSD := key == "planUsage" || strings.Contains(lower, "usd") || strings.Contains(lower, "billing") || strings.Contains(lower, "stripe")
		unit, id, label := "requests", "cursor:requests:"+strings.TrimSpace(lower), key+" requests"
		if isUSD {
			unit, id, label = "usd", "cursor:usd:"+strings.TrimSpace(lower), key+" spend"
		}
		amount := UsageAmount{Used: used, Unit: unit}
		if hasLimit {
			left := math.Max(0, limit-used)
			amount.Limit, amount.Remaining = number(limit), number(left)
			if limit > 0 {
				amount.UsedFraction, amount.RemainingFraction = number(used/limit), number(left/limit)
			} else {
				amount.UsedFraction, amount.RemainingFraction = number(0), number(0)
			}
		}
		limits = append(limits, limitFor(id, label, window, amount))
	}
	return limits
}

// fetchJSON is fetchCursorJson: a failed or non-2xx request is nil.
func fetchJSON(request *http.Request, client *http.Client, source string) object {
	response, err := client.Do(request)
	if err != nil {
		log.WithField("provider", "cursor").WithField("source", source).WithError(err).Warn("Cursor usage request error")
		return nil
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		log.WithField("provider", "cursor").WithField("source", source).WithField("status", response.StatusCode).Warn("Cursor usage request failed")
		return nil
	}
	body, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return nil
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(io.LimitReader(body, 4<<20))
	if err != nil {
		return nil
	}
	value, ok := cursorwire.ParseJSON(string(data))
	if !ok {
		return nil
	}
	payload, _ := value.(object)
	return payload
}

// FetchUsage is cursorUsageProvider.fetchUsage for a signed-in account:
// /auth/usage on the account's API host, and on the default host the
// cursor.com usage summary (with GetPlanInfo's weekly reset for the X
// Subscription) and the profile email. A report from neither is nil.
func FetchUsage(ctx context.Context, client *http.Client, baseURL, token, accountID, storedEmail string) *Usage {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = cursorwire.DefaultBaseURL
	}
	var wg sync.WaitGroup
	var legacy, summary []UsageLimit
	var hasLegacy, hasSummary bool
	var profileEmail string
	wg.Add(1)
	go func() {
		defer wg.Done()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/auth/usage", nil)
		if err != nil {
			return
		}
		fetchHeaders(req.Header)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		if payload := fetchJSON(req, client, "auth-usage"); payload != nil {
			legacy = parseLegacyUsage(payload)
			hasLegacy = len(legacy) > 0
		}
	}()
	if userID := UserID(token); userID != "" && base == cursorwire.DefaultBaseURL {
		wg.Add(2)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, usageSummaryURL, nil)
			if err != nil {
				return
			}
			SetSessionHeaders(req.Header, userID, token)
			payload := fetchJSON(req, client, "usage-summary")
			if payload == nil {
				return
			}
			weeklyResetsAt, hasWeeklyReset := 0.0, false
			if isUnifiedMembership(payload) {
				planReq, errPlan := http.NewRequestWithContext(ctx, http.MethodPost, planInfoURL, strings.NewReader("{}"))
				if errPlan == nil {
					SetConnectJSONHeaders(planReq.Header, token)
					if plan := record(fetchJSON(planReq, client, "plan-info"), "planInfo"); plan != nil {
						weeklyResetsAt, hasWeeklyReset = parseTimestamp(field(plan, "includedUsageResetsAt"))
					}
				}
			}
			summary = parseIndividualUsage(payload, weeklyResetsAt, hasWeeklyReset)
			hasSummary = len(summary) > 0
		}()
		go func() {
			defer wg.Done()
			auth := newAuth(client)
			email, err := auth.FetchEmail(ctx, token)
			if err != nil {
				log.WithField("provider", "cursor").WithField("source", "auth-me").WithError(err).Warn("Cursor usage request error")
				return
			}
			profileEmail = email
		}()
	}
	wg.Wait()

	var limits []UsageLimit
	switch {
	case hasLegacy && hasSummary:
		// Beside the summary's dollar rails, an uncapped unused /auth/usage
		// bucket carries nothing.
		for _, limit := range legacy {
			if limit.Amount.Limit != nil || limit.Amount.Used > 0 {
				limits = append(limits, limit)
			}
		}
		limits = append(limits, summary...)
	case hasLegacy:
		limits = legacy
	case hasSummary:
		limits = summary
	default:
		return nil
	}
	email := profileEmail
	if email == "" {
		email = strings.TrimSpace(storedEmail)
	}
	return &Usage{Provider: "cursor", Limits: limits, Metadata: UsageMetadata{Email: email, AccountID: accountID}}
}
