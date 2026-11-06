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
	"strings"
	"sync"
	"testing"
	"time"
)

// The port discovers each captured LM Studio server as NeoPi's runtime does
// (testdata/neopi_lmstudio_runtime.json.gz, captured by
// capture_neopi_lmstudio_runtime.ts): the requests it sends, whether
// discovery fails, and every served row it builds, in order, with its
// resolved compat. Judge and embedding rows NeoPi builds are not served here.
func TestNeoPiLmStudioRuntimeMatchesNeoPi(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_lmstudio_runtime.json.gz")
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
			Scenario string                     `json:"scenario"`
			BaseURL  string                     `json:"baseUrl"`
			Replies  map[string]json.RawMessage `json:"replies"`
			Requests []struct {
				URL string `json:"url"`
			} `json:"requests"`
			Error  string `json:"error"`
			Models []struct {
				ID            string          `json:"id"`
				Name          string          `json:"name"`
				API           string          `json:"api"`
				Kind          string          `json:"kind"`
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
	noEnv := func(string) string { return "" }
	for _, result := range capture.Results {
		t.Run(result.Scenario, func(t *testing.T) {
			var mu sync.Mutex
			var asked []string
			base := NeoPiLmStudioBaseURL(result.BaseURL, noEnv)
			got, err := NeoPiLmStudioRuntimeListing(t.Context(), base, func(_ context.Context, endpoint string, _ bool) (int, []byte, error) {
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
			mu.Lock()
			slices.Sort(asked)
			mu.Unlock()
			slices.Sort(want)
			if result.Error != "" {
				// A failed listing returns without awaiting the native probes
				// NeoPi fired beside it, so they may not have gone out yet:
				// every request sent is one NeoPi sent, the listing included.
				mu.Lock()
				sent := slices.Clone(asked)
				mu.Unlock()
				for _, request := range sent {
					if !slices.Contains(want, request) {
						t.Errorf("asked %q, which NeoPi never asked (NeoPi asked %q)", request, want)
					}
				}
				if len(want) > 0 && !slices.ContainsFunc(sent, func(request string) bool { return strings.HasSuffix(request, "/v1/models") }) {
					t.Errorf("never asked the listing; NeoPi asked %q", want)
				}
				if !errors.Is(err, ErrNeoPiListing) {
					t.Fatalf("NeoPi's discovery failed (%s); port returned %d rows, err %v", result.Error, len(got), err)
				}
				return
			}
			if !reflect.DeepEqual(asked, want) {
				t.Errorf("asked %q\nNeoPi asked %q", asked, want)
			}
			if empty := (*NeoPiEmptyListing)(nil); errors.As(err, &empty) {
				got, err = empty.Models, nil
			}
			if err != nil {
				t.Fatalf("port rejected a roster NeoPi accepted: %v", err)
			}
			served := result.Models[:0:0]
			for _, model := range result.Models {
				if model.API == "openai-completions" && (model.Kind == "" || model.Kind == "chat") {
					served = append(served, model)
				}
			}
			if len(got) != len(served) {
				t.Fatalf("rows: port %d, NeoPi %d served", len(got), len(served))
			}
			for i, want := range served {
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
				var wantCompat NeoPiCompletionsCompat
				if errJSON := json.Unmarshal(want.Compat, &wantCompat); errJSON != nil {
					t.Fatal(errJSON)
				}
				if !reflect.DeepEqual(row.CompletionsCompat, &wantCompat) {
					t.Errorf("%s: completions compat differs from NeoPi's\n port %+v\nNeoPi %+v", want.ID, row.CompletionsCompat, wantCompat)
				}
			}
		})
	}
}

// discoverOpenAIModelsList awaits /models and the native probes with
// Promise.all, which rejects as soon as /models fails: the listing reports the
// failure without waiting for a native probe that never answers, and the
// probes it abandons are cancelled before it returns.
func TestNeoPiLmStudioListingFailureCancelsNatives(t *testing.T) {
	var cancelled [2]chan struct{}
	for i := range cancelled {
		cancelled[i] = make(chan struct{})
	}
	do := func(ctx context.Context, endpoint string, native bool) (int, []byte, error) {
		switch {
		case !native:
			return 401, nil, nil
		case strings.HasSuffix(endpoint, "/api/v0/models"):
			<-ctx.Done()
			close(cancelled[0])
		default:
			<-ctx.Done()
			close(cancelled[1])
		}
		return 0, nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := NeoPiLmStudioRuntimeListing(t.Context(), "http://127.0.0.1:1234/v1", do)
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("discovery waited on a native probe after /models failed")
	}
	var httpErr *neoPiLmStudioHTTPError
	if !errors.As(err, &httpErr) || httpErr.status != 401 {
		t.Fatalf("err = %v; want the /models HTTP 401", err)
	}
	for i, ch := range cancelled {
		select {
		case <-ch:
		default:
			t.Errorf("native probe %d still running when the listing returned", i)
		}
	}
}
