package devin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestLoginMatchesNeoPi replays testdata/neopi_devin_auth_capture.json: NeoPi's
// own devin oauth-code rule with PKCE bytes and the clock pinned. The same
// verifier bytes must yield the same authorize URL, token request, credential
// and error wording.
func TestLoginMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_devin_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Now         int64  `json:"now"`
		RedirectURI string `json:"redirectUri"`
		State       string `json:"state"`
		Callback    struct {
			Port     int    `json:"port"`
			Path     string `json:"path"`
			Hostname string `json:"hostname"`
		} `json:"callback"`
		Scenarios []struct {
			Name     string `json:"name"`
			Code     string `json:"code"`
			Response struct {
				Status int    `json:"status"`
				Body   string `json:"body"`
			} `json:"response"`
			Authorize struct {
				URL string `json:"url"`
			} `json:"authorize"`
			Requests []struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"requests"`
			Credentials *struct {
				Access  string `json:"access"`
				Expires int64  `json:"expires"`
			} `json:"credentials"`
			Error *string `json:"error"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("http://127.0.0.1:%d/callback", CallbackPort); capture.RedirectURI != want {
		t.Fatalf("NeoPi callback %s, Go login uses %s", capture.RedirectURI, want)
	}
	// NeoPi's capture pins crypto.getRandomValues to (i*7+3) & 0xff.
	raw := make([]byte, 96)
	for i := range raw {
		raw[i] = byte(i*7 + 3)
	}
	pkce := pkceFromBytes(raw)
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var gotMethod, gotPath, gotBody string
			var gotHeaders map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotMethod, gotPath, gotBody = r.Method, r.URL.Path, string(body)
				gotHeaders = map[string]string{}
				for name, values := range r.Header {
					if name != "Content-Length" {
						gotHeaders[strings.ToLower(name)] = strings.Join(values, ", ")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(scenario.Response.Status)
				_, _ = io.WriteString(w, scenario.Response.Body)
			}))
			defer server.Close()
			svc := NewDevinAuthService(server.Client())
			svc.SetAPIBaseURL(server.URL)
			svc.now = func() time.Time { return time.UnixMilli(capture.Now) }

			if got := svc.BuildAuthorizationURL(capture.RedirectURI, pkce.CodeChallenge, capture.State); got != scenario.Authorize.URL {
				t.Errorf("authorize URL:\n got %s\nwant %s", got, scenario.Authorize.URL)
			}
			credential, errExchange := svc.ExchangeCodeForToken(context.Background(), scenario.Code, pkce.CodeVerifier)
			request := scenario.Requests[0]
			if wantPath := strings.TrimPrefix(request.URL, DefaultAPIBaseURL); gotMethod != request.Method || gotPath != wantPath {
				t.Errorf("token request = %s %s, want %s %s", gotMethod, gotPath, request.Method, wantPath)
			}
			wantHeaders := map[string]string{}
			for name, value := range request.Headers {
				if name != "content-length" {
					wantHeaders[name] = value
				}
			}
			if !reflect.DeepEqual(gotHeaders, wantHeaders) {
				t.Errorf("token headers:\n got %v\nwant %v", gotHeaders, wantHeaders)
			}
			if gotBody != request.Body {
				t.Errorf("token body:\n got %s\nwant %s", gotBody, request.Body)
			}
			if scenario.Error != nil {
				if errExchange == nil || errExchange.Error() != *scenario.Error {
					t.Fatalf("error = %v, want %q", errExchange, *scenario.Error)
				}
				return
			}
			if errExchange != nil {
				t.Fatal(errExchange)
			}
			if credential.Access != scenario.Credentials.Access || credential.Expires.UnixMilli() != scenario.Credentials.Expires {
				t.Errorf("credential = %s @%d, want %s @%d", credential.Access, credential.Expires.UnixMilli(), scenario.Credentials.Access, scenario.Credentials.Expires)
			}
		})
	}
}

func TestOAuthServerCallback(t *testing.T) {
	server := NewOAuthServer(0)
	port, err := server.Start()
	if err != nil {
		t.Fatalf("OAuthServer.Start() failed: %v", err)
	}
	defer func() {
		_ = server.Stop(context.Background())
	}()

	resp, errGet := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?code=mock-code&state=mock-state", port))
	if errGet != nil {
		t.Fatalf("GET /callback failed: %v", errGet)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	res, errWait := server.WaitForCallback(2 * time.Second)
	if errWait != nil {
		t.Fatalf("WaitForCallback failed: %v", errWait)
	}
	if res.Code != "mock-code" || res.State != "mock-state" {
		t.Errorf("callback = %+v, want mock-code/mock-state", res)
	}
}
