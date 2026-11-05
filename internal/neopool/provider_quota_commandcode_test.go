package neopool

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A Command Code report gates routing as NeoPi's rotation reads it: an
// exceeded window is exhausted whatever its fraction, a window without a cap
// is reported but unknown, and an empty credit balance exhausts the account
// while a positive one adds no meter.
func TestCommandCodeUsageWindowsGateLikeNeoPi(t *testing.T) {
	low, high := 0.2, 0.5
	resets := time.UnixMilli(1793854800000)
	usage := &commandcode.Usage{Balance: 0, Limits: []commandcode.UsageLimit{
		{ID: "commandcode:5h", WindowID: "5h", DurationMs: int64(5 * time.Hour / time.Millisecond), ResetsAt: resets, UsedFraction: &low, Status: "exhausted"},
		{ID: "commandcode:7d", WindowID: "7d", DurationMs: int64(7 * 24 * time.Hour / time.Millisecond), UsedFraction: &high, Status: "ok"},
	}}
	windows := commandCodeUsageWindows(usage)
	if len(windows) != 3 {
		t.Fatalf("windows = %+v, want 5h, week and the empty balance", windows)
	}
	if w := windows[0]; w.Kind != "5h" || !w.Known || w.Used != 1 || w.ResetsAt != resets.UnixMilli() || w.StartsAt != resets.Add(-5*time.Hour).UnixMilli() {
		t.Errorf("exceeded 5h window = %+v, want exhausted until its reset", w)
	}
	if w := windows[1]; w.Kind != "week" || !w.Known || w.Used != 0.5 {
		t.Errorf("weekly window = %+v, want half used", w)
	}
	if w := windows[2]; w.Kind != "month" || !w.Known || w.Used != 1 {
		t.Errorf("balance window = %+v, want an exhausted gate", w)
	}

	usage = &commandcode.Usage{Balance: 12, Limits: []commandcode.UsageLimit{{ID: "commandcode:5h", WindowID: "5h", Status: "unknown"}}}
	windows = commandCodeUsageWindows(usage)
	if len(windows) != 1 || windows[0].Kind != "5h" || windows[0].Known {
		t.Fatalf("windows = %+v, want one unknown 5h window and no balance meter", windows)
	}

	// A present cap at or below zero leaves the fraction unknown, and NeoPi's
	// isUsageLimitExhausted then falls back to used >= limit.
	zero := 0.0
	usage = &commandcode.Usage{Balance: 12, Limits: []commandcode.UsageLimit{{ID: "commandcode:5h", WindowID: "5h", Used: 3, Cap: &zero, Status: "unknown"}}}
	if windows = commandCodeUsageWindows(usage); len(windows) != 1 || !windows[0].Known || windows[0].Used != 1 {
		t.Fatalf("windows = %+v, want the zero-cap 5h window exhausted", windows)
	}
}

// NeoPi's null report (a transient failure) keeps the last good reading, a
// revoked key's 401 purges it, and the probe carries no deadline once
// connected (AGENTS.md).
func TestCommandCodeQuotaRetainsLastGoodUntilRevoked(t *testing.T) {
	a := &coreauth.Auth{ID: t.Name(), Provider: commandcode.Provider, Attributes: map[string]string{"api_key": "cc_quota", "base_url": "https://api.commandcode.ai/provider"}}
	if client := (&quotaClients{clients: map[string]*http.Client{}}).client(a); client.Timeout != 0 {
		t.Errorf("usage probe timeout = %v, want none", client.Timeout)
	}
	credits := struct {
		status int
		body   string
	}{http.StatusOK, `{"credits":{"monthlyCredits":5},"windowLimits":{"fiveHour":{"used":2,"cap":10}}}`}
	q := newQuotaProber()
	client := &http.Client{Transport: copilotQuotaRoundTrip(func(req *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, `{"user":{"id":"u1"}}`
		if req.URL.Path == "/alpha/billing/credits" {
			status, body = credits.status, credits.body
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: req}, nil
	})}
	q.clientFor = func(*coreauth.Auth) *http.Client { return client }
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := q.refresh(t.Context(), a, now); err != nil {
		t.Fatal(err)
	}
	if w := q.windows(a, now); len(w) != 1 || !w[0].Known || w[0].Used != 0.2 {
		t.Fatalf("windows after a good report = %+v", w)
	}
	credits.status, credits.body = http.StatusServiceUnavailable, "{}"
	if err := q.refresh(t.Context(), a, now.Add(time.Minute)); err == nil {
		t.Fatal("an unavailable report succeeded")
	}
	if w := q.windows(a, now.Add(time.Minute)); len(w) != 1 || !w[0].Known || w[0].Used != 0.2 {
		t.Fatalf("windows after a transient failure = %+v, want the last good reading", w)
	}
	credits.status, credits.body = http.StatusUnauthorized, `{"error":"revoked"}`
	if err := q.refresh(t.Context(), a, now.Add(2*time.Minute)); err == nil {
		t.Fatal("a revoked key's report succeeded")
	}
	if w := q.windows(a, now.Add(2*time.Minute)); len(w) != 1 || w[0].Known {
		t.Fatalf("windows after a revoked key = %+v, want unknown", w)
	}
}
