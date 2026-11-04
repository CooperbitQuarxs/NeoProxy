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

type capturedRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type authScenario struct {
	Kind           string          `json:"kind"`
	Name           string          `json:"name"`
	Device         json.RawMessage `json:"device"`
	DeviceStatus   int             `json:"deviceStatus"`
	Discovery      json.RawMessage `json:"discovery"`
	UserinfoStatus int             `json:"userinfoStatus"`
	Polls          []struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"polls"`
	Auth *struct {
		URL      string `json:"url"`
		UserCode string `json:"userCode"`
	} `json:"auth"`
	Credential *struct {
		Refresh   string `json:"refresh"`
		Expires   int64  `json:"expires"`
		Email     string `json:"email"`
		AccountID string `json:"accountId"`
	} `json:"credential"`
	Error    string            `json:"error"`
	Before   int64             `json:"before"`
	Waits    []int64           `json:"waits"`
	Requests []capturedRequest `json:"requests"`
}

// redirect sends every https://auth.x.ai request to the capture server,
// keeping the URL the code asked for.
type redirect struct {
	target *url.URL
	asked  chan string
}

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	r.asked <- req.URL.String()
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// TestXAIAuthMatchesNeoPiSource replays NeoPi's own xai-oauth device login and
// refresh captures (testdata/capture_neopi_xai_auth.ts): every request's
// method, URL, headers and body, the poll waits, errors and credentials.
func TestXAIAuthMatchesNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_xai_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Scenarios []authScenario `json:"scenarios"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			asked := make(chan string, 16)
			var got []capturedRequest
			poll := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				headers := map[string]string{}
				for name, values := range r.Header {
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				got = append(got, capturedRequest{Method: r.Method, URL: <-asked, Headers: headers, Body: string(body)})
				w.Header().Set("Content-Type", "application/json")
				respond := func(status int, payload json.RawMessage, fallback string) {
					if len(payload) == 0 {
						payload = json.RawMessage(fallback)
					}
					w.WriteHeader(status)
					var compact bytes.Buffer
					_ = json.Compact(&compact, payload)
					_, _ = w.Write(compact.Bytes())
				}
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					respond(200, scenario.Discovery, `{"issuer":"https://auth.x.ai","token_endpoint":"https://auth.x.ai/oauth2/token"}`)
				case "/oauth2/device/code":
					status := scenario.DeviceStatus
					if status == 0 {
						status = 200
					}
					respond(status, scenario.Device, `{"device_code":"device-code-fake","user_code":"FAKE-CODE","verification_uri":"https://accounts.x.ai/device","verification_uri_complete":"https://accounts.x.ai/device?code=FAKE-CODE","interval":5,"expires_in":900}`)
				case "/oauth2/userinfo":
					status := scenario.UserinfoStatus
					if status == 0 {
						status = 200
					}
					respond(status, nil, `{"sub":"xai-userinfo-subject","email":"Grok@Example.Invalid","name":"Fake"}`)
				default:
					response := scenario.Polls[min(poll, len(scenario.Polls)-1)]
					poll++
					respond(response.Status, response.Body, `{}`)
				}
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			var waits []int64
			before := time.Now()
			a := &XAIAuth{
				httpClient:      &http.Client{Transport: redirect{target: target, asked: asked}},
				minPollInterval: minimumPollInterval,
				now:             time.Now,
				sleep: func(_ context.Context, delay time.Duration) error {
					waits = append(waits, delay.Milliseconds())
					return nil
				},
			}
			var data *TokenData
			var device *DeviceCodeResponse
			if scenario.Kind == "login" {
				device, err = a.StartDeviceFlow(context.Background())
				if err == nil {
					var bundle *AuthBundle
					if bundle, err = a.WaitForAuthorization(context.Background(), device); err == nil {
						data = &bundle.TokenData
					}
				}
			} else {
				data, err = a.RefreshTokens(context.Background(), "xai-refresh-stored", "")
			}
			gotError := ""
			if err != nil {
				gotError = err.Error()
			}
			if gotError != scenario.Error {
				t.Errorf("error %q; source %q", gotError, scenario.Error)
			}
			if scenario.Auth != nil && (device == nil || device.VerificationURIComplete != scenario.Auth.URL || device.UserCode != scenario.Auth.UserCode) {
				t.Errorf("auth prompt %+v; source %+v", device, scenario.Auth)
			}
			if len(waits) != len(scenario.Waits) || (len(waits) > 0 && !reflect.DeepEqual(waits, scenario.Waits)) {
				t.Errorf("waits %v; source %v", waits, scenario.Waits)
			}
			if want := scenario.Credential; want != nil {
				if data == nil {
					t.Fatal("no credential")
				}
				if data.RefreshToken != want.Refresh || data.Email != want.Email || data.Subject != want.AccountID {
					t.Errorf("credential refresh=%q email=%q account=%q; source %+v", data.RefreshToken, data.Email, data.Subject, *want)
				}
				// NeoPi stores expiry less the five-minute skew; NeoProxy stores the
				// server's expiry and refreshes RefreshLead() early.
				expire, errParse := time.Parse(time.RFC3339, data.Expire)
				if errParse != nil {
					t.Fatal(errParse)
				}
				gotOffset := expire.Add(-RefreshLead()).Sub(before)
				wantOffset := time.Duration(want.Expires-scenario.Before) * time.Millisecond
				if diff := gotOffset - wantOffset; diff < -2*time.Second || diff > 2*time.Second {
					t.Errorf("expiry offset %v; source %v", gotOffset, wantOffset)
				}
			}
			for i := range scenario.Requests {
				delete(scenario.Requests[i].Headers, "host")
			}
			for i := range got {
				delete(got[i].Headers, "content-length")
			}
			for i := range scenario.Requests {
				delete(scenario.Requests[i].Headers, "content-length")
			}
			if !reflect.DeepEqual(got, scenario.Requests) {
				gotJSON, _ := json.MarshalIndent(got, "", " ")
				wantJSON, _ := json.MarshalIndent(scenario.Requests, "", " ")
				t.Errorf("requests\ngot    %s\nsource %s", gotJSON, wantJSON)
			}
		})
	}
}
