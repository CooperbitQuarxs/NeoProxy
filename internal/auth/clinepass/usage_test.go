package clinepass

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

type capturedReply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// usageCapture is testdata/clinepass_usage_capture.json: NeoPi's OWN
// clinePassUsageProvider.fetchUsage over fixed quota and account answers.
type usageCapture struct {
	APIKey  string `json:"apiKey"`
	Results []struct {
		Name     string         `json:"name"`
		BaseURL  string         `json:"baseUrl"`
		Usage    capturedReply  `json:"usage"`
		Account  *capturedReply `json:"account"`
		Requests []struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"requests"`
		Error  *string `json:"error"`
		Report *struct {
			Email     string `json:"email"`
			AccountID string `json:"accountId"`
			Limits    []struct {
				ID           string  `json:"id"`
				WindowID     string  `json:"windowId"`
				DurationMs   int64   `json:"durationMs"`
				ResetsAt     int64   `json:"resetsAt"`
				Used         float64 `json:"used"`
				UsedFraction float64 `json:"usedFraction"`
				Status       string  `json:"status"`
			} `json:"limits"`
		} `json:"report"`
	} `json:"results"`
}

// Every captured answer yields NeoPi's requests (quota route, then the account
// route once a window parsed) and NeoPi's report: the three plan windows in
// percent, a rejected key as an error, and no plan, transient failures or
// unusable reports as nothing.
func TestFetchUsageMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/clinepass_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture usageCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, result := range capture.Results {
		t.Run(result.Name, func(t *testing.T) {
			var mu sync.Mutex
			var sent []*http.Request
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				sent = append(sent, r.Clone(r.Context()))
				mu.Unlock()
				reply := result.Usage
				if strings.HasSuffix(r.URL.Path, "/users/me") {
					reply = capturedReply{Status: http.StatusInternalServerError, Body: json.RawMessage(`{}`)}
					if result.Account != nil {
						reply = *result.Account
					}
				}
				var text string
				body := []byte(reply.Body)
				if json.Unmarshal(reply.Body, &text) == nil {
					body = []byte(text)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(reply.Status)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			base := server.URL + "/api/v1"
			if result.BaseURL != "" {
				parsed, errParse := url.Parse(result.BaseURL)
				if errParse != nil {
					t.Fatal(errParse)
				}
				base = server.URL + parsed.Path
			}
			usage, err := FetchUsage(t.Context(), server.Client(), base, capture.APIKey)
			if len(sent) != len(result.Requests) {
				t.Fatalf("requests = %d, NeoPi %d", len(sent), len(result.Requests))
			}
			for i, want := range result.Requests {
				wantURL, _ := url.Parse(want.URL)
				if sent[i].URL.Path != wantURL.Path || sent[i].Method != want.Method {
					t.Errorf("request %d = %s %s, NeoPi %s %s", i, sent[i].Method, sent[i].URL.Path, want.Method, wantURL.Path)
				}
				for name, value := range want.Headers {
					if got := sent[i].Header.Get(name); got != value {
						t.Errorf("request %d header %s = %q, NeoPi %q", i, name, got, value)
					}
				}
			}
			if result.Error != nil {
				if _, ok := err.(*UsageError); !ok {
					t.Fatalf("err = %v, NeoPi threw %q", err, *result.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, NeoPi returned a report", err)
			}
			if result.Report == nil {
				if usage != nil {
					t.Fatalf("usage = %+v, NeoPi returned nothing", usage)
				}
				return
			}
			if usage == nil {
				t.Fatal("usage = nil, NeoPi returned a report")
			}
			if usage.Email != result.Report.Email || usage.AccountID != result.Report.AccountID {
				t.Errorf("identity = %q/%q, NeoPi %q/%q", usage.Email, usage.AccountID, result.Report.Email, result.Report.AccountID)
			}
			if len(usage.Limits) != len(result.Report.Limits) {
				t.Fatalf("limits = %d, NeoPi %d", len(usage.Limits), len(result.Report.Limits))
			}
			for i, want := range result.Report.Limits {
				got := usage.Limits[i]
				resetsAt := int64(0)
				if !got.ResetsAt.IsZero() {
					resetsAt = got.ResetsAt.UnixMilli()
				}
				if got.ID != want.ID || got.WindowID != want.WindowID || got.DurationMs != want.DurationMs || resetsAt != want.ResetsAt ||
					got.Used != want.Used || got.UsedFraction != want.UsedFraction || got.Status != want.Status {
					t.Errorf("limit %d = %+v (resets %d), NeoPi %+v", i, got, resetsAt, want)
				}
			}
		})
	}
}

// A rejected key stays rejected when its error body cannot be decoded: NeoPi
// classifies 401/403 before it reads the body.
func TestFetchUsageRejectsKeyWithUnreadableBody(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(status)
			_, _ = w.Write([]byte("not gzip"))
		}))
		usage, err := FetchUsage(t.Context(), server.Client(), server.URL, "sk_test")
		server.Close()
		rejected, ok := err.(*UsageError)
		if usage != nil || !ok || rejected.Status != status {
			t.Fatalf("status %d: usage = %+v, err = %v; want a rejected key", status, usage, err)
		}
	}
}
