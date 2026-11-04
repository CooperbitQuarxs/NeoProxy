package neopool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

func windowByKind(t *testing.T, windows []Window, kind string) Window {
	t.Helper()
	for _, w := range windows {
		if w.Kind == kind {
			return w
		}
	}
	t.Fatalf("no %s window in %+v", kind, windows)
	return Window{}
}

func TestQuotaClaudeUsageShape(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer claude-secret" {
			t.Error("usage request did not select the account credential")
		}
		if r.URL.Path != "/api/oauth/usage" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":42.4,"resets_at":"2026-10-28T15:00:00.000000+00:00"},"seven_day":{"utilization":7,"resets_at":"2026-11-02T00:00:00Z"},"seven_day_opus":null}`))
	}))
	defer server.Close()
	q := newQuotaProber()
	q.claudeURL = server.URL + "/api/oauth/usage"
	a := &coreauth.Auth{ID: "claude-one.json", Provider: "claude", Metadata: map[string]any{"access_token": "claude-secret"}}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	if err := q.refresh(context.Background(), a, now); err != nil {
		t.Fatal(err)
	}
	windows := q.windows(a, now)
	five := windowByKind(t, windows, "5h")
	if !five.Known || five.Source != "provider-reported" || five.Unit != "percent" || five.Capacity != 100 || five.UsedTokens != 42 || five.Used != .424 {
		t.Fatalf("5h window: %+v", five)
	}
	reset := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	if five.ResetsAt != reset.UnixMilli() || five.StartsAt != reset.Add(-5*time.Hour).UnixMilli() {
		t.Fatalf("5h bounds: %+v", five)
	}
	week := windowByKind(t, windows, "week")
	if week.UsedTokens != 7 || week.Used != .07 || week.ResetsAt != time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("week window: %+v", week)
	}
}

func TestQuotaClaudeGenericLimitsDoNotPromoteModelCapsToSharedBudgets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"five_hour":null,"seven_day":null,"limits":[{"kind":"session","percent":12,"is_active":false},{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`))
	}))
	defer server.Close()
	q := newQuotaProber()
	q.claudeURL = server.URL + "/api/oauth/usage"
	a := &coreauth.Auth{ID: "claude-generic.json", Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-oat-api-key-entry"}}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if err := q.refresh(context.Background(), a, now); err != nil {
		t.Fatal(err)
	}
	windows := q.windows(a, now)
	if len(windows) != 1 || windows[0].Kind != "5h" || windows[0].Used != .12 || !windows[0].Known {
		t.Fatalf("model-specific cap or missing week became an account-wide budget: %+v", windows)
	}
}

func TestQuotaCodexUsageShape(t *testing.T) {
	token := fakeJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc-777"}})
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(3 * 24 * time.Hour).Unix()
	var gotAuth, gotAccount string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccount = r.Header.Get("Authorization"), r.Header.Get("chatgpt-account-id")
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		body, _ := json.Marshal(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			// NeoPi reads reset_at first (seconds or milliseconds), then reset_after_seconds.
			"primary_window":   map[string]any{"used_percent": 12.5, "limit_window_seconds": 18000, "reset_after_seconds": 600},
			"secondary_window": map[string]any{"used_percent": "80", "limit_window_seconds": 604800, "reset_after_seconds": 1, "reset_at": resetAt * 1000},
		}})
		_, _ = w.Write(body)
	}))
	defer server.Close()
	q := newQuotaProber()
	q.codexURL = server.URL + "/backend-api/wham/usage"
	a := &coreauth.Auth{ID: "codex-one.json", Provider: "codex", Metadata: map[string]any{"access_token": token}}
	if err := q.refresh(context.Background(), a, now); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+token || gotAccount != "acc-777" {
		t.Fatalf("request headers: %q %q", gotAuth, gotAccount)
	}
	windows := q.windows(a, now)
	five := windowByKind(t, windows, "5h")
	if !five.Known || five.UsedTokens != 13 || five.Used != .125 || five.ResetsAt != now.Add(10*time.Minute).UnixMilli() || five.StartsAt != now.Add(10*time.Minute-5*time.Hour).UnixMilli() {
		t.Fatalf("5h window: %+v", five)
	}
	week := windowByKind(t, windows, "week")
	if week.UsedTokens != 80 || week.Used != .8 || week.ResetsAt != resetAt*1000 || week.Source != "provider-reported" {
		t.Fatalf("week window: %+v", week)
	}
}

// Kimi gates on the first 5h and the first 7d limit, as NeoPi's ranking reads
// them: later duplicates and non-gating windows never become budgets, and a
// limit without a duration spans its window's default.
func TestQuotaKimiWindowsFollowNeoPiRanking(t *testing.T) {
	reset := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	fraction := func(v float64) *float64 { return &v }
	windows := kimiUsageWindows(&kimiauth.Usage{Limits: []kimiauth.UsageLimit{
		{Label: "Weekly limit", WindowID: "7d", ResetsAt: reset, UsedFraction: fraction(.37)},
		{Label: "5h limit", WindowID: "5h", DurationMs: (5 * time.Hour).Milliseconds(), ResetsAt: reset, UsedFraction: fraction(.12)},
		{Label: "Total quota", WindowID: "7d", UsedFraction: fraction(.99)},
		{Label: "Daily", WindowID: "1d", UsedFraction: fraction(1)},
		{Label: "Monthly total", WindowID: "limit_month_total", UsedFraction: fraction(1)},
	}})
	if len(windows) != 2 {
		t.Fatalf("windows = %+v, want 5h and week only", windows)
	}
	week := windowByKind(t, windows, "week")
	if !week.Known || week.Used != .37 || week.ResetsAt != reset.UnixMilli() || week.StartsAt != reset.Add(-7*24*time.Hour).UnixMilli() {
		t.Fatalf("week window: %+v", week)
	}
	five := windowByKind(t, windows, "5h")
	if five.Used != .12 || five.StartsAt != reset.Add(-5*time.Hour).UnixMilli() {
		t.Fatalf("5h window: %+v", five)
	}
	unknown := kimiUsageWindows(&kimiauth.Usage{Limits: []kimiauth.UsageLimit{{Label: "5h limit", WindowID: "5h"}}})
	if len(unknown) != 1 || unknown[0].Known || unknown[0].Kind != "5h" {
		t.Fatalf("fractionless limit: %+v", unknown)
	}
}

// A provider that fails keeps the last good reading for 30 minutes and then
// reports unknown windows; the snapshot carries both, without any sleeping.
func TestQuotaSnapshotKeepsLastGoodThenUnknown(t *testing.T) {
	f := newFixture(t)
	healthy := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy {
			http.Error(w, "boom private-body", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":30,"limit_window_seconds":18000,"reset_after_seconds":60}}}`))
	}))
	defer server.Close()
	f.pool.quota.codexURL = server.URL
	ctx := context.Background()
	a, ok := f.manager.GetByID(f.accounts[0].EngineAuthID)
	if !ok {
		t.Fatal("missing engine auth")
	}
	if supported, err := f.pool.RefreshQuota(ctx, a); !supported || err != nil {
		t.Fatalf("refresh: %v %v", supported, err)
	}
	provider := func() []Window {
		snap, err := f.pool.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, account := range snap.Accounts {
			if account.ID == f.accounts[0].ID {
				return account.ProviderWindows
			}
		}
		t.Fatal("account missing from snapshot")
		return nil
	}
	if w := provider(); len(w) != 1 || !w[0].Known || w[0].Used != .3 {
		t.Fatalf("fresh reading: %+v", w)
	}
	healthy = false
	f.at = f.at.Add(29 * time.Minute)
	if _, err := f.pool.RefreshQuota(ctx, a); err == nil {
		t.Fatal("failing provider reported success")
	}
	if w := provider(); len(w) != 1 || !w[0].Known || w[0].Used != .3 {
		t.Fatalf("last good reading dropped early: %+v", w)
	}
	f.at = f.at.Add(2 * time.Minute)
	if w := provider(); len(w) != 1 || w[0].Known || w[0].Source != "provider-unavailable" || w[0].Kind != "5h" {
		t.Fatalf("stale reading still known: %+v", w)
	}
	// The other account was never asked, so it keeps showing no provider windows.
	snap, err := f.pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range snap.Accounts {
		if account.ID == f.accounts[1].ID && account.ProviderWindows != nil {
			t.Fatalf("unasked account got windows: %+v", account.ProviderWindows)
		}
	}
}

func TestQuotaIgnoresNonNativeCredentials(t *testing.T) {
	for _, a := range []*coreauth.Auth{
		{ID: "gw", Provider: "codex", Attributes: map[string]string{"neopi_source_id": "src"}, Metadata: map[string]any{"access_token": "x"}},
		{ID: "key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-ant-test-key-0000000000"}},
		{ID: "other", Provider: "gemini", Metadata: map[string]any{"access_token": "x"}},
	} {
		if got := quotaProvider(a); got != "" {
			t.Fatalf("%s: unexpected quota provider %q", a.ID, got)
		}
	}
}
