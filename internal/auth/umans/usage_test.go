package umans

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

// usageCapture is testdata/umans_usage_capture.json: NeoPi's OWN
// umansUsageProvider.fetchUsage over fixed /v1/usage answers.
type usageCapture struct {
	APIKey  string `json:"apiKey"`
	Results []struct {
		Name    string          `json:"name"`
		BaseURL string          `json:"baseUrl"`
		Status  int             `json:"status"`
		Body    json.RawMessage `json:"body"`
		Request struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"request"`
		Error  *string `json:"error"`
		Report *struct {
			Plan   string   `json:"plan"`
			Notes  []string `json:"notes"`
			Limits []struct {
				ID           string   `json:"id"`
				WindowID     string   `json:"windowId"`
				DurationMs   int64    `json:"durationMs"`
				ResetsAt     int64    `json:"resetsAt"`
				Used         *float64 `json:"used"`
				Limit        *float64 `json:"limit"`
				UsedFraction *float64 `json:"usedFraction"`
				Status       string   `json:"status"`
			} `json:"limits"`
		} `json:"report"`
	} `json:"results"`
}

func sameNumber(a, b *float64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// Every captured answer yields NeoPi's request (path, Bearer key, Accept) and
// NeoPi's report: the soft-cap/burst split, single-row fallbacks, the window
// and its reset, concurrency, the deprioritized note, a rejected key as an
// error and transient failures or empty reports as nothing.
func TestFetchUsageMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/umans_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture usageCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, result := range capture.Results {
		t.Run(result.Name, func(t *testing.T) {
			var sent *http.Request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent = r.Clone(context.Background())
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(result.Status)
				_, _ = w.Write(result.Body)
			}))
			defer server.Close()
			base := server.URL
			if result.BaseURL != "" {
				parsed, errParse := url.Parse(result.BaseURL)
				if errParse != nil {
					t.Fatal(errParse)
				}
				base = server.URL + parsed.Path
			}
			usage, err := FetchUsage(context.Background(), server.Client(), base, capture.APIKey)
			want, _ := url.Parse(result.Request.URL)
			if sent.Method != result.Request.Method || sent.URL.Path != want.Path {
				t.Errorf("request = %s %s, want %s %s", sent.Method, sent.URL.Path, result.Request.Method, want.Path)
			}
			for name, value := range result.Request.Headers {
				if got := sent.Header.Get(name); got != value {
					t.Errorf("header %s = %q, want %q", name, got, value)
				}
			}
			if result.Error != nil {
				if err == nil || err.Error() != *result.Error+" Unauthorized" {
					t.Fatalf("err = %v, want %q", err, *result.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Report == nil {
				if usage != nil {
					t.Fatalf("usage = %+v, want none", usage)
				}
				return
			}
			if usage == nil {
				t.Fatal("usage = nil")
			}
			if usage.Plan != result.Report.Plan || usage.Deprioritized != (len(result.Report.Notes) > 0) {
				t.Errorf("plan/deprioritized = %q/%v, want %q/%v", usage.Plan, usage.Deprioritized, result.Report.Plan, result.Report.Notes)
			}
			if len(usage.Limits) != len(result.Report.Limits) {
				t.Fatalf("limits = %d, want %d", len(usage.Limits), len(result.Report.Limits))
			}
			for i, w := range result.Report.Limits {
				g := usage.Limits[i]
				resets := int64(0)
				if !g.ResetsAt.IsZero() {
					resets = g.ResetsAt.UnixMilli()
				}
				if g.ID != w.ID || g.WindowID != w.WindowID || g.DurationMs != w.DurationMs || resets != w.ResetsAt || g.Status != w.Status ||
					!sameNumber(g.Used, w.Used) || !sameNumber(g.Limit, w.Limit) || !sameNumber(g.UsedFraction, w.UsedFraction) {
					t.Errorf("limit %d = %+v (resets %d), want %+v", i, g, resets, w)
				}
			}
		})
	}
}
