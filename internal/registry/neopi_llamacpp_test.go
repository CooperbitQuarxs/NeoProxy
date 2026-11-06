package registry

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"
)

// The port discovers each captured llama.cpp server as NeoPi's runtime does
// (testdata/neopi_llamacpp_discovery.json.gz, captured by
// capture_neopi_llamacpp_discovery.ts): the requests it sends, whether
// discovery fails, and every row it builds, in order, with its API and
// resolved compat.
func TestNeoPiLlamaCppDiscoveryMatchesNeoPi(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_llamacpp_discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Results []struct {
			Scenario         string                     `json:"scenario"`
			BaseURL          string                     `json:"baseUrl"`
			FallbackBaseURLs []string                   `json:"fallbackBaseUrls"`
			APIKey           string                     `json:"apiKey"`
			Replies          map[string]json.RawMessage `json:"replies"`
			Requests         []struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"requests"`
			Error  string `json:"error"`
			Models []struct {
				ID            string          `json:"id"`
				Name          string          `json:"name"`
				API           string          `json:"api"`
				BaseURL       string          `json:"baseUrl"`
				Reasoning     bool            `json:"reasoning"`
				Input         []string        `json:"input"`
				ContextWindow float64         `json:"contextWindow"`
				MaxTokens     float64         `json:"maxTokens"`
				Thinking      *NeoPiThinking  `json:"thinking"`
				Compat        json.RawMessage `json:"compat"`
			} `json:"models"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Results) == 0 {
		t.Fatal("capture holds no scenarios")
	}
	for _, result := range capture.Results {
		t.Run(result.Scenario, func(t *testing.T) {
			var mu sync.Mutex
			var asked []string
			bases := append([]string{result.BaseURL}, result.FallbackBaseURLs...)
			got, err := NeoPiLlamaCppListing(t.Context(), bases, func(_ context.Context, endpoint string) (int, []byte, error) {
				parsed, errParse := url.Parse(endpoint)
				if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
					// fetch rejects a URL that does not parse before sending.
					return 0, nil, errors.New("invalid URL")
				}
				mu.Lock()
				asked = append(asked, endpoint)
				mu.Unlock()
				reply, ok := result.Replies[parsed.Scheme+"://"+parsed.Host+parsed.EscapedPath()]
				if !ok {
					return 404, []byte("not found"), nil
				}
				if string(reply) == `"unreachable"` {
					return 0, nil, errors.New("connection refused")
				}
				var answer neoPiCaptureReply
				if errJSON := json.Unmarshal(reply, &answer); errJSON != nil {
					return 0, nil, errJSON
				}
				return answer.Status, []byte(answer.Body), nil
			})
			var want []string
			for _, request := range result.Requests {
				want = append(want, request.URL)
			}
			slices.Sort(asked)
			slices.Sort(want)
			if !reflect.DeepEqual(asked, want) {
				t.Errorf("asked %q\nNeoPi asked %q", asked, want)
			}
			if result.Error != "" {
				if !errors.Is(err, ErrNeoPiListing) {
					t.Fatalf("NeoPi's discovery failed (%s); port returned %d rows, err %v", result.Error, len(got), err)
				}
				return
			}
			if empty := (*NeoPiEmptyListing)(nil); errors.As(err, &empty) {
				got, err = empty.Models, nil
			}
			if err != nil {
				t.Fatalf("port rejected a roster NeoPi accepted: %v", err)
			}
			if len(got) != len(result.Models) {
				t.Fatalf("rows: port %d, NeoPi %d", len(got), len(result.Models))
			}
			for i, want := range result.Models {
				row := got[i].NeoPi
				if row.ID != want.ID || row.Name != want.Name || row.API != want.API || row.BaseURL != want.BaseURL {
					t.Errorf("row %d: %s %q %s %s, NeoPi %s %q %s %s", i, row.ID, row.Name, row.API, row.BaseURL, want.ID, want.Name, want.API, want.BaseURL)
				}
				if row.Reasoning != want.Reasoning || !reflect.DeepEqual(row.Input, want.Input) {
					t.Errorf("%s: reasoning %v input %v, NeoPi %v %v", want.ID, row.Reasoning, row.Input, want.Reasoning, want.Input)
				}
				if row.ContextWindow != int(want.ContextWindow) || row.MaxTokens != int(want.MaxTokens) {
					t.Errorf("%s: limits %d/%d, NeoPi %v/%v", want.ID, row.ContextWindow, row.MaxTokens, want.ContextWindow, want.MaxTokens)
				}
				if !reflect.DeepEqual(row.Thinking, want.Thinking) {
					t.Errorf("%s: thinking %+v, NeoPi %+v", want.ID, row.Thinking, want.Thinking)
				}
				switch want.API {
				case "openai-completions":
					var wantCompat NeoPiCompletionsCompat
					if errJSON := json.Unmarshal(want.Compat, &wantCompat); errJSON != nil {
						t.Fatal(errJSON)
					}
					if !reflect.DeepEqual(row.CompletionsCompat, &wantCompat) {
						t.Errorf("%s: completions compat differs from NeoPi's\n port %+v\nNeoPi %+v", want.ID, row.CompletionsCompat, wantCompat)
					}
				case "openai-responses":
					var wantCompat NeoPiCompat
					if errJSON := json.Unmarshal(want.Compat, &wantCompat); errJSON != nil {
						t.Fatal(errJSON)
					}
					if !reflect.DeepEqual(row.Compat, &wantCompat) {
						t.Errorf("%s: responses compat %+v, NeoPi %+v", want.ID, row.Compat, wantCompat)
					}
				default:
					t.Errorf("%s: unexpected api %s", want.ID, want.API)
				}
			}
		})
	}
}

// discoverLlamaCppModelsAt awaits /models and /props with Promise.all, which
// rejects as soon as /models fails: a /props that never answers neither holds
// back the /models rejection nor keeps an unreachable server from yielding to
// its fallback. The abandoned /props request is cancelled.
func TestNeoPiLlamaCppModelsFailureDoesNotWaitForProps(t *testing.T) {
	roster := []byte(`{"data":[{"id":"Qwen3-8B-Q4_K_M"}]}`)
	cases := []struct {
		name      string
		primary   func() (int, []byte, error)
		wantHTTP  bool
		wantModel bool
	}{
		{name: "HTTP failure ends discovery", primary: func() (int, []byte, error) { return 401, nil, nil }, wantHTTP: true},
		{name: "transport failure moves on", primary: func() (int, []byte, error) { return 0, nil, errors.New("connection refused") }, wantModel: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cancelled := make(chan struct{})
			do := func(ctx context.Context, endpoint string) (int, []byte, error) {
				switch endpoint {
				case "http://primary.test/models":
					return tc.primary()
				case "http://primary.test/props":
					<-ctx.Done()
					close(cancelled)
					return 0, nil, ctx.Err()
				case "http://fallback.test/models":
					return 200, roster, nil
				}
				return 404, nil, nil
			}
			type outcome struct {
				models []*ModelInfo
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				models, err := NeoPiLlamaCppListing(t.Context(), []string{"http://primary.test", "http://fallback.test"}, do)
				done <- outcome{models, err}
			}()
			var got outcome
			select {
			case got = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("discovery waited on the primary's /props after its /models failed")
			}
			var httpErr *neoPiLlamaCppHTTPError
			if isHTTP := errors.As(got.err, &httpErr); isHTTP != tc.wantHTTP {
				t.Fatalf("err = %v; want HTTP failure %v", got.err, tc.wantHTTP)
			}
			if tc.wantModel && (got.err != nil || len(got.models) != 1) {
				t.Fatalf("fallback listing = %v, %v; want its one model", got.models, got.err)
			}
			select {
			case <-cancelled:
			case <-time.After(10 * time.Second):
				t.Fatal("the abandoned /props request was not cancelled")
			}
		})
	}
}

// normalizeLlamaCppBaseUrl reads the base with new URL, which maps an IDNA
// host to its ASCII form and rejects an invalid punycode label, leaving such
// a base as it is (expected values from Bun's URL).
func TestNeoPiLlamaCppBaseURLIDNA(t *testing.T) {
	for input, want := range map[string]string{
		"http://münchen.local:8080/v1/":    "http://xn--mnchen-3ya.local:8080/v1",
		"http://MÜNCHEN.local/v1":          "http://xn--mnchen-3ya.local/v1",
		"http://bücher.example:80/":        "http://xn--bcher-kva.example",
		"http://xn--mnchen-3ya.local/v1//": "http://xn--mnchen-3ya.local/v1",
		"http://XN--MNCHEN-3YA.local/v1":   "http://xn--mnchen-3ya.local/v1",
		"http://xn--ls8h.example/":         "http://xn--ls8h.example",
		"http://ab--cd.example/":           "http://ab--cd.example",
		"http://server.xn--/v1/":           "http://server.xn--/v1/",
		"http://xn--.example/v1":           "http://xn--.example/v1",
		"http://xn--zz.example/v1":         "http://xn--zz.example/v1",
		"http://xn--a.example/":            "http://xn--a.example/",
	} {
		if got := NeoPiLlamaCppBaseURL(input); got != want {
			t.Errorf("NeoPiLlamaCppBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
	if got := neoPiLlamaCppNativeBase(NeoPiLlamaCppBaseURL("http://münchen.local:8080/v1/")); got != "http://xn--mnchen-3ya.local:8080" {
		t.Errorf("native base %q, want http://xn--mnchen-3ya.local:8080", got)
	}
}
