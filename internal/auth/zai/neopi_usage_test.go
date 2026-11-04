package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// usageCapture is testdata/neopi_zai_usage_capture.json: NeoPi's OWN
// zaiUsageProvider over synthetic quota payloads.
type usageCapture struct {
	Key       string `json:"key"`
	Scenarios []struct {
		Name   string          `json:"name"`
		Status int             `json:"status"`
		Quota  json.RawMessage `json:"quota"`
		Plan   *string         `json:"plan"`
		Limits []struct {
			ID           string   `json:"id"`
			WindowID     string   `json:"windowId"`
			DurationMs   *int64   `json:"durationMs"`
			ResetsAt     *int64   `json:"resetsAt"`
			UsedFraction *float64 `json:"usedFraction"`
			Shared       bool     `json:"shared"`
		} `json:"limits"`
		QuotaRequest struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"quotaRequest"`
	} `json:"scenarios"`
}

// The quota request matches NeoPi's, and every payload projects to the same
// windows, fractions, resets and sharing, including a coding-plan-less account.
func TestUsageMatchesNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_zai_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture usageCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var seen *http.Request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Clone(r.Context())
				seen.URL.Host = "api.z.ai"
				status := scenario.Status
				if status == 0 {
					status = http.StatusOK
				}
				var compact bytes.Buffer
				_ = json.Compact(&compact, scenario.Quota)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(compact.Bytes())
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			usage, err := FetchUsage(context.Background(), &http.Client{Transport: redirect{target: target}}, capture.Key)
			if seen == nil {
				t.Fatal("no quota request")
			}
			if got := "https://api.z.ai" + seen.URL.Path; got != scenario.QuotaRequest.URL || seen.Method != scenario.QuotaRequest.Method {
				t.Errorf("request = %s %s, want %s %s", seen.Method, got, scenario.QuotaRequest.Method, scenario.QuotaRequest.URL)
			}
			for name, want := range scenario.QuotaRequest.Headers {
				if name == "host" {
					continue
				}
				if got := seen.Header.Get(name); got != want {
					t.Errorf("header %s = %q, want %q", name, got, want)
				}
			}
			for name := range seen.Header {
				if _, ok := scenario.QuotaRequest.Headers[strings.ToLower(name)]; !ok && !strings.EqualFold(name, "X-Capture-Url") {
					t.Errorf("extra header %s", name)
				}
			}
			if scenario.Limits == nil {
				if usage != nil {
					t.Fatalf("usage = %+v, want none", usage)
				}
				return
			}
			if err != nil || usage == nil {
				t.Fatalf("FetchUsage() = %+v, %v", usage, err)
			}
			if plan := ""; scenario.Plan != nil {
				plan = *scenario.Plan
				if usage.Plan != plan {
					t.Errorf("plan = %q, want %q", usage.Plan, plan)
				}
			}
			if len(usage.Limits) != len(scenario.Limits) {
				t.Fatalf("limits = %+v, want %d", usage.Limits, len(scenario.Limits))
			}
			for i, want := range scenario.Limits {
				got := usage.Limits[i]
				if !strings.HasSuffix(want.ID, ":"+got.WindowID) || !strings.HasPrefix(want.ID, "zai:"+got.Kind+":") || got.WindowID != want.WindowID || got.Shared != want.Shared {
					t.Errorf("limit %d = %+v, want %+v", i, got, want)
				}
				if (want.DurationMs == nil) != (got.DurationMs == 0) || want.DurationMs != nil && *want.DurationMs != got.DurationMs {
					t.Errorf("limit %d duration = %d, want %v", i, got.DurationMs, want.DurationMs)
				}
				if (want.ResetsAt == nil) != got.ResetsAt.IsZero() || want.ResetsAt != nil && *want.ResetsAt != got.ResetsAt.UnixMilli() {
					t.Errorf("limit %d resets = %v, want %v", i, got.ResetsAt, want.ResetsAt)
				}
				if (want.UsedFraction == nil) != (got.UsedFraction == nil) || want.UsedFraction != nil && *want.UsedFraction != *got.UsedFraction {
					t.Errorf("limit %d fraction = %v, want %v", i, got.UsedFraction, want.UsedFraction)
				}
			}
		})
	}
}
