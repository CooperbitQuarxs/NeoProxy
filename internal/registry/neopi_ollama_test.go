package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sync"
	"testing"
)

// neoPiOllamaCapture is testdata/neopi_ollama_discovery.json.gz, written by
// capture_neopi_ollama_discovery.ts from NeoPi's own runtime discovery.
type neoPiOllamaCapture struct {
	Results []struct {
		Scenario      string                       `json:"scenario"`
		BaseURL       string                       `json:"baseUrl"`
		ContextLength *string                      `json:"contextLength"`
		Tags          neoPiCaptureReply            `json:"tags"`
		Shows         map[string]neoPiCaptureReply `json:"shows"`
		Requests      []struct {
			Method  string            `json:"method"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
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
	ImplicitBase []struct {
		Env  map[string]string `json:"env"`
		Base string            `json:"base"`
	} `json:"implicitBase"`
	ContextOverride []struct {
		Value    string `json:"value"`
		Override *int   `json:"override"`
	} `json:"contextOverride"`
}

func loadNeoPiOllamaCapture(t *testing.T) *neoPiOllamaCapture {
	t.Helper()
	raw, err := os.ReadFile("testdata/neopi_ollama_discovery.json.gz")
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
	capture := &neoPiOllamaCapture{}
	if err := json.Unmarshal(data, capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// The port discovers each captured server as NeoPi's runtime does: the
// requests it sends (method, URL, body), whether the discovery fails, and
// every row it builds, in order, with its resolved Responses compat.
func TestNeoPiOllamaDiscoveryMatchesNeoPi(t *testing.T) {
	capture := loadNeoPiOllamaCapture(t)
	if len(capture.Results) == 0 {
		t.Fatal("capture holds no scenarios")
	}
	for _, result := range capture.Results {
		t.Run(result.Scenario, func(t *testing.T) {
			getenv := func(name string) string {
				if name == "OLLAMA_CONTEXT_LENGTH" && result.ContextLength != nil {
					return *result.ContextLength
				}
				return ""
			}
			var mu sync.Mutex
			var asked []string
			got, err := NeoPiOllamaListing(result.BaseURL, getenv, func(endpoint string, body []byte) (int, []byte, error) {
				parsed, errParse := url.Parse(endpoint)
				if errParse != nil {
					return 0, nil, errParse
				}
				method := "GET"
				if body != nil {
					method = "POST"
				}
				mu.Lock()
				asked = append(asked, method+" "+parsed.String()+" "+string(body))
				mu.Unlock()
				reply, ok := neoPiCaptureReply{}, false
				switch {
				case parsed.Path == "/api/tags":
					reply, ok = result.Tags, true
				case parsed.Path == "/api/show":
					var request struct {
						Model any `json:"model"`
					}
					if errJSON := json.Unmarshal(body, &request); errJSON != nil {
						return 0, nil, errJSON
					}
					reply, ok = result.Shows[fmt.Sprint(request.Model)]
				}
				if !ok {
					return 404, []byte("not found"), nil
				}
				return reply.Status, []byte(reply.Body), nil
			})
			var want []string
			for _, request := range result.Requests {
				want = append(want, request.Method+" "+request.URL+" "+request.Body)
			}
			// The lookups run concurrently, in any order.
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
				// NeoProxy's limits are whole tokens; NeoPi keeps a fractional window.
				if row.ContextWindow != int(want.ContextWindow) || row.MaxTokens != int(want.MaxTokens) {
					t.Errorf("%s: limits %d/%d, NeoPi %v/%v", want.ID, row.ContextWindow, row.MaxTokens, want.ContextWindow, want.MaxTokens)
				}
				if !reflect.DeepEqual(row.Thinking, want.Thinking) {
					t.Errorf("%s: thinking %+v, NeoPi %+v", want.ID, row.Thinking, want.Thinking)
				}
				var wantCompat NeoPiCompat
				if errJSON := json.Unmarshal(want.Compat, &wantCompat); errJSON != nil {
					t.Fatal(errJSON)
				}
				if !reflect.DeepEqual(row.Compat, &wantCompat) {
					t.Errorf("%s: responses compat %+v, NeoPi %+v", want.ID, row.Compat, wantCompat)
				}
			}
		})
	}
}

// An account that configures no address reaches NeoPi's implicit server
// (OLLAMA_BASE_URL, else OLLAMA_HOST, else the default), and
// OLLAMA_CONTEXT_LENGTH overrides only as a positive safe integer.
func TestNeoPiOllamaEnvironmentMatchesNeoPi(t *testing.T) {
	capture := loadNeoPiOllamaCapture(t)
	for _, want := range capture.ImplicitBase {
		getenv := func(name string) string { return want.Env[name] }
		if got := neoPiOllamaImplicitBase(getenv); got != want.Base {
			t.Errorf("implicit base for %v: %q, NeoPi %q", want.Env, got, want.Base)
		}
	}
	for _, want := range capture.ContextOverride {
		getenv := func(name string) string {
			if name == "OLLAMA_CONTEXT_LENGTH" {
				return want.Value
			}
			return ""
		}
		wantOverride := 0
		if want.Override != nil {
			wantOverride = *want.Override
		}
		if got := neoPiOllamaContextOverride(getenv); got != wantOverride {
			t.Errorf("OLLAMA_CONTEXT_LENGTH=%q: %d, NeoPi %d", want.Value, got, wantOverride)
		}
	}
	if len(capture.ImplicitBase) == 0 || len(capture.ContextOverride) == 0 {
		t.Fatal("capture holds no environment cases")
	}
}
