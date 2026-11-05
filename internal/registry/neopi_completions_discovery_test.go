package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
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
	// APIKey is the scenario's own key, when not the capture's: a Xiaomi
	// Token Plan key walks the regional clusters.
	APIKey string `json:"apiKey"`
	Source string `json:"source"`
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
		Kind          string         `json:"kind"`
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
			// The port serves NeoPi's chat rows over Chat Completions and its
			// image rows over openai-images. Speech runners, and rows a rule
			// classifies as a runner kind while they stay on the completions
			// transport (which NeoPi keeps out of chat), are not served.
			want := result.Models[:0:0]
			for _, model := range result.Models {
				if model.API == "openai-images" || model.API == "openai-completions" && (model.Kind == "" || model.Kind == "chat") {
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
				if row.API != want.API || row.Kind != want.Kind {
					t.Errorf("%s: api %s kind %q, NeoPi %s kind %q", want.ID, row.API, row.Kind, want.API, want.Kind)
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
				if want.API == "openai-images" {
					continue
				}
				var wantCompat NeoPiCompletionsCompat
				if err := json.Unmarshal(capture.Compats[want.CompatIndex], &wantCompat); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(row.CompletionsCompat, &wantCompat) {
					t.Errorf("%s: compat differs from NeoPi's", want.ID)
				}
			}
		})
	}
}

// Every compat field NeoPi resolves has a home in the Go record: a field the
// record lacked would be silently dropped before the parity comparison.
func TestNeoPiCompletionsCapturedCompatFieldsKnown(t *testing.T) {
	capture := loadNeoPiDiscoveryCapture(t)
	for i, raw := range capture.Compats {
		var compat NeoPiCompletionsCompat
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&compat); err != nil {
			t.Fatalf("captured compat %d: %v", i, err)
		}
	}
}

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
	case result.Provider == "xiaomi" && (strings.HasPrefix(result.APIKey, "tp-") || strings.HasPrefix(result.APIKey, "ttp-")):
		// One reply per region tried, SGP then AMS then CN; the last reply
		// answers every further request.
		var err error = ErrNeoPiListing
		for i, region := range []string{"xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn"} {
			reply := result.Replies[min(i, len(result.Replies)-1)]
			if reply.Status/100 != 2 {
				continue
			}
			var models []*ModelInfo
			if models, err = NeoPiCompletionsListing("xiaomi", NeoPiCompletionsListingBase(region), []byte(reply.Body)); err == nil {
				return models, nil
			}
		}
		return nil, err
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
	return NeoPiFireworksListing(baseURL, records)
}
