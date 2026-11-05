package neopool

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/alibabatokenplan"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A Token Plan key is metered only when it carries its console cookie, and the
// cookie (not the inference key) is what the usage request authenticates with.
func TestTokenPlanQuotaProviderNeedsCookie(t *testing.T) {
	a := &coreauth.Auth{Provider: "openai-compatible-alibaba-token-plan", Attributes: map[string]string{"compat_name": "alibaba-token-plan", "api_key": "sk-sp-key", "base_url": alibabatokenplan.ChinaBaseURL}}
	if got := quotaProvider(a); got != "" {
		t.Fatalf("quotaProvider without cookie = %q", got)
	}
	a.Attributes["console_cookie"] = "a=b"
	if got := quotaProvider(a); got != "alibaba-token-plan" {
		t.Fatalf("quotaProvider = %q", got)
	}
	if got := quotaAccessToken(a); got != "a=b" {
		t.Fatalf("quotaAccessToken = %q, want the cookie", got)
	}
}

// 5h and 7d credits gate the account; monthly credits are reported without a
// start and unknown to routing.
func TestAlibabaTokenPlanUsageWindows(t *testing.T) {
	reset := time.UnixMilli(4102444800000)
	windows := alibabaTokenPlanUsageWindows(&alibabatokenplan.Usage{Limits: []alibabatokenplan.UsageLimit{
		{WindowID: "5h", DurationMs: 5 * 3600 * 1000, ResetsAt: reset, UsedFraction: 0.25},
		{WindowID: "7d", DurationMs: 7 * 24 * 3600 * 1000, UsedFraction: 0.5},
		{WindowID: "monthly", ResetsAt: reset, UsedFraction: 1},
	}})
	if len(windows) != 3 {
		t.Fatalf("windows = %+v", windows)
	}
	if w := windows[0]; w.Kind != "5h" || !w.Known || w.Used != 0.25 || w.ResetsAt != reset.UnixMilli() || w.StartsAt != reset.Add(-5*time.Hour).UnixMilli() {
		t.Errorf("5h window = %+v", w)
	}
	if w := windows[1]; w.Kind != "week" || !w.Known || w.Used != 0.5 || w.ResetsAt != 0 || w.StartsAt != 0 {
		t.Errorf("week window = %+v", w)
	}
	if w := windows[2]; w.Kind != "month" || w.Known || w.Used != 1 || w.StartsAt != 0 || w.ResetsAt != reset.UnixMilli() {
		t.Errorf("month window = %+v", w)
	}
}
