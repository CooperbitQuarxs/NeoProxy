package githubcopilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// copilotAuthCapture is testdata/neopi_github_copilot_auth.json: every
// request NeoPi's own loginGitHubCopilot made against a scripted fetch.
type copilotAuthCapture struct {
	Token     string   `json:"token"`
	ModelIDs  []string `json:"modelIds"`
	Denied    string   `json:"denied"`
	Scenarios []struct {
		Name        string `json:"name"`
		Input       string `json:"input"`
		Credentials struct {
			Access        string `json:"access"`
			EnterpriseURL string `json:"enterpriseUrl"`
			APIEndpoint   string `json:"apiEndpoint"`
		} `json:"credentials"`
		Requests []copilotAuthRequest `json:"requests"`
	} `json:"scenarios"`
}

type copilotAuthRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// key is the request's comparable form: method, path and query, body, and
// the headers it set (Go's transport adds its own framing headers).
func (r copilotAuthRequest) key() string {
	parsed, _ := url.Parse(r.URL)
	headers, _ := json.Marshal(r.Headers)
	return r.Method + " " + parsed.RequestURI() + " " + r.Body + " " + string(headers)
}

// The Go sign-in makes the requests NeoPi's own loginGitHubCopilot makes, in
// order, for public github.com and a GitHub Enterprise domain: device code,
// polls through pending and slow_down, the plan endpoint lookup, and each
// model's policy (a denied chat identity retried once as the CLI), and it
// stores the same credential.
func TestGitHubCopilotSignInMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_github_copilot_auth.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture copilotAuthCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var mu sync.Mutex
			var got []copilotAuthRequest
			polls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				headers := map[string]string{}
				for name, values := range r.Header {
					switch name {
					case "Accept-Encoding", "Content-Length":
						continue
					}
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				mu.Lock()
				got = append(got, copilotAuthRequest{Method: r.Method, URL: r.URL.RequestURI(), Headers: headers, Body: string(body)})
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/login/device/code":
					_, _ = io.WriteString(w, `{"device_code":"dev-code","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","interval":5,"expires_in":900}`)
				case r.URL.Path == "/login/oauth/access_token":
					mu.Lock()
					polls++
					n := polls
					mu.Unlock()
					switch n {
					case 1:
						_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
					case 2:
						_, _ = io.WriteString(w, `{"error":"slow_down","interval":7}`)
					default:
						_, _ = io.WriteString(w, `{"access_token":"`+capture.Token+`","token_type":"bearer","scope":"read:user"}`)
					}
				case r.URL.Path == "/copilot_internal/user":
					_, _ = io.WriteString(w, `{"endpoints":{"api":"https://api.individual.githubcopilot.com/"}}`)
				case strings.HasSuffix(r.URL.Path, "/policy"):
					if strings.Contains(r.URL.Path, "/models/"+capture.Denied+"/") && r.Header.Get("Copilot-Integration-Id") == ChatIntegrationID {
						w.WriteHeader(http.StatusForbidden)
						_, _ = io.WriteString(w, `{"error":{"message":"denied"}}`)
						return
					}
					_, _ = io.WriteString(w, `{"state":"enabled"}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			var waits []time.Duration
			a := NewAuth(server.Client())
			a.githubAPI = server.URL
			a.deviceBase = func(string) string { return server.URL }
			a.wait = func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil }
			frozen := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
			a.now = func() time.Time { return frozen }

			code, err := a.StartDeviceFlow(context.Background(), scenario.Input)
			if err != nil {
				t.Fatal(err)
			}
			credential, err := a.PollForToken(context.Background(), code)
			if err != nil {
				t.Fatal(err)
			}
			if credential.AccessToken != scenario.Credentials.Access || credential.EnterpriseURL != scenario.Credentials.EnterpriseURL || credential.APIEndpoint != scenario.Credentials.APIEndpoint {
				t.Errorf("credential = %+v, want NeoPi's %+v", credential, scenario.Credentials)
			}
			// NeoPi's schedule: the 5s interval at 1.2x twice, then the
			// slow_down interval of 7s at 1.4x.
			if want := []time.Duration{6 * time.Second, 6 * time.Second, 9800 * time.Millisecond}; !reflect.DeepEqual(waits, want) {
				t.Errorf("poll waits = %v, want %v", waits, want)
			}
			// Policies go to the discovered endpoint, here the loopback server.
			credential.APIEndpoint = server.URL
			a.EnableModels(context.Background(), credential, capture.ModelIDs, "", nil)

			want := scenario.Requests
			if len(got) != len(want) {
				t.Fatalf("made %d requests, NeoPi made %d", len(got), len(want))
			}
			// The sign-in requests are ordered; a batch of policies runs
			// concurrently, so policies compare as a set.
			const signIn = 5
			for i := range signIn {
				if got[i].key() != want[i].key() {
					t.Errorf("request %d:\n got %s\nwant %s", i, got[i].key(), want[i].key())
				}
			}
			var gotPolicies, wantPolicies []string
			for i := signIn; i < len(want); i++ {
				gotPolicies = append(gotPolicies, got[i].key())
				wantPolicies = append(wantPolicies, want[i].key())
			}
			slices.Sort(gotPolicies)
			slices.Sort(wantPolicies)
			if !reflect.DeepEqual(gotPolicies, wantPolicies) {
				for i := range wantPolicies {
					if i < len(gotPolicies) && gotPolicies[i] != wantPolicies[i] {
						t.Errorf("policy request differs:\n got %s\nwant %s", gotPolicies[i], wantPolicies[i])
						break
					}
				}
			}
		})
	}
}
