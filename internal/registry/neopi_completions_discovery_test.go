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

type neoPiCaptureReply struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type neoPiDiscoveryResult struct {
	Provider string              `json:"provider"`
	Scenario string              `json:"scenario"`
	Replies  []neoPiCaptureReply `json:"replies"`
	// Roster and Live are ClinePass's two hosts: Cline's recommended-models
	// and OpenRouter's catalog.
	Roster *neoPiCaptureReply `json:"roster"`
	Live   *neoPiCaptureReply `json:"live"`
	Source string             `json:"source"`
	Models []struct {
		ID            string         `json:"id"`
		API           string         `json:"api"`
		Name          string         `json:"name"`
		Reasoning     bool           `json:"reasoning"`
		Input         []string       `json:"input"`
		ContextWindow *int           `json:"contextWindow"`
		MaxTokens     *int           `json:"maxTokens"`
		BaseURL       string         `json:"baseUrl"`
		CompatIndex   int            `json:"compatIndex"`
		Thinking      *NeoPiThinking `json:"thinking"`
	} `json:"models"`
}

type neoPiDiscoveryCapture struct {
	Compats []json.RawMessage      `json:"compats"`
	Results []neoPiDiscoveryResult `json:"results"`
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
			got, err := neoPiReplayListing(baseURL, result)
			if result.Source == "bundled" {
				if err == nil {
					t.Fatalf("NeoPi kept the bundled roster; port resolved %d models", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("port rejected a listing NeoPi accepted: %v", err)
			}
			byID := map[string]*NeoPiModel{}
			for _, info := range got {
				byID[info.ID] = info.NeoPi
			}
			// The port serves the Chat Completions rows; NeoPi's image and
			// speech runners on the same provider are out of its scope.
			want := result.Models[:0:0]
			for _, model := range result.Models {
				if model.API == "openai-completions" {
					want = append(want, model)
				}
			}
			if len(byID) != len(want) {
				t.Errorf("roster size: port %d, NeoPi %d", len(byID), len(want))
			}
			for _, want := range want {
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
				gaps := neoPiRuleEngineGaps[result.Provider+"/"+want.ID]
				if equal := reflect.DeepEqual(row.Thinking, want.Thinking); !equal && !gaps["thinking"] {
					t.Errorf("%s: thinking %+v, NeoPi %+v", want.ID, row.Thinking, want.Thinking)
				} else if equal && gaps["thinking"] {
					t.Errorf("%s: thinking now matches NeoPi; drop it from neoPiRuleEngineGaps", want.ID)
				}
				// A discovered non-reasoning id takes its compat from its nearest
				// bundled sibling (see neoPiNewRow), which only approximates NeoPi's
				// id-derived compat; reasoning rows come from the captured derivation.
				if row.Reasoning && (bundledChanged(result.Provider, want.ID) || !neoPiBundledRow(result.Provider, want.ID).Reasoning) {
					var wantCompat NeoPiCompletionsCompat
					if err := json.Unmarshal(capture.Compats[want.CompatIndex], &wantCompat); err != nil {
						t.Fatal(err)
					}
					if equal := reflect.DeepEqual(row.CompletionsCompat, &wantCompat); !equal && !gaps["compat"] {
						t.Errorf("%s: compat differs from NeoPi's", want.ID)
					} else if equal && gaps["compat"] {
						t.Errorf("%s: compat now matches NeoPi; drop it from neoPiRuleEngineGaps", want.ID)
					}
				}
			}
		})
	}
}

// neoPiRuleEngineGaps are unbundled reasoning ids whose fields NeoPi's
// catalog rule engine derives from class rules the port cannot evaluate:
// Wafer serves a Qwen-backed id with the qwen thinking format, whose disable
// mode and ladder come from the qwen-format rules, and a GLM past the newest
// bundled revision takes the GLM class's generic ladder (minimal..high)
// rather than Wafer's provider-wide one (minimal..xhigh). A ClinePass id the
// bundle lacks takes its class's compat: Claude's Anthropic cache markers and
// GPT-5's ban on sampling parameters.
var neoPiRuleEngineGaps = map[string]map[string]bool{
	"wafer-serverless/zz-wafer-qwen": {"thinking": true, "compat": true},
	"wafer-serverless/GLM-5.4":       {"thinking": true},
	"cline-pass/claude-sonnet-4-6":   {"compat": true},
	"cline-pass/gpt-5.4-thinking":    {"compat": true},
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
		models, err := neoPiReplayListing(base, result)
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
		"cerebras":              {0, 4, 8, nil},
		"deepseek":              {0, 4, 4, nil},
		"fireworks":             {0, 5, 35, nil},
		"groq":                  {0, 4, 20, nil},
		"huggingface":           {0, 4, 82, nil},
		"mistral":               {0, 4, 44, nil},
		"moonshot":              {1, 11, 19, []string{"AlwaysSendMaxTokens", "DisableReasoningOnForcedToolChoice", "ReasoningDisableMode", "RequiresAssistantContentForToolCalls", "RequiresReasoningContentForToolCalls", "StreamRevision", "ThinkingFormat"}},
		"nanogpt":               {0, 6, 1197, nil},
		"nvidia":                {0, 4, 175, nil},
		"together":              {0, 4, 42, nil},
		"alibaba-token-plan":    {4, 12, 6, []string{"AlwaysSendMaxTokens", "DisableReasoningOnForcedToolChoice", "ReasoningDeltasMayBeCumulative", "RequiresAssistantContentForToolCalls", "RequiresReasoningContentForToolCalls", "StreamMarkupHealingPattern", "StreamRevision", "StripImageInput", "ThinkingLoopGuard", "ToolSchemaFlavor"}},
		"aiand":                 {0, 7, 6, nil},
		"baseten":               {0, 6, 8, nil},
		"coralbricks":           {0, 9, 2, nil},
		"deepinfra":             {0, 2, 2, nil},
		"gmi-cloud":             {0, 4, 1, nil},
		"helmcode":              {0, 5, 4, nil},
		"inception":             {0, 1, 1, nil},
		"novita":                {0, 1, 1, nil},
		"qianfan":               {0, 5, 1, nil},
		"qwen-portal":           {0, 4, 2, nil},
		"sambanova":             {0, 4, 1, nil},
		"stepfun":               {0, 5, 4, nil},
		"synthetic":             {0, 8, 6, nil},
		"venice":                {0, 4, 183, nil},
		"wafer-serverless":      {1, 10, 22, []string{"ReasoningDisableMode"}},
		"xiaomi-token-plan-sgp": {0, 4, 6, nil},
		"xiaomi-token-plan-ams": {0, 4, 6, nil},
		"xiaomi-token-plan-cn":  {0, 4, 7, nil},
		"yolo-auto":             {0, 6, 4, nil},
		"zhipu-coding-plan":     {0, 8, 6, nil},
		"coreweave":             {0, 4, 44, nil},
		"cline-pass":            {3, 16, 20, []string{"CacheControlFormat", "SupportsForcedToolChoice", "SupportsSamplingParams", "SupportsStore"}},
		"aimlapi":               {1, 6, 5, []string{"DisableReasoningOnForcedToolChoice"}},
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

// neoPiReplayListing resolves a captured scenario's replies the way the
// discovery client does for its provider.
func neoPiReplayListing(baseURL string, result neoPiDiscoveryResult) ([]*ModelInfo, error) {
	switch {
	case result.Provider == "fireworks":
		return neoPiReplayFireworks(baseURL, result.Replies)
	case result.Roster != nil:
		if result.Roster.Status/100 != 2 {
			return nil, ErrNeoPiListing
		}
		var live []byte
		if result.Live != nil && result.Live.Status/100 == 2 {
			live = []byte(result.Live.Body)
		}
		return NeoPiClinePassListing(baseURL, []byte(result.Roster.Body), live)
	case len(result.Replies) == 0 || result.Replies[0].Status/100 != 2:
		return nil, ErrNeoPiListing
	}
	return NeoPiCompletionsListing(result.Provider, baseURL, []byte(result.Replies[0].Body))
}

func neoPiReplayFireworks(baseURL string, replies []neoPiCaptureReply) ([]*ModelInfo, error) {
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
