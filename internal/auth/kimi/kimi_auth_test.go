package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	Device       json.RawMessage `json:"device"`
	DeviceStatus int             `json:"deviceStatus"`
	Polls        []struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"polls"`
	Auth *struct {
		URL      string `json:"url"`
		UserCode string `json:"userCode"`
	} `json:"auth"`
	Credential *struct {
		Access    string `json:"access"`
		Refresh   string `json:"refresh"`
		Expires   int64  `json:"expires"`
		AccountID string `json:"accountId"`
	} `json:"credential"`
	Error    string            `json:"error"`
	Before   int64             `json:"before"`
	Waits    []int64           `json:"waits"`
	Requests []capturedRequest `json:"requests"`
}

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

// TestKimiAuthMatchesNeoPiSource replays NeoPi's own kimi-code device login and
// refresh captures (testdata/capture_neopi_kimi_auth.ts): every request's
// method, URL, headers and body, the poll waits, errors and credentials. The
// fingerprint's host-derived values are those of the machine running the test,
// as NeoPi's would be; the device id is the captured install's.
func TestKimiAuthMatchesNeoPiSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_kimi_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		KimiHeaders map[string]string `json:"kimiHeaders"`
		Scenarios   []authScenario    `json:"scenarios"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	authDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(authDir, deviceIDFileName), []byte(capture.KimiHeaders["X-Msh-Device-Id"]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	local := CommonHeaders(authDir)
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
				if r.URL.Path == "/api/oauth/device_authorization" {
					status := scenario.DeviceStatus
					if status == 0 {
						status = 200
					}
					respond(status, scenario.Device, `{"device_code":"device-code-fake","user_code":"FAKE-CODE","verification_uri":"https://www.kimi.com/code/authorize_device","verification_uri_complete":"https://www.kimi.com/code/authorize_device?user_code=FAKE-CODE","interval":5,"expires_in":900}`)
					return
				}
				response := scenario.Polls[min(poll, len(scenario.Polls)-1)]
				poll++
				respond(response.Status, response.Body, `{}`)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			var waits []int64
			before := time.Now()
			a := NewAuthWithHTTPClient(&http.Client{Transport: redirect{target: target, asked: asked}}, authDir)
			a.wait = func(_ context.Context, delay time.Duration) error {
				waits = append(waits, delay.Milliseconds())
				return nil
			}
			var credential *Credential
			var device *DeviceCodeResponse
			if scenario.Kind == "login" {
				if device, err = a.StartDeviceFlow(context.Background()); err == nil {
					credential, err = a.PollForToken(context.Background(), device)
				}
			} else {
				credential, err = a.Refresh(context.Background(), "kimi-refresh-stored")
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
			if !reflect.DeepEqual(waits, scenario.Waits) && (len(waits) > 0 || len(scenario.Waits) > 0) {
				t.Errorf("waits %v; source %v", waits, scenario.Waits)
			}
			if want := scenario.Credential; want != nil {
				if credential == nil {
					t.Fatal("no credential")
				}
				if credential.AccessToken != want.Access || credential.RefreshToken != want.Refresh || credential.AccountID != want.AccountID {
					t.Errorf("credential %+v; source %+v", *credential, *want)
				}
				gotOffset := credential.Expires.Sub(before)
				wantOffset := time.Duration(want.Expires-scenario.Before) * time.Millisecond
				if diff := gotOffset - wantOffset; diff < -2*time.Second || diff > 2*time.Second {
					t.Errorf("expiry offset %v; source %v", gotOffset, wantOffset)
				}
			}
			for i := range scenario.Requests {
				delete(scenario.Requests[i].Headers, "host")
				delete(scenario.Requests[i].Headers, "content-length")
				for _, name := range []string{"X-Msh-Device-Name", "X-Msh-Device-Model", "X-Msh-Os-Version"} {
					scenario.Requests[i].Headers[strings.ToLower(name)] = local.Get(name)
				}
			}
			for i := range got {
				delete(got[i].Headers, "content-length")
			}
			if !reflect.DeepEqual(got, scenario.Requests) {
				gotJSON, _ := json.MarshalIndent(got, "", " ")
				wantJSON, _ := json.MarshalIndent(scenario.Requests, "", " ")
				t.Errorf("requests\ngot    %s\nsource %s", gotJSON, wantJSON)
			}
		})
	}
}
