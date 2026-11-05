package cursor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

type cursorUsageCapture struct {
	Token     string `json:"token"`
	Scenarios []struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseUrl"`
		Email   string `json:"email"`
		Replies map[string]struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		} `json:"replies"`
		Requests []struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"requests"`
		Report json.RawMessage `json:"report"`
	} `json:"scenarios"`
}

// scriptedTransport answers each URL from the capture and records requests.
type scriptedTransport struct {
	mu      sync.Mutex
	replies map[string]struct {
		status int
		body   string
	}
	requests []*http.Request
	bodies   map[*http.Request]string
}

func (s *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	key := req.URL.Scheme + "://" + req.URL.Host + req.URL.Path
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.bodies[req] = body
	reply, ok := s.replies[key]
	s.mu.Unlock()
	if !ok {
		reply = struct {
			status int
			body   string
		}{500, "unscripted"}
	}
	return &http.Response{StatusCode: reply.status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(reply.body)), Request: req}, nil
}

// TestFetchUsageMatchesNeoPi replays testdata/neopi_cursor_usage_capture.json:
// the same requests, and the same report, for legacy plan rails, the X
// Subscription, overall buckets, a custom host and a total failure.
func TestFetchUsageMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_cursor_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture cursorUsageCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			transport := &scriptedTransport{replies: map[string]struct {
				status int
				body   string
			}{}, bodies: map[*http.Request]string{}}
			for url, reply := range scenario.Replies {
				transport.replies[url] = struct {
					status int
					body   string
				}{reply.Status, reply.Body}
			}
			usage := FetchUsage(context.Background(), &http.Client{Transport: transport}, scenario.BaseURL, capture.Token, "acct-1", scenario.Email)

			sort.Slice(transport.requests, func(i, j int) bool { return transport.requests[i].URL.String() < transport.requests[j].URL.String() })
			if len(transport.requests) != len(scenario.Requests) {
				t.Fatalf("requests = %d, NeoPi %d", len(transport.requests), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				got := transport.requests[i]
				if got.Method != want.Method || got.URL.String() != want.URL {
					t.Errorf("request %d = %s %s, NeoPi %s %s", i, got.Method, got.URL, want.Method, want.URL)
				}
				if transport.bodies[got] != want.Body {
					t.Errorf("request %d body = %q, NeoPi %q", i, transport.bodies[got], want.Body)
				}
				for name, value := range want.Headers {
					if name == "content-length" {
						continue
					}
					if got.Header.Get(name) != value {
						t.Errorf("request %d header %s = %q, NeoPi %q", i, name, got.Header.Get(name), value)
					}
				}
				if len(got.Header) != len(want.Headers)-boolInt(want.Headers["content-length"] != "") {
					t.Errorf("request %d headers = %v, NeoPi %v", i, got.Header, want.Headers)
				}
			}

			var gotReport, wantReport any
			if usage != nil {
				encoded, _ := json.Marshal(usage)
				_ = json.Unmarshal(encoded, &gotReport)
			}
			_ = json.Unmarshal(scenario.Report, &wantReport)
			if !reflect.DeepEqual(gotReport, wantReport) {
				got, _ := json.Marshal(gotReport)
				t.Errorf("report differs\n go:    %s\n neopi: %s", got, scenario.Report)
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
