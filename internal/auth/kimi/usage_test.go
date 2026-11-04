package kimi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The usage request matches NeoPi's (the fingerprint's host-derived values
// are this machine's), and every payload reports the same limits, windows,
// resets and fractions, including a plan-less account.
func TestUsageMatchesNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_kimi_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Token       string            `json:"token"`
		KimiHeaders map[string]string `json:"kimiHeaders"`
		Scenarios   []struct {
			Name    string          `json:"name"`
			Status  int             `json:"status"`
			Payload json.RawMessage `json:"payload"`
			Limits  []struct {
				Label        string   `json:"label"`
				WindowID     *string  `json:"windowId"`
				DurationMs   *int64   `json:"durationMs"`
				ResetsAt     *int64   `json:"resetsAt"`
				UsedFraction *float64 `json:"usedFraction"`
			} `json:"limits"`
			Request struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"request"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	authDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(authDir, deviceIDFileName), []byte(capture.KimiHeaders["X-Msh-Device-Id"]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint := CommonHeaders(authDir)
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var seen *http.Request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = r.Clone(r.Context())
				status := scenario.Status
				if status == 0 {
					status = http.StatusOK
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(scenario.Payload)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			usage, err := FetchUsage(context.Background(), &http.Client{Transport: redirect{target: target, asked: make(chan string, 1)}}, authDir, capture.Token, time.Now())
			if seen == nil {
				t.Fatal("no usage request")
			}
			if got := "https://api.kimi.com" + seen.URL.Path; got != scenario.Request.URL || seen.Method != scenario.Request.Method {
				t.Errorf("request = %s %s, want %s %s", seen.Method, got, scenario.Request.Method, scenario.Request.URL)
			}
			want := map[string]string{}
			for name, value := range scenario.Request.Headers {
				if name != "host" {
					want[name] = value
				}
			}
			for _, name := range []string{"X-Msh-Device-Name", "X-Msh-Device-Model", "X-Msh-Os-Version"} {
				want[strings.ToLower(name)] = fingerprint.Get(name)
			}
			for name, value := range want {
				if got := seen.Header.Get(name); got != value {
					t.Errorf("header %s = %q, want %q", name, got, value)
				}
			}
			for name := range seen.Header {
				if _, ok := want[strings.ToLower(name)]; !ok && !strings.EqualFold(name, "X-Capture-Url") {
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
			if len(usage.Limits) != len(scenario.Limits) {
				t.Fatalf("limits = %+v, want %d", usage.Limits, len(scenario.Limits))
			}
			for i, want := range scenario.Limits {
				got := usage.Limits[i]
				windowID := ""
				if want.WindowID != nil {
					windowID = *want.WindowID
				}
				if got.Label != want.Label || got.WindowID != windowID {
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
