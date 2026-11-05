package kilo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type loginCapture struct {
	DeviceAuthBaseURL string `json:"deviceAuthBaseUrl"`
	Results           []struct {
		Name    string `json:"name"`
		Replies []struct {
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		} `json:"replies"`
		Requests []struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"requests"`
		Shown *struct {
			URL          string `json:"url"`
			Instructions string `json:"instructions"`
		} `json:"shown"`
		Result struct {
			Token  string `json:"token"`
			Error  string `json:"error"`
			Kind   string `json:"kind"`
			Status *int   `json:"status"`
		} `json:"result"`
	} `json:"results"`
}

// TestLoginMatchesNeoPiCapture replays NeoPi's own loginKilo runs: the same
// requests in the same order, the same code shown, and the same token or
// error, kind and status.
func TestLoginMatchesNeoPiCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/kilo_login_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture loginCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.DeviceAuthBaseURL != DeviceAuthBaseURL || len(capture.Results) == 0 {
		t.Fatalf("capture base %q with %d results", capture.DeviceAuthBaseURL, len(capture.Results))
	}
	for _, scenario := range capture.Results {
		t.Run(scenario.Name, func(t *testing.T) {
			type seen struct{ method, path, contentType string }
			var requests []seen
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				requests = append(requests, seen{r.Method, r.URL.Path, r.Header.Get("Content-Type")})
				reply := scenario.Replies[min(len(requests)-1, len(scenario.Replies)-1)]
				w.WriteHeader(reply.Status)
				if len(reply.Body) > 0 && string(reply.Body) != "null" {
					var text string
					if json.Unmarshal(reply.Body, &text) == nil {
						_, _ = io.WriteString(w, text)
					} else {
						_, _ = w.Write(reply.Body)
					}
				}
			}))
			defer server.Close()
			base := server.URL + "/api/device-auth"
			clock := time.Unix(0, 0)
			now := func() time.Time { return clock }
			wait := func(context.Context, time.Duration) error { clock = clock.Add(PollInterval); return nil }

			var token string
			code, err := StartDeviceFlow(context.Background(), server.Client(), base)
			if err == nil {
				if scenario.Shown == nil || scenario.Shown.URL != code.VerificationURL || scenario.Shown.Instructions != "Enter code: "+code.Code {
					t.Errorf("shown %+v, NeoPi %+v", code, scenario.Shown)
				}
				token, err = PollForToken(context.Background(), server.Client(), base, code, now, wait)
			}
			if scenario.Result.Error != "" {
				var loginErr *Error
				if !errors.As(err, &loginErr) || loginErr.Message != scenario.Result.Error || loginErr.Kind != scenario.Result.Kind {
					t.Fatalf("error = %v, NeoPi %q (%s)", err, scenario.Result.Error, scenario.Result.Kind)
				}
				if want := scenario.Result.Status; want != nil && loginErr.Status != *want {
					t.Errorf("status = %d, NeoPi %d", loginErr.Status, *want)
				}
			} else if err != nil || token != scenario.Result.Token {
				t.Fatalf("token = %q, %v, NeoPi %q", token, err, scenario.Result.Token)
			}
			if len(requests) != len(scenario.Requests) {
				t.Fatalf("requests = %d, NeoPi %d", len(requests), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				got := requests[i]
				if got.method != want.Method || "https://api.kilo.ai"+got.path != want.URL || got.contentType != want.Headers["content-type"] {
					t.Errorf("request %d = %s %s (%q), NeoPi %s %s (%q)", i, got.method, got.path, got.contentType, want.Method, want.URL, want.Headers["content-type"])
				}
			}
		})
	}
}

// The poll stops at the code's expiry with NeoPi's timeout error.
func TestPollTimesOutAtExpiry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	clock := time.Unix(0, 0)
	polls := 0
	wait := func(context.Context, time.Duration) error { clock = clock.Add(PollInterval); polls++; return nil }
	_, err := PollForToken(context.Background(), server.Client(), server.URL, &DeviceCode{Code: "c", ExpiresIn: 12}, func() time.Time { return clock }, wait)
	var loginErr *Error
	if !errors.As(err, &loginErr) || loginErr.Kind != "timeout" || !strings.Contains(loginErr.Message, "timed out") {
		t.Fatalf("error = %v, want the timeout", err)
	}
	if polls != 3 {
		t.Errorf("waited %d times, want 3 (polls at 0s, 5s, 10s)", polls)
	}
}
