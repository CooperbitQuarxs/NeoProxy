package xai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestFetchUsageMatchesNeoPiSource replays NeoPi's own xaiOauthUsageProvider
// captures (testdata/capture_neopi_xai_usage.ts): the billing requests and the
// weekly, monthly and on-demand figures it reports.
func TestFetchUsageMatchesNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_xai_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Token     string `json:"token"`
		Scenarios []struct {
			Name    string          `json:"name"`
			Credits json.RawMessage `json:"credits"`
			Monthly json.RawMessage `json:"monthly"`
			Limits  []struct {
				ID       string  `json:"id"`
				Used     float64 `json:"used"`
				Limit    float64 `json:"limit"`
				ResetsAt int64   `json:"resetsAt"`
			} `json:"limits"`
			Requests []capturedRequest `json:"requests"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			asked := make(chan string, 4)
			var got []capturedRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				headers := map[string]string{}
				for name, values := range r.Header {
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				got = append(got, capturedRequest{Method: r.Method, URL: <-asked, Headers: headers, Body: string(body)})
				payload := scenario.Monthly
				if r.URL.Query().Get("format") == "credits" {
					payload = scenario.Credits
				}
				if len(payload) == 0 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				var compact bytes.Buffer
				_ = json.Compact(&compact, payload)
				_, _ = w.Write(compact.Bytes())
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			client := &http.Client{Transport: redirect{target: target, asked: asked}}
			usage, errFetch := FetchUsage(context.Background(), client, capture.Token, time.Now())
			for i := range scenario.Requests {
				delete(scenario.Requests[i].Headers, "host")
			}
			if !reflect.DeepEqual(got, scenario.Requests) {
				t.Errorf("requests\ngot    %+v\nsource %+v", got, scenario.Requests)
			}
			if scenario.Limits == nil {
				if errFetch == nil {
					t.Fatalf("usage %+v; source reported nothing", usage)
				}
				return
			}
			if errFetch != nil {
				t.Fatal(errFetch)
			}
			for _, limit := range scenario.Limits {
				switch limit.ID {
				case "xai-oauth:credits:1w":
					if usage.Weekly == nil || usage.Weekly.CreditUsagePercent != limit.Used || usage.Weekly.PeriodEnd.UnixMilli() != limit.ResetsAt {
						t.Errorf("weekly %+v; source %+v", usage.Weekly, limit)
					}
				case "xai-oauth:included:1mo":
					if usage.Monthly == nil || usage.Monthly.Used != limit.Used || usage.Monthly.Limit != limit.Limit || usage.Monthly.PeriodEnd.UnixMilli() != limit.ResetsAt {
						t.Errorf("monthly %+v; source %+v", usage.Monthly, limit)
					}
				case "xai-oauth:on-demand":
					if usage.OnDemand == nil || usage.OnDemand.Used != limit.Used || usage.OnDemand.Cap != limit.Limit {
						t.Errorf("on-demand %+v; source %+v", usage.OnDemand, limit)
					}
				}
			}
		})
	}
}
