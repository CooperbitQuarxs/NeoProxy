package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"
)

type neoPiDiscoveryCapture struct {
	Compats []json.RawMessage `json:"compats"`
	Results []struct {
		Provider string `json:"provider"`
		Scenario string `json:"scenario"`
		Replies  []struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		} `json:"replies"`
		Source string `json:"source"`
		Models []struct {
			ID            string         `json:"id"`
			Name          string         `json:"name"`
			Reasoning     bool           `json:"reasoning"`
			Input         []string       `json:"input"`
			ContextWindow *int           `json:"contextWindow"`
			MaxTokens     *int           `json:"maxTokens"`
			BaseURL       string         `json:"baseUrl"`
			CompatIndex   int            `json:"compatIndex"`
			Thinking      *NeoPiThinking `json:"thinking"`
		} `json:"models"`
	} `json:"results"`
}

func loadNeoPiDiscoveryCapture(t *testing.T) *neoPiDiscoveryCapture {
	t.Helper()
	raw, err := os.ReadFile("testdata/neopi_completions_discovery.json.gz")
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
	capture := &neoPiDiscoveryCapture{}
	if err := json.Unmarshal(data, capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

// TestNeoPiCompletionsDiscoveryParity replays the listings NeoPi's own model
// managers were captured against and compares the rosters they resolve.
func TestNeoPiCompletionsDiscoveryParity(t *testing.T) {
	capture := loadNeoPiDiscoveryCapture(t)
	if len(capture.Results) == 0 {
		t.Fatal("capture holds no scenarios")
	}
	for _, result := range capture.Results {
		t.Run(result.Provider+"/"+result.Scenario, func(t *testing.T) {
			baseURL := NeoPiCompletionsListingBase(result.Provider)
			var got []*ModelInfo
			var err error
			if result.Provider == "fireworks" {
				got, err = neoPiReplayFireworks(baseURL, result.Replies)
			} else {
				reply := result.Replies[0]
				if reply.Status/100 != 2 {
					err = ErrNeoPiListing
				} else {
					got, err = NeoPiCompletionsListing(result.Provider, baseURL, []byte(reply.Body))
				}
			}
			if result.Source == "bundled" {
				if err == nil {
					t.Fatalf("NeoPi kept the bundled roster; port resolved %d models", len(got))
				}
				return
			}
			if err != nil {
				if result.Provider == "alibaba-token-plan" && len(result.Models) == 0 {
					// NeoPi serves an empty roster for one cycle; the port keeps
					// the bundled one instead (documented divergence).
					return
				}
				t.Fatalf("port rejected a listing NeoPi accepted: %v", err)
			}
			byID := map[string]*NeoPiModel{}
			for _, info := range got {
				byID[info.ID] = info.NeoPi
			}
			if len(byID) != len(result.Models) {
				t.Errorf("roster size: port %d, NeoPi %d", len(byID), len(result.Models))
			}
			for _, want := range result.Models {
				row := byID[want.ID]
				if row == nil {
					t.Errorf("%s: missing from port roster", want.ID)
					continue
				}
				if row.Name != want.Name {
					t.Errorf("%s: name %q, NeoPi %q", want.ID, row.Name, want.Name)
				}
				if row.Reasoning != want.Reasoning {
					t.Errorf("%s: reasoning %v, NeoPi %v", want.ID, row.Reasoning, want.Reasoning)
				}
				if !reflect.DeepEqual(row.Input, want.Input) {
					t.Errorf("%s: input %v, NeoPi %v", want.ID, row.Input, want.Input)
				}
				if wantContext := neoPiCaptureLimit(want.ContextWindow); row.ContextWindow != wantContext {
					t.Errorf("%s: context %d, NeoPi %d", want.ID, row.ContextWindow, wantContext)
				}
				if wantMax := neoPiCaptureLimit(want.MaxTokens); row.MaxTokens != wantMax {
					t.Errorf("%s: max tokens %d, NeoPi %d", want.ID, row.MaxTokens, wantMax)
				}
				if row.BaseURL != want.BaseURL {
					t.Errorf("%s: base URL %q, NeoPi %q", want.ID, row.BaseURL, want.BaseURL)
				}
				if !reflect.DeepEqual(row.Thinking, want.Thinking) {
					t.Errorf("%s: thinking %+v, NeoPi %+v", want.ID, row.Thinking, want.Thinking)
				}
				// A discovered non-reasoning id takes its compat from its nearest
				// bundled sibling (see neoPiNewRow), which only approximates NeoPi's
				// id-derived compat; reasoning rows come from the captured derivation.
				if row.Reasoning && (bundledChanged(result.Provider, want.ID) || !neoPiBundledRow(result.Provider, want.ID).Reasoning) {
					var wantCompat NeoPiCompletionsCompat
					if err := json.Unmarshal(capture.Compats[want.CompatIndex], &wantCompat); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(row.CompletionsCompat, &wantCompat) {
						t.Errorf("%s: compat differs from NeoPi's", want.ID)
					}
				}
			}
		})
	}
}

// TestNeoPiCompletionsCapturedCompat compares every resolved compat field in
// the public-origin capture, including bundled and newly discovered IDs.
func TestNeoPiCompletionsCapturedCompat(t *testing.T) {
	capture := loadNeoPiDiscoveryCapture(t)
	// Fail if the capture contains a compat field the Go representation would
	// otherwise silently discard before comparison.
	for i, raw := range capture.Compats {
		var compat NeoPiCompletionsCompat
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&compat); err != nil {
			t.Fatalf("captured compat %d: %v", i, err)
		}
	}
	type counts struct {
		seen          map[string]bool
		mismatches    map[string]bool
		fields        map[string]bool
		knownSeen     map[string]bool
		knownMismatch map[string]bool
	}
	byProvider := map[string]*counts{}
	for _, result := range capture.Results {
		if result.Source == "bundled" {
			continue
		}
		base := NeoPiCompletionsListingBase(result.Provider)
		var models []*ModelInfo
		var err error
		if result.Provider == "fireworks" {
			models, err = neoPiReplayFireworks(base, result.Replies)
		} else {
			if len(result.Replies) == 0 || result.Replies[0].Status/100 != 2 {
				continue
			}
			models, err = NeoPiCompletionsListing(result.Provider, base, []byte(result.Replies[0].Body))
		}
		if err != nil {
			continue
		}
		if byProvider[result.Provider] == nil {
			byProvider[result.Provider] = &counts{seen: map[string]bool{}, mismatches: map[string]bool{}, fields: map[string]bool{}, knownSeen: map[string]bool{}, knownMismatch: map[string]bool{}}
		}
		c := byProvider[result.Provider]
		got := map[string]*NeoPiModel{}
		for _, info := range models {
			got[info.ID] = info.NeoPi
		}
		for _, expected := range result.Models {
			unknown := bundledChanged(result.Provider, expected.ID)
			actual := got[expected.ID]
			if actual == nil || actual.CompletionsCompat == nil {
				continue
			}
			if unknown {
				c.seen[expected.ID] = true
			} else {
				c.knownSeen[expected.ID] = true
			}
			var want NeoPiCompletionsCompat
			if err := json.Unmarshal(capture.Compats[expected.CompatIndex], &want); err != nil {
				t.Fatal(err)
			}
			haveValue, wantValue := reflect.ValueOf(*actual.CompletionsCompat), reflect.ValueOf(want)
			for i := 0; i < haveValue.NumField(); i++ {
				if !reflect.DeepEqual(haveValue.Field(i).Interface(), wantValue.Field(i).Interface()) {
					if unknown {
						c.mismatches[expected.ID] = true
					} else {
						c.knownMismatch[expected.ID] = true
					}
					c.fields[haveValue.Type().Field(i).Name] = true
				}
			}
		}
	}
	// Pin the measured exceptions until NeoPi's catalog rule evaluator is
	// ported. Any additional mismatch (or a correction) requires review.
	expected := map[string]struct {
		unknown, total, bundled int
		fields                  []string
	}{
		"cerebras":           {0, 4, 8, nil},
		"deepseek":           {0, 4, 4, nil},
		"fireworks":          {0, 5, 35, nil},
		"groq":               {0, 4, 20, nil},
		"huggingface":        {0, 4, 82, nil},
		"mistral":            {0, 4, 44, nil},
		"moonshot":           {1, 11, 19, []string{"AlwaysSendMaxTokens", "DisableReasoningOnForcedToolChoice", "ReasoningDisableMode", "RequiresAssistantContentForToolCalls", "RequiresReasoningContentForToolCalls", "StreamRevision", "ThinkingFormat"}},
		"nanogpt":            {0, 6, 1197, nil},
		"nvidia":             {0, 4, 175, nil},
		"together":           {0, 4, 42, nil},
		"alibaba-token-plan": {4, 12, 6, []string{"AlwaysSendMaxTokens", "DisableReasoningOnForcedToolChoice", "ReasoningDeltasMayBeCumulative", "RequiresAssistantContentForToolCalls", "RequiresReasoningContentForToolCalls", "StreamMarkupHealingPattern", "StreamRevision", "StripImageInput", "ThinkingLoopGuard", "ToolSchemaFlavor"}},
	}
	if len(byProvider) != len(expected) {
		t.Fatalf("measured %d providers, want %d", len(byProvider), len(expected))
	}
	for provider, want := range expected {
		c := byProvider[provider]
		if c == nil {
			t.Errorf("%s: no capture", provider)
			continue
		}
		fields := map[string]bool{}
		for _, field := range want.fields {
			fields[field] = true
		}
		if len(c.mismatches) != want.unknown || len(c.seen) != want.total || len(c.knownMismatch) != 0 || len(c.knownSeen) != want.bundled || !reflect.DeepEqual(c.fields, fields) {
			t.Errorf("%s: unknown %d/%d, bundled %d/%d, fields %v; expected unknown %d/%d, bundled 0/%d, fields %v", provider, len(c.mismatches), len(c.seen), len(c.knownMismatch), len(c.knownSeen), c.fields, want.unknown, want.total, want.bundled, fields)
		}
	}
}

// bundledChanged reports whether id is absent from the bundle, so its row is
// built from the discovery alone.
func bundledChanged(provider, id string) bool { return neoPiBundledRow(provider, id) == nil }

func neoPiCaptureLimit(limit *int) int {
	if limit == nil {
		return 0
	}
	return *limit
}

func neoPiReplayFireworks(baseURL string, replies []struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}) ([]*ModelInfo, error) {
	var records []json.RawMessage
	for _, reply := range replies {
		if reply.Status/100 != 2 {
			return nil, ErrNeoPiListing
		}
		page, next, err := NeoPiFireworksPage([]byte(reply.Body))
		if err != nil {
			return nil, err
		}
		records = append(records, page...)
		if next == "" {
			break
		}
	}
	return NeoPiFireworksListing(baseURL, records), nil
}
