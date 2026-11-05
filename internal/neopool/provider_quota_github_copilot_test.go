package neopool

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/githubcopilot"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type copilotQuotaRoundTrip func(*http.Request) (*http.Response, error)

func (f copilotQuotaRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// Copilot's quotas gate the whole account (NeoPi's default ranking, no model
// scope) at the most consumed one, in one monthly window that names only its
// reset; unlimited quotas measure nothing, and an empty report shows nothing.
func TestGitHubCopilotUsageWindows(t *testing.T) {
	fraction := func(v float64) *float64 { return &v }
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	window := &githubcopilot.UsageWindow{ID: "monthly", Label: "Monthly", ResetsAt: reset.UnixMilli()}
	windows := githubCopilotUsageWindows(&githubcopilot.Usage{Limits: []githubcopilot.UsageLimit{
		{ID: "copilot:premium", Window: window, Amount: githubcopilot.UsageAmount{UsedFraction: fraction(0.4)}},
		{ID: "copilot:chat", Window: window, Amount: githubcopilot.UsageAmount{UsedFraction: fraction(0.8)}},
		{ID: "copilot:completions", Window: window, Amount: githubcopilot.UsageAmount{UsedFraction: fraction(0.05)}},
	}})
	if len(windows) != 1 {
		t.Fatalf("windows = %+v", windows)
	}
	if w := windows[0]; w.Kind != "month" || !w.Known || w.Used != 0.8 || w.ResetsAt != reset.UnixMilli() || w.StartsAt != 0 || len(w.Models) != 0 {
		t.Errorf("month window = %+v", w)
	}
	unmeasured := githubCopilotUsageWindows(&githubcopilot.Usage{Limits: []githubcopilot.UsageLimit{{ID: "copilot:premium", Window: window, Notes: []string{"Unlimited"}}}})
	if len(unmeasured) != 1 || unmeasured[0].Known || unmeasured[0].Kind != "month" {
		t.Errorf("unlimited windows = %+v", unmeasured)
	}
	if empty := githubCopilotUsageWindows(&githubcopilot.Usage{}); len(empty) != 0 {
		t.Errorf("empty report windows = %+v", empty)
	}
}

// A transient usage failure keeps the last good reading; a report refused
// with 401 or 403 drops it at once (NeoPi's rethrowAuthRejection), and the
// probe carries no deadline once connected (AGENTS.md).
func TestGitHubCopilotQuotaRefusalDropsLastGood(t *testing.T) {
	a := &coreauth.Auth{ID: t.Name(), Provider: githubcopilot.Provider, Metadata: map[string]any{"type": "github-copilot", "access_token": "gho_quota", "refresh_token": "gho_quota"}}
	if client := (&quotaClients{clients: map[string]*http.Client{}}).client(a); client.Timeout != 0 {
		t.Errorf("usage probe timeout = %v, want none", client.Timeout)
	}
	internal := struct {
		status int
		body   string
	}{http.StatusOK, `{"copilot_plan":"individual","quota_reset_date":"2026-12-01","quota_snapshots":{"premium_interactions":{"entitlement":300,"remaining":180,"percent_remaining":60,"unlimited":false}}}`}
	q := newQuotaProber()
	client := &http.Client{Transport: copilotQuotaRoundTrip(func(req *http.Request) (*http.Response, error) {
		status, body := http.StatusOK, `{"login":"octocat"}`
		if strings.HasSuffix(req.URL.Path, "/copilot_internal/user") {
			status, body = internal.status, internal.body
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: req}, nil
	})}
	q.clientFor = func(*coreauth.Auth) *http.Client { return client }
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := q.refresh(t.Context(), a, now); err != nil {
		t.Fatal(err)
	}
	if w := q.windows(a, now); len(w) != 1 || !w[0].Known || w[0].Used != 0.4 {
		t.Fatalf("windows after a good report = %+v", w)
	}
	internal.status, internal.body = http.StatusInternalServerError, "{}"
	if err := q.refresh(t.Context(), a, now.Add(time.Minute)); err == nil {
		t.Fatal("a 500 report succeeded")
	}
	if w := q.windows(a, now.Add(time.Minute)); len(w) != 1 || !w[0].Known || w[0].Used != 0.4 {
		t.Fatalf("windows after a transient failure = %+v, want the last good reading", w)
	}
	internal.status, internal.body = http.StatusUnauthorized, `{"message":"Bad credentials"}`
	if err := q.refresh(t.Context(), a, now.Add(2*time.Minute)); err == nil {
		t.Fatal("a refused report succeeded")
	}
	if w := q.windows(a, now.Add(2*time.Minute)); len(w) != 1 || w[0].Known {
		t.Fatalf("windows after a refused report = %+v, want unknown", w)
	}
}
