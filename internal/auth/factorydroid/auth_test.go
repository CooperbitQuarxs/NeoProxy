package factorydroid

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// factoryAuthCapture is testdata/neopi_factory_auth_capture.json: NeoPi's OWN
// device login and refresh for factory-droid, each scenario with the
// responses its synthetic WorkOS and Factory hosts gave, the requests NeoPi
// sent, the waits it asked for and the credential or error it produced.
type factoryAuthCapture struct {
	Stored    map[string]any `json:"stored"`
	Scenarios []struct {
		Kind         string          `json:"kind"`
		Name         string          `json:"name"`
		Device       json.RawMessage `json:"device"`
		DeviceStatus int             `json:"deviceStatus"`
		Polls        []capturedReply `json:"polls"`
		Refreshes    []capturedReply `json:"refreshes"`
		Whoami       []struct {
			Token string `json:"token"`
			capturedReply
		} `json:"whoami"`
		Org        *capturedReply `json:"org"`
		Auth       map[string]any `json:"auth"`
		Credential map[string]any `json:"credential"`
		Error      string         `json:"error"`
		Waits      []float64      `json:"waits"`
		Requests   []struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"requests"`
	} `json:"scenarios"`
}

type capturedReply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// The device response the capture server answers with when a scenario names
// none.
const capturedDevice = `{"device_code":"device-code-fake","user_code":"FAKE-CODE","verification_uri":"https://auth.factory.ai/device","verification_uri_complete":"https://auth.factory.ai/device?user_code=FAKE-CODE","interval":5,"expires_in":300}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNeoPiFactoryAuthParity(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_factory_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture factoryAuthCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			type sent struct {
				method, url, body string
				headers           http.Header
			}
			var requests []sent
			polls, refreshes := 0, 0
			reply := func(r *http.Request, status int, body []byte) *http.Response {
				if status == 0 {
					status = http.StatusOK
				}
				if len(body) == 0 {
					body = []byte("{}")
				}
				// Response.json serializes compactly.
				var compact bytes.Buffer
				if json.Compact(&compact, body) == nil {
					body = compact.Bytes()
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}
			}
			pick := func(replies []capturedReply, index *int) capturedReply {
				entry := replies[min(*index, len(replies)-1)]
				*index++
				return entry
			}
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var body []byte
				if r.Body != nil {
					body, _ = io.ReadAll(r.Body)
				}
				requests = append(requests, sent{method: r.Method, url: r.URL.String(), body: string(body), headers: r.Header.Clone()})
				switch r.URL.Path {
				case "/user_management/authorize/device":
					device := []byte(capturedDevice)
					if len(scenario.Device) > 0 {
						device = scenario.Device
					}
					return reply(r, scenario.DeviceStatus, device), nil
				case "/user_management/authenticate":
					entry := capturedReply{}
					if strings.Contains(string(body), "grant_type=refresh_token") {
						entry = pick(scenario.Refreshes, &refreshes)
					} else {
						entry = pick(scenario.Polls, &polls)
					}
					return reply(r, entry.Status, entry.Body), nil
				case "/api/cli/whoami":
					for _, entry := range scenario.Whoami {
						if strings.Contains(r.Header.Get("Authorization"), entry.Token) {
							return reply(r, entry.Status, entry.Body), nil
						}
					}
					return reply(r, http.StatusOK, nil), nil
				case "/api/cli/org":
					if scenario.Org == nil {
						return reply(r, http.StatusNotFound, nil), nil
					}
					return reply(r, scenario.Org.Status, scenario.Org.Body), nil
				}
				return reply(r, 599, []byte("unexpected")), nil
			})}
			auth := NewAuth(client)
			var waits []float64
			auth.wait = func(_ context.Context, d time.Duration) error {
				waits = append(waits, float64(d.Milliseconds()))
				return nil
			}
			var credential *Credential
			var code *DeviceCode
			if scenario.Kind == "login" {
				code, err = auth.StartDeviceFlow(context.Background())
				if err == nil {
					credential, err = auth.PollForToken(context.Background(), code)
				}
			} else {
				credential, err = auth.Refresh(context.Background(), storedCredential(capture.Stored))
			}

			if scenario.Error != "" {
				if err == nil || err.Error() != scenario.Error {
					t.Errorf("error = %v, want %q", err, scenario.Error)
				}
			} else if err != nil {
				t.Fatalf("error = %v, want credential", err)
			} else if got, want := credentialJSON(credential), comparableCredential(scenario.Credential); got != want {
				t.Errorf("credential:\n got %s\nwant %s", got, want)
			}
			if scenario.Auth != nil && (code == nil || code.URL() != scenario.Auth["url"] || code.UserCode != scenario.Auth["userCode"]) {
				t.Errorf("device code = %+v, want %v", code, scenario.Auth)
			}
			if len(waits) != len(scenario.Waits) {
				t.Errorf("waits = %v, want %v", waits, scenario.Waits)
			} else {
				for i := range waits {
					if waits[i] != scenario.Waits[i] {
						t.Errorf("waits = %v, want %v", waits, scenario.Waits)
						break
					}
				}
			}
			if len(requests) != len(scenario.Requests) {
				t.Fatalf("%d requests, NeoPi sent %d", len(requests), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				got := requests[i]
				if got.method != want.Method || got.url != want.URL || got.body != want.Body {
					t.Errorf("request %d = %s %s %q, want %s %s %q", i, got.method, got.url, got.body, want.Method, want.URL, want.Body)
				}
				for name, value := range want.Headers {
					if name == "host" || name == "content-length" {
						continue
					}
					if got.headers.Get(name) != value {
						t.Errorf("request %d header %s = %q, want %q", i, name, got.headers.Get(name), value)
					}
				}
				for name := range got.headers {
					if _, ok := want.Headers[strings.ToLower(name)]; !ok {
						t.Errorf("request %d extra header %s", i, name)
					}
				}
			}
		})
	}
}

// storedCredential is the capture's stored OAuth credential.
func storedCredential(stored map[string]any) *Credential {
	text := func(key string) string { value, _ := stored[key].(string); return value }
	return &Credential{AccessToken: text("access"), RefreshToken: text("refresh"), Email: text("email"), AccountID: text("accountId"),
		OrgID: text("orgId"), ActiveOrganizationID: text("activeOrganizationId"), Region: text("region"), InferenceRegion: text("inferenceRegion")}
}

// credentialJSON renders a credential in NeoPi's field names, omitting the
// unset ones as NeoPi's object does.
func credentialJSON(c *Credential) string {
	fields := map[string]any{"access": c.AccessToken, "refresh": c.RefreshToken, "expires": float64(c.Expires.UnixMilli())}
	for key, value := range map[string]string{"email": c.Email, "accountId": c.AccountID, "orgId": c.OrgID, "activeOrganizationId": c.ActiveOrganizationID, "region": c.Region, "inferenceRegion": c.InferenceRegion} {
		if value != "" {
			fields[key] = value
		}
	}
	encoded, _ := json.Marshal(fields)
	return string(encoded)
}

func comparableCredential(fields map[string]any) string {
	encoded, _ := json.Marshal(fields)
	return string(encoded)
}
