package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sync"
	"testing"
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
			got, err := NeoPiLlamaCppListing(bases, func(endpoint string) (int, []byte, error) {
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
