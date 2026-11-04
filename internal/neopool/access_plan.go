package neopool

import (
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// AccessPlan is the plan shown for one piece of provider access. Source is
// "detected" (read from the credential), "admin" (override) or "default".
type AccessPlan struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	MonthlyUSD float64 `json:"monthlyUSD"`
	Source     string  `json:"source"`
}

// planOverride is the durable admin choice; nil fields are not overridden.
type planOverride struct {
	PlanID     *string
	MonthlyUSD *float64
}

// detectPlan reads the subscription tier a credential reveals. It returns the
// catalogue plan ID, or "" when the credential does not say.
func detectPlan(provider string, a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	switch provider {
	case "codex":
		// The id_token claim is authoritative; the engine defaults a missing claim
		// to "free", so a stored plan_type alone is only trusted after the claim
		// and the credential's file name.
		if token := metaString(a, "id_token"); token != "" {
			if claims, err := codex.ParseJWTToken(token); err == nil && claims != nil {
				if plan := chatGPTPlanID(claims.CodexAuthInfo.ChatgptPlanType); plan != "" {
					return plan
				}
			}
		}
		if plan := fileNamePlan(a, chatGPTPlans); plan != "" {
			return plan
		}
		for _, raw := range []string{metaString(a, "plan_type"), a.Attributes["plan_type"]} {
			if plan := chatGPTPlanID(raw); plan != "" {
				return plan
			}
		}
	case "claude":
		tier := strings.ToLower(metaString(a, "rate_limit_tier") + " " + metaString(a, "rateLimitTier"))
		sub := strings.ToLower(firstNonEmpty(metaString(a, "subscription_type"), metaString(a, "subscriptionType"), metaString(a, "plan"), metaString(a, "plan_type")))
		switch {
		case strings.Contains(tier, "20x"):
			return "max20x"
		case strings.Contains(tier, "5x"):
			return "max5x"
		case strings.Contains(sub, "max"):
			// A Max subscription without its tier is valued at the smaller tier.
			return "max5x"
		case strings.Contains(sub, "pro"):
			return "pro"
		}
	case "xai":
		hint := strings.ToLower(planHint(a))
		switch {
		case strings.Contains(hint, "heavy"):
			return "supergrok-heavy"
		case strings.Contains(hint, "supergrok"):
			return "supergrok"
		}
	case "antigravity", "gemini":
		hint := strings.ToLower(planHint(a))
		switch {
		case strings.Contains(hint, "ultra"):
			return "google-ai-ultra"
		case strings.Contains(hint, "pro") || strings.Contains(hint, "standard"):
			return "google-ai-pro"
		}
	}
	return ""
}

func chatGPTPlanID(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, plan := range chatGPTPlans {
		if raw == plan.ID {
			return raw
		}
	}
	return ""
}

// fileNamePlan matches the plan suffix the engine writes into credential names,
// e.g. codex-<hash>-<email>-pro.json.
func fileNamePlan(a *coreauth.Auth, plans []ProviderPlan) string {
	name := a.FileName
	if name == "" {
		name = filepath.Base(a.Attributes["path"])
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".json")
	for _, plan := range plans {
		if strings.HasSuffix(name, "-"+plan.ID) {
			return plan.ID
		}
	}
	return ""
}

func planHint(a *coreauth.Auth) string {
	for _, key := range []string{"plan_type", "plan", "subscription_type", "subscriptionType", "subscription", "tier", "tier_id", "subs_tier_name"} {
		if v := metaString(a, key); v != "" {
			return v
		}
		if v := strings.TrimSpace(a.Attributes[key]); v != "" {
			return v
		}
	}
	return ""
}

func metaString(a *coreauth.Auth, key string) string {
	if a == nil || a.Metadata == nil {
		return ""
	}
	v, _ := a.Metadata[key].(string)
	return strings.TrimSpace(v)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolvePlan combines detection, catalogue defaults and the admin override.
func resolvePlan(provider, kind string, a *coreauth.Auth, override planOverride) AccessPlan {
	plan := AccessPlan{ID: "default", Name: "Subscription", MonthlyUSD: defaultSubscriptionUSD, Source: "default"}
	switch kind {
	case "apiKey":
		plan = AccessPlan{ID: "apiKey", Name: "Pay-as-you-go key", MonthlyUSD: defaultAPIKeyUSD, Source: "default"}
	case "neopi":
		plan.Name = "Shared from NeoPi"
	}
	if kind != "apiKey" {
		if id := detectPlan(provider, a); id != "" {
			if known, ok := catalogPlan(provider, id); ok {
				plan = AccessPlan{ID: known.ID, Name: known.Name, MonthlyUSD: known.MonthlyUSD, Source: "detected"}
			}
		}
	}
	if override.PlanID != nil {
		if known, ok := catalogPlan(provider, *override.PlanID); ok {
			plan = AccessPlan{ID: known.ID, Name: known.Name, MonthlyUSD: known.MonthlyUSD}
		} else if *override.PlanID == "default" {
			plan.ID, plan.Name = "default", "Subscription"
		} else if *override.PlanID == "apiKey" {
			plan.ID, plan.Name = "apiKey", "Pay-as-you-go key"
		}
		plan.Source = "admin"
	}
	if override.MonthlyUSD != nil {
		plan.MonthlyUSD = *override.MonthlyUSD
		plan.Source = "admin"
	}
	return plan
}

// validPlanID reports whether an admin may choose planID for provider.
func validPlanID(provider, planID string) bool {
	if planID == "default" || planID == "apiKey" {
		return true
	}
	_, ok := catalogPlan(provider, planID)
	return ok
}
