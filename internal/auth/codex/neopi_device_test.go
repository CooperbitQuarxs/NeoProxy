package codex

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

func TestCodexNeoPiDeviceCaptureParity(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_device_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scenarios []struct {
			Name        string          `json:"name"`
			Init        json.RawMessage `json:"init"`
			InitStatus  int             `json:"initStatus"`
			Tokens      json.RawMessage `json:"tokens"`
			TokenStatus int             `json:"tokenStatus"`
			Polls       []struct {
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			} `json:"polls"`
			Cancel bool `json:"cancel"`
			Auth   struct {
				URL      string `json:"url"`
				UserCode string `json:"userCode"`
			} `json:"auth"`
			Credential neopiOAuthCredential `json:"credential"`
			Error      string               `json:"error"`
			Waits      []int64              `json:"waits"`
			Requests   []neopiOAuthRequest  `json:"requests"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			captured := make(chan neopiOAuthRequest, len(scenario.Requests)+1)
			poll := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
				}
				headers := map[string]string{"host": r.Host}
				for name, values := range r.Header {
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				captured <- neopiOAuthRequest{Method: r.Method, URL: "https://" + r.Host + r.URL.RequestURI(), Headers: headers, Body: string(body)}
				var status int
				var response json.RawMessage
				switch r.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					status, response = scenario.InitStatus, scenario.Init
				case "/api/accounts/deviceauth/token":
					if len(scenario.Polls) == 0 {
						t.Error("unexpected device poll")
						http.Error(w, "unexpected poll", 500)
						return
					}
					step := scenario.Polls[min(poll, len(scenario.Polls)-1)]
					poll++
					status, response = step.Status, step.Body
				case "/oauth/token":
					status, response = scenario.TokenStatus, scenario.Tokens
				default:
					t.Errorf("unexpected URL %s", r.URL)
					http.Error(w, "unexpected URL", 500)
					return
				}
				if status == 0 {
					status = http.StatusOK
				}
				if len(response) == 0 {
					response = json.RawMessage(`{}`)
				}
				var compact bytes.Buffer
				if errCompact := json.Compact(&compact, response); errCompact != nil {
					t.Error(errCompact)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(compact.Bytes())
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				clone := r.Clone(r.Context())
				clone.Host = r.URL.Host
				clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
				return http.DefaultTransport.RoundTrip(clone)
			})}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waits := []int64{}
			before := time.Now()
			session, callErr := auth.StartDevice(ctx)
			var bundle *CodexAuthBundle
			if callErr == nil {
				if session.URL != scenario.Auth.URL || session.UserCode != scenario.Auth.UserCode {
					t.Errorf("browser authorization got %q/%q, want %#v", session.URL, session.UserCode, scenario.Auth)
				}
				bundle, callErr = auth.waitForDevice(ctx, session, func(_ context.Context, delay time.Duration) error {
					waits = append(waits, delay.Milliseconds())
					if scenario.Cancel {
						cancel()
					}
					return nil
				})
			}
			after := time.Now()
			requests := make([]neopiOAuthRequest, 0, len(captured))
			for len(captured) > 0 {
				requests = append(requests, <-captured)
			}
			if !reflect.DeepEqual(requests, scenario.Requests) {
				t.Errorf("raw device request sequence differs\ngot  %#v\nwant %#v", requests, scenario.Requests)
			}
			if !reflect.DeepEqual(waits, scenario.Waits) {
				t.Errorf("poll waits got %v, want %v", waits, scenario.Waits)
			}
			if scenario.Error != "" {
				if callErr == nil || callErr.Error() != scenario.Error {
					t.Errorf("error got %v, want %q", callErr, scenario.Error)
				}
				return
			}
			if callErr != nil {
				t.Fatal(callErr)
			}
			storage := auth.CreateTokenStorage(bundle)
			actual := neopiOAuthCredential{Access: storage.AccessToken, Refresh: storage.RefreshToken, AccountID: storage.AccountID, Email: storage.Email, Plan: storage.PlanType}
			if actual != scenario.Credential {
				t.Errorf("credential got %#v, want %#v", actual, scenario.Credential)
			}
			var token struct {
				ExpiresIn float64 `json:"expires_in"`
			}
			if errJSON := json.Unmarshal(scenario.Tokens, &token); errJSON != nil {
				t.Fatal(errJSON)
			}
			expires, errExpiry := time.Parse(time.RFC3339Nano, storage.Expire)
			if errExpiry != nil {
				t.Fatal(errExpiry)
			}
			duration := time.Duration(token.ExpiresIn * float64(time.Second))
			if expires.Before(before.Add(duration).Truncate(time.Millisecond)) || expires.After(after.Add(duration)) {
				t.Errorf("expiry %s differs from zero-skew source interval [%s,%s]", expires, before.Add(duration), after.Add(duration))
			}
		})
	}
}
