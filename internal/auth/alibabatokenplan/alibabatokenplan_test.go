package alibabatokenplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/neopikeys"
)

type capturedRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// redirect sends every request to a loopback server and keeps the original URL.
type redirect struct {
	target *url.URL
	seen   *[]capturedRequest
}

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	headers := map[string]string{}
	for name := range req.Header {
		headers[strings.ToLower(name)] = req.Header.Get(name)
	}
	if req.ContentLength > 0 {
		headers["content-length"] = strconv.FormatInt(req.ContentLength, 10)
	}
	*r.seen = append(*r.seen, capturedRequest{Method: req.Method, URL: req.URL.String(), Headers: headers, Body: string(body)})
	forward := req.Clone(req.Context())
	forward.URL.Scheme, forward.URL.Host = r.target.Scheme, r.target.Host
	forward.Body = io.NopCloser(bytes.NewReader(body))
	forward.RequestURI = ""
	return http.DefaultTransport.RoundTrip(forward)
}

func compareRequests(t *testing.T, got, want []capturedRequest) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("requests = %d, want %d", len(got), len(want))
	}
	traceID := regexp.MustCompile(`feTraceId%22%3A%22[0-9a-f-]+%22`)
	for i := range want {
		if got[i].Method != want[i].Method || got[i].URL != want[i].URL {
			t.Errorf("request %d = %s %s, want %s %s", i, got[i].Method, got[i].URL, want[i].Method, want[i].URL)
		}
		// feTraceId is crypto.randomUUID(); the rest of the body is exact.
		gotBody, wantBody := traceID.ReplaceAllString(got[i].Body, "feTraceId"), traceID.ReplaceAllString(want[i].Body, "feTraceId")
		if gotBody != wantBody {
			t.Errorf("request %d body = %q, want %q", i, gotBody, wantBody)
		}
		for name, value := range want[i].Headers {
			if name == "host" || name == "content-length" && traceID.MatchString(want[i].Body) {
				continue
			}
			if got[i].Headers[name] != value {
				t.Errorf("request %d header %s = %q, want %q", i, name, got[i].Headers[name], value)
			}
		}
		for name := range got[i].Headers {
			if _, ok := want[i].Headers[name]; !ok {
				t.Errorf("request %d has extra header %s", i, name)
			}
		}
	}
}

type reply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

func (r *reply) write(w http.ResponseWriter) {
	status := http.StatusOK
	if r != nil && r.Status != 0 {
		status = r.Status
	}
	if r == nil || len(r.Body) == 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("{}"))
		return
	}
	var text string
	if r.Body[0] == '"' && json.Unmarshal(r.Body, &text) == nil {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(text))
		return
	}
	var compact bytes.Buffer
	_ = json.Compact(&compact, r.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(compact.Bytes())
}

// usageCapture is testdata/neopi_alibaba_usage_capture.json: NeoPi's OWN
// alibabaTokenPlanUsageProvider over synthetic console responses.
type usageCapture struct {
	Scenarios []struct {
		Name       string `json:"name"`
		Credential string `json:"credential"`
		Session    *reply `json:"session"`
		Usage      *reply `json:"usage"`
		Supported  bool   `json:"supported"`
		Report     *struct {
			Source    string  `json:"source"`
			AccountID *string `json:"accountId"`
			Limits    []struct {
				WindowID     string  `json:"windowId"`
				DurationMs   *int64  `json:"durationMs"`
				ResetsAt     *int64  `json:"resetsAt"`
				UsedFraction float64 `json:"usedFraction"`
			} `json:"limits"`
		} `json:"report"`
		Requests []capturedRequest `json:"requests"`
	} `json:"scenarios"`
}

func TestUsageMatchesNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_alibaba_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture usageCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			credential, ok := Parse(scenario.Credential)
			if supported := ok && credential.Cookie != ""; supported != scenario.Supported {
				t.Fatalf("supported = %v, want %v", supported, scenario.Supported)
			}
			if !scenario.Supported {
				return
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					scenario.Usage.write(w)
					return
				}
				scenario.Session.write(w)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			var seen []capturedRequest
			usage, err := FetchUsage(context.Background(), &http.Client{Transport: redirect{target: target, seen: &seen}}, credential.Cookie, IsChina(credential.BaseURL))
			compareRequests(t, seen, scenario.Requests)
			if scenario.Report == nil {
				if err == nil || usage != nil {
					t.Fatalf("FetchUsage() = %+v, %v, want a failure", usage, err)
				}
				return
			}
			if err != nil || usage == nil {
				t.Fatalf("FetchUsage() = %+v, %v", usage, err)
			}
			if usage.Source != scenario.Report.Source {
				t.Errorf("source = %q, want %q", usage.Source, scenario.Report.Source)
			}
			if wantAccount := ""; scenario.Report.AccountID != nil {
				wantAccount = *scenario.Report.AccountID
				if usage.AccountID != wantAccount {
					t.Errorf("account = %q, want %q", usage.AccountID, wantAccount)
				}
			} else if usage.AccountID != "" {
				t.Errorf("account = %q, want none", usage.AccountID)
			}
			if len(usage.Limits) != len(scenario.Report.Limits) {
				t.Fatalf("limits = %+v, want %d", usage.Limits, len(scenario.Report.Limits))
			}
			for i, want := range scenario.Report.Limits {
				got := usage.Limits[i]
				if got.WindowID != want.WindowID || got.UsedFraction != want.UsedFraction {
					t.Errorf("limit %d = %+v, want %+v", i, got, want)
				}
				if (want.DurationMs == nil) != (got.DurationMs == 0) || want.DurationMs != nil && *want.DurationMs != got.DurationMs {
					t.Errorf("limit %d duration = %d, want %v", i, got.DurationMs, want.DurationMs)
				}
				if (want.ResetsAt == nil) != got.ResetsAt.IsZero() || want.ResetsAt != nil && *want.ResetsAt != got.ResetsAt.UnixMilli() {
					t.Errorf("limit %d resets = %v, want %v", i, got.ResetsAt, want.ResetsAt)
				}
			}
		})
	}
}

// keyEntryCapture is the Token Plan part of testdata/neopikeys/neopi_keys_capture.json.
type keyEntryCapture struct {
	Scenarios []struct {
		Provider string          `json:"provider"`
		Label    string          `json:"label"`
		Answers  []string        `json:"answers"`
		Status   int             `json:"status"`
		RawBody  json.RawMessage `json:"body"`
		Result   struct {
			Value string `json:"value"`
			Error string `json:"error"`
		} `json:"result"`
		Requests []capturedRequest `json:"requests"`
	} `json:"scenarios"`
}

// The region choice, key check and cookie prompt of NeoPi's sign-in: the same
// probe request, the same failure messages, and the same stored bundle.
func TestKeyEntryMatchesNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("../neopikeys/testdata/neopi_keys_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture keyEntryCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, scenario := range capture.Scenarios {
		if scenario.Provider != Provider || len(scenario.Requests) == 0 {
			continue
		}
		checked++
		t.Run(scenario.Label, func(t *testing.T) {
			// answers: region choice, [custom URL,] key, cookie.
			baseURL, next := InternationalBaseURL, 1
			switch strings.TrimSpace(scenario.Answers[0]) {
			case "2":
				baseURL = ChinaBaseURL
			case "3":
				baseURL, next = NormalizeBaseURL(scenario.Answers[1]), 2
			}
			key, cookie := strings.TrimSpace(scenario.Answers[next]), scenario.Answers[next+1]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				(&reply{Status: scenario.Status, Body: scenario.RawBody}).write(w)
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			var seen []capturedRequest
			validateErr := ValidateKey(context.Background(), &http.Client{Transport: redirect{target: target, seen: &seen}}, baseURL, key)
			compareRequests(t, seen, scenario.Requests)
			if scenario.Result.Value == "" && validateErr != nil {
				var httpErr *neopikeys.HTTPError
				if !errors.As(validateErr, &httpErr) || httpErr.Error() != scenario.Result.Error {
					t.Fatalf("ValidateKey() = %v, want %q", validateErr, scenario.Result.Error)
				}
				return
			}
			if validateErr != nil {
				t.Fatalf("ValidateKey() = %v", validateErr)
			}
			normalized, cookieErr := NormalizeCookie(cookie, baseURL)
			if scenario.Result.Value == "" {
				if cookieErr == nil || cookieErr.Error() != scenario.Result.Error {
					t.Fatalf("NormalizeCookie() = %q, %v, want %q", normalized, cookieErr, scenario.Result.Error)
				}
				return
			}
			if cookieErr != nil {
				t.Fatalf("NormalizeCookie() = %v", cookieErr)
			}
			stored, ok := Parse(scenario.Result.Value)
			wantBase := ""
			if baseURL != InternationalBaseURL {
				wantBase = baseURL
			}
			if !ok || stored.Token != key || stored.Cookie != normalized || stored.BaseURL != wantBase {
				t.Errorf("stored bundle = %+v, want token %q cookie %q base %q", stored, key, normalized, wantBase)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no Token Plan scenarios replayed")
	}
}

func TestParseMatchesNeoPi(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Credential
		ok   bool
	}{
		{"sk-sp-abc", Credential{Token: "sk-sp-abc"}, true},
		{"  sk-sp-abc== ", Credential{Token: "sk-sp-abc=="}, true},
		{"not-a-key", Credential{Token: "not-a-key"}, false},
		{"", Credential{}, false},
		{`{"token":" sk-sp-abc ","cookie":" a=b ","baseUrl":" https://x/v1 "}`, Credential{Token: "sk-sp-abc", Cookie: "a=b", BaseURL: "https://x/v1"}, true},
		{`{"token":"sk-sp-abc"}`, Credential{Token: "sk-sp-abc"}, true},
		{`{"token":"sk-sp-abc","cookie":null}`, Credential{}, false},
		{`{"token":"sk-sp-abc","baseUrl":5}`, Credential{}, false},
		{`{"token":"nope"}`, Credential{}, false},
		{`{"cookie":"a=b"}`, Credential{}, false},
		{`{broken`, Credential{}, false},
	} {
		got, ok := Parse(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("Parse(%q) = %+v, %v, want %+v, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
