package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type capturedReply struct {
	network bool
	status  int
	body    string
}

func (r *capturedReply) UnmarshalJSON(data []byte) error {
	if string(data) == `"network-error"` {
		r.network = true
		return nil
	}
	var reply struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	err := json.Unmarshal(data, &reply)
	r.status, r.body = reply.Status, reply.Body
	return err
}

type capturedRequest struct {
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	Body         string            `json:"body"`
	NetworkError bool              `json:"networkError"`
}

type authScenario struct {
	Name     string                     `json:"name"`
	Replies  map[string][]capturedReply `json:"replies"`
	Requests []capturedRequest          `json:"requests"`
	Delays   []int64                    `json:"delays"`
	LoginURL string                     `json:"loginUrl"`
	Result   *struct {
		Access  string `json:"access"`
		Refresh string `json:"refresh"`
		Expires int64  `json:"expires"`
		Email   string `json:"email"`
	} `json:"result"`
	Error       *string `json:"error"`
	Credentials *struct {
		Access  string `json:"access"`
		Refresh string `json:"refresh"`
		Email   string `json:"email"`
	} `json:"credentials"`
}

// TestAuthMatchesNeoPi replays testdata/neopi_cursor_auth_capture.json:
// NeoPi's own loginCursorHook and refreshCursorHook against scripted replies,
// with PKCE bytes, uuid, clock and poll sleeps pinned. The same inputs must
// give the same login URL, requests, poll delays, credential and error text.
func TestAuthMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_cursor_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Now       int64          `json:"now"`
		UUID      string         `json:"uuid"`
		Scenarios []authScenario `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 96)
	for i := range raw {
		raw[i] = byte(i*7 + 3)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			var mu sync.Mutex
			var got []capturedRequest
			replies := map[string][]capturedReply{}
			for path, list := range scenario.Replies {
				replies[path] = append([]capturedReply(nil), list...)
			}
			origin := map[string]string{"/auth/poll": "https://api2.cursor.sh", "/oauth/token": "https://api2.cursor.sh", "/aiserver.v1.DashboardService/GetMe": "https://api2.cursor.sh"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				mu.Lock()
				if len(replies[r.URL.Path]) == 0 {
					mu.Unlock()
					t.Errorf("unscripted request %s %s", r.Method, r.URL.RequestURI())
					http.Error(w, "unscripted", 599)
					return
				}
				reply := replies[r.URL.Path][0]
				replies[r.URL.Path] = replies[r.URL.Path][1:]
				request := capturedRequest{Method: r.Method, URL: origin[r.URL.Path] + r.URL.RequestURI(), Body: string(body), Headers: map[string]string{}}
				for name, values := range r.Header {
					request.Headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				got = append(got, request)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(reply.status)
				_, _ = io.WriteString(w, reply.body)
			}))
			defer server.Close()
			// A scripted network error fails inside the client, as NeoPi's
			// capture throws inside fetch, so nothing reaches the server.
			client := server.Client()
			base := client.Transport
			client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				mu.Lock()
				if list := replies[r.URL.Path]; len(list) > 0 && list[0].network {
					replies[r.URL.Path] = list[1:]
					got = append(got, capturedRequest{Method: r.Method, URL: origin[r.URL.Path] + r.URL.RequestURI(), NetworkError: true})
					mu.Unlock()
					return nil, errors.New("fetch failed")
				}
				mu.Unlock()
				return base.RoundTrip(r)
			})

			var delays []int64
			a := newAuth(client)
			a.now = func() time.Time { return time.UnixMilli(capture.Now) }
			a.sleep = func(_ context.Context, d time.Duration) error {
				delays = append(delays, d.Milliseconds())
				return nil
			}
			a.pollURL, a.refreshURL, a.getMeURL = server.URL+"/auth/poll", server.URL+"/oauth/token", server.URL+"/aiserver.v1.DashboardService/GetMe"

			var credential *Credential
			var errFlow error
			if scenario.Credentials == nil {
				session := newLoginSession(raw, capture.UUID)
				if session.URL != scenario.LoginURL {
					t.Errorf("login URL:\n got %s\nwant %s", session.URL, scenario.LoginURL)
				}
				credential, errFlow = a.Login(context.Background(), session)
			} else {
				stored := Credential{Access: scenario.Credentials.Access, Refresh: scenario.Credentials.Refresh, Email: scenario.Credentials.Email}
				credential, errFlow = a.Refresh(context.Background(), stored)
			}

			if !reflect.DeepEqual(delays, scenario.Delays) && (len(delays) > 0 || len(scenario.Delays) > 0) {
				t.Errorf("poll delays = %v, want %v", delays, scenario.Delays)
			}
			if len(got) != len(scenario.Requests) {
				t.Fatalf("%d requests, want %d", len(got), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				have := got[i]
				if have.Method != want.Method || have.URL != want.URL || have.NetworkError != want.NetworkError {
					t.Errorf("request %d = %s %s, want %s %s", i, have.Method, have.URL, want.Method, want.URL)
					continue
				}
				if want.NetworkError {
					continue
				}
				delete(have.Headers, "content-length")
				delete(want.Headers, "content-length")
				if !reflect.DeepEqual(have.Headers, want.Headers) {
					t.Errorf("request %d headers:\n got %v\nwant %v", i, have.Headers, want.Headers)
				}
				if have.Body != want.Body {
					t.Errorf("request %d body:\n got %s\nwant %s", i, have.Body, want.Body)
				}
			}
			if scenario.Error != nil {
				if errFlow == nil || errFlow.Error() != *scenario.Error {
					t.Fatalf("error = %v, want %q", errFlow, *scenario.Error)
				}
				return
			}
			if errFlow != nil {
				t.Fatal(errFlow)
			}
			want := scenario.Result
			if credential.Access != want.Access || credential.Refresh != want.Refresh || credential.Expires.UnixMilli() != want.Expires || credential.Email != want.Email {
				t.Errorf("credential = %s/%s @%d %q, want %s/%s @%d %q", credential.Access, credential.Refresh, credential.Expires.UnixMilli(), credential.Email, want.Access, want.Refresh, want.Expires, want.Email)
			}
		})
	}
}

func TestUserIDTakesTheSecondSubjectPart(t *testing.T) {
	for sub, want := range map[string]string{"auth0|user_abc": "user_abc", "github|1245379": "1245379", "a|b|c": "b", "plain": "plain"} {
		token := "e30." + strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(b64(`{"sub":"`+sub+`"}`)), "=") + ".sig"
		if got := UserID(token); got != want {
			t.Errorf("UserID(%s) = %q, want %q", sub, got, want)
		}
	}
}

func b64(s string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var out strings.Builder
	for i := 0; i < len(s); i += 3 {
		var chunk [3]byte
		n := copy(chunk[:], s[i:])
		v := uint(chunk[0])<<16 | uint(chunk[1])<<8 | uint(chunk[2])
		for j := 0; j < 4; j++ {
			if j > n {
				out.WriteByte('=')
				continue
			}
			out.WriteByte(alphabet[v>>(18-6*j)&63])
		}
	}
	return out.String()
}
