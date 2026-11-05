package commandcode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
)

type capturedReply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// usageCapture is testdata/commandcode_usage_capture.json: NeoPi's OWN
// commandCodeUsageProvider.fetchUsage over fixed identity and credits answers.
type usageCapture struct {
	APIKey  string `json:"apiKey"`
	Results []struct {
		Name     string         `json:"name"`
		BaseURL  string         `json:"baseUrl"`
		Whoami   capturedReply  `json:"whoami"`
		Credits  *capturedReply `json:"credits"`
		Requests []struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
		} `json:"requests"`
		Error  *string `json:"error"`
		Report *struct {
			AccountID string   `json:"accountId"`
			Email     string   `json:"email"`
			OrgID     string   `json:"orgId"`
			OrgName   string   `json:"orgName"`
			PlanType  string   `json:"planType"`
			Balance   *float64 `json:"balance"`
			Limits    []struct {
				ID           string   `json:"id"`
				WindowID     string   `json:"windowId"`
				DurationMs   int64    `json:"durationMs"`
				ResetsAt     int64    `json:"resetsAt"`
				Used         float64  `json:"used"`
				Cap          *float64 `json:"cap"`
				UsedFraction *float64 `json:"usedFraction"`
				Status       string   `json:"status"`
			} `json:"limits"`
		} `json:"report"`
	} `json:"results"`
}

func equalOptional(a, b *float64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// Every captured answer yields NeoPi's requests (the identity route, then
// the org's credits route) and NeoPi's report: the credit windows with their
// caps, fractions, resets and statuses and the credit balance; a revoked key
// as an error; and every other failure or unusable answer as nothing.
func TestFetchUsageMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/commandcode_usage_capture.json")
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
				reply := result.Whoami
				if r.URL.Path != whoamiPath {
					reply = capturedReply{Status: http.StatusInternalServerError, Body: json.RawMessage(`{}`)}
					if result.Credits != nil {
						reply = *result.Credits
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
			// The loopback server stands in for the account origin: a
			// configured base keeps its path, which the origin drops.
			base := server.URL
			if parsed, errParse := url.Parse(result.BaseURL); errParse == nil && parsed.Host != "" {
				base = server.URL + parsed.Path
			} else if result.BaseURL != "" && resolveOrigin(result.BaseURL) != defaultOrigin {
				t.Fatalf("an unparseable base resolved to %s", resolveOrigin(result.BaseURL))
			}
			usage, err := FetchUsage(t.Context(), server.Client(), base, capture.APIKey)
			if len(sent) != len(result.Requests) {
				t.Fatalf("requests = %d, NeoPi %d", len(sent), len(result.Requests))
			}
			for i, want := range result.Requests {
				wantURL, _ := url.Parse(want.URL)
				if sent[i].URL.Path != wantURL.Path || sent[i].URL.RawQuery != wantURL.RawQuery || sent[i].Method != want.Method {
					t.Errorf("request %d = %s %s?%s, NeoPi %s %s?%s", i, sent[i].Method, sent[i].URL.Path, sent[i].URL.RawQuery, want.Method, wantURL.Path, wantURL.RawQuery)
				}
				if parsed, errParse := url.Parse(result.BaseURL); errParse == nil && parsed.Host != "" && resolveOrigin(result.BaseURL) != wantURL.Scheme+"://"+wantURL.Host {
					t.Errorf("origin = %s, NeoPi %s://%s", resolveOrigin(result.BaseURL), wantURL.Scheme, wantURL.Host)
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
			want := result.Report
			if usage == nil {
				t.Fatal("usage = nil, NeoPi returned a report")
			}
			if usage.AccountID != want.AccountID || usage.Email != want.Email || usage.OrgID != want.OrgID || usage.OrgName != want.OrgName || usage.PlanType != want.PlanType {
				t.Errorf("identity = %+v, NeoPi %+v", usage, want)
			}
			if want.Balance == nil || usage.Balance != *want.Balance {
				t.Errorf("balance = %v, NeoPi %v", usage.Balance, want.Balance)
			}
			if len(usage.Limits) != len(want.Limits) {
				t.Fatalf("limits = %d, NeoPi %d", len(usage.Limits), len(want.Limits))
			}
			for i, wantLimit := range want.Limits {
				got := usage.Limits[i]
				resetsAt := int64(0)
				if !got.ResetsAt.IsZero() {
					resetsAt = got.ResetsAt.UnixMilli()
				}
				if got.ID != wantLimit.ID || got.WindowID != wantLimit.WindowID || got.DurationMs != wantLimit.DurationMs || resetsAt != wantLimit.ResetsAt ||
					got.Used != wantLimit.Used || !equalOptional(got.Cap, wantLimit.Cap) || !equalOptional(got.UsedFraction, wantLimit.UsedFraction) || got.Status != wantLimit.Status {
					t.Errorf("limit %d = %+v (resets %d), NeoPi %+v", i, got, resetsAt, wantLimit)
				}
			}
		})
	}
}
