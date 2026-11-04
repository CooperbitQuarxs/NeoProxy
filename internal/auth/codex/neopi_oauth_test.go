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

type neopiOAuthRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type neopiOAuthCredential struct {
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	AccountID string `json:"accountId"`
	Email     string `json:"email"`
	Plan      string `json:"orgName"`
}

func TestCodexNeoPiOAuthCaptureParity(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_oauth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scenarios []struct {
			Name       string               `json:"name"`
			Phase      string               `json:"phase"`
			AuthURL    string               `json:"authURL"`
			Response   json.RawMessage      `json:"response"`
			Status     int                  `json:"status"`
			Previous   neopiOAuthCredential `json:"previous"`
			Credential neopiOAuthCredential `json:"credential"`
			Error      string               `json:"error"`
			Requests   []neopiOAuthRequest  `json:"requests"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			captured := make(chan neopiOAuthRequest, 1)
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
				w.Header().Set("Content-Type", "application/json")
				status := scenario.Status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				var response bytes.Buffer
				if errCompact := json.Compact(&response, scenario.Response); errCompact != nil {
					t.Error(errCompact)
				}
				_, _ = w.Write(response.Bytes())
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				clone := r.Clone(r.Context())
				clone.Host = r.URL.Host
				clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
				return http.DefaultTransport.RoundTrip(clone)
			})}}
			var storage *CodexTokenStorage
			var callErr error
			before := time.Now()
			if scenario.Phase == "login" {
				wantURL, _ := url.Parse(scenario.AuthURL)
				form, errForm := url.ParseQuery(scenario.Requests[0].Body)
				if errForm != nil {
					t.Fatal(errForm)
				}
				pkce := &PKCECodes{CodeVerifier: form.Get("code_verifier"), CodeChallenge: wantURL.Query().Get("code_challenge")}
				gotURL, errURL := auth.GenerateAuthURL(wantURL.Query().Get("state"), pkce)
				if errURL != nil {
					t.Fatal(errURL)
				}
				if gotURL != scenario.AuthURL {
					t.Errorf("authorize URL differs\ngot  %s\nwant %s", gotURL, scenario.AuthURL)
				}
				var bundle *CodexAuthBundle
				bundle, callErr = auth.ExchangeCodeForTokens(context.Background(), form.Get("code"), pkce)
				if callErr == nil {
					storage = auth.CreateTokenStorage(bundle)
				}
			} else {
				storage = &CodexTokenStorage{AccessToken: scenario.Previous.Access, RefreshToken: scenario.Previous.Refresh, AccountID: scenario.Previous.AccountID, Email: scenario.Previous.Email, PlanType: scenario.Previous.Plan}
				var refreshed *CodexTokenData
				refreshed, callErr = auth.RefreshTokens(context.Background(), scenario.Previous.Refresh)
				if callErr == nil {
					auth.UpdateTokenStorage(storage, refreshed)
				}
			}
			after := time.Now()
			got := <-captured
			want := scenario.Requests[0]
			if !reflect.DeepEqual(got, want) {
				t.Errorf("request differs\ngot  %#v\nwant %#v", got, want)
			}
			if scenario.Error != "" {
				if callErr == nil || !strings.Contains(callErr.Error(), scenario.Error) {
					t.Errorf("error got %v, want %q", callErr, scenario.Error)
				}
				return
			}
			if callErr != nil {
				t.Fatal(callErr)
			}
			actual := neopiOAuthCredential{Access: storage.AccessToken, Refresh: storage.RefreshToken, AccountID: storage.AccountID, Email: storage.Email, Plan: storage.PlanType}
			if actual != scenario.Credential {
				t.Errorf("credential got %#v, want %#v", actual, scenario.Credential)
			}
			var response struct {
				ExpiresIn float64 `json:"expires_in"`
			}
			if errJSON := json.Unmarshal(scenario.Response, &response); errJSON != nil {
				t.Fatal(errJSON)
			}
			expires, errExpiry := time.Parse(time.RFC3339Nano, storage.Expire)
			if errExpiry != nil {
				t.Fatal(errExpiry)
			}
			duration := time.Duration(response.ExpiresIn * float64(time.Second))
			if expires.Before(before.Add(duration).Truncate(time.Millisecond)) || expires.After(after.Add(duration)) {
				t.Errorf("stored expiry %s does not match zero-skew source interval [%s,%s]", expires, before.Add(duration), after.Add(duration))
			}
		})
	}
}
