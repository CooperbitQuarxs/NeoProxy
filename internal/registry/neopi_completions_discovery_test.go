package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
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
	// BaseURL is the provider base the scenario's account configures;
	// empty is the manager's default.
	BaseURL string `json:"baseUrl"`
	// Keyless scenarios build the manager without a key: only a public
	// listing still fetches.
	Keyless bool   `json:"keyless"`
	Source  string `json:"source"`
	// ModelsDev is the models.dev catalog's answer (SiliconFlow); absent, it
	// answered 503.
	ModelsDev *neoPiCaptureReply `json:"modelsDev"`
	// Native holds LM Studio's /api/v0 and /api/v1 answers; absent, they
	// answered 404.
	Native struct {
		V0 *neoPiCaptureReply `json:"v0"`
		V1 *neoPiCaptureReply `json:"v1"`
	} `json:"native"`
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
		Cost          NeoPiCost      `json:"cost"`
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
			if result.BaseURL != "" {
				baseURL = NeoPiV1BaseURL(result.Provider, result.BaseURL)
			}
			got, err := neoPiReplayListing(baseURL, result)
			// An empty listing is a short-lived success that serves the
			// bundled rows the merge keeps.
			if empty := (*NeoPiEmptyListing)(nil); errors.As(err, &empty) {
				got, err = empty.Models, nil
			}
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
			// The port serves NeoPi's chat rows over Chat Completions,
			// Anthropic Messages or Responses and its image and speech rows
			// over their runner transports. Rows a rule classifies as a
			// runner kind while they stay on a chat transport (which NeoPi
			// keeps out of chat), and rows on other transports (a judge's),
			// are not served.
			want := result.Models[:0:0]
			for _, model := range result.Models {
				runner := model.API == "openai-images" || model.API == "openai-speech"
				chat := model.API == "openai-completions" || model.API == "anthropic-messages" || model.API == "openai-responses"
				if runner || chat && (model.Kind == "" || model.Kind == "chat") {
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
				if row.cost != want.Cost {
					t.Errorf("%s: cost %+v, NeoPi %+v", want.ID, row.cost, want.Cost)
				}
				switch want.API {
				case "openai-completions":
					var wantCompat NeoPiCompletionsCompat
					if err := json.Unmarshal(capture.Compats[want.CompatIndex], &wantCompat); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(row.CompletionsCompat, &wantCompat) {
						t.Errorf("%s: compat differs from NeoPi's", want.ID)
					}
				case "anthropic-messages":
					var wantCompat NeoPiAnthropicCompat
					if err := json.Unmarshal(capture.Compats[want.CompatIndex], &wantCompat); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(row.AnthropicCompat, &wantCompat) {
						t.Errorf("%s: anthropic compat %+v, NeoPi %+v", want.ID, row.AnthropicCompat, wantCompat)
					}
				case "openai-responses":
					var wantCompat NeoPiCompat
					if err := json.Unmarshal(capture.Compats[want.CompatIndex], &wantCompat); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(row.Compat, &wantCompat) {
						t.Errorf("%s: responses compat %+v, NeoPi %+v", want.ID, row.Compat, wantCompat)
					}
				}
			}
		})
	}
}

// Every compat field NeoPi resolves for a completions row has a home in the
// Go record: a field the record lacked would be silently dropped before the
// parity comparison. (The Anthropic record keeps only the fields NeoPi's
// Anthropic provider reads for a third-party model.)
func TestNeoPiCompletionsCapturedCompatFieldsKnown(t *testing.T) {
	capture := loadNeoPiDiscoveryCapture(t)
	completions := map[int]bool{}
	for _, result := range capture.Results {
		for _, model := range result.Models {
			if model.API == "openai-completions" {
				completions[model.CompatIndex] = true
			}
		}
	}
	for i, raw := range capture.Compats {
		if !completions[i] {
			continue
		}
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
	case result.Keyless && !NeoPiCompletionsDiscoversWithoutKey(result.Provider):
		return nil, ErrNeoPiListing
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
			models, err = NeoPiCompletionsListing("xiaomi", NeoPiCompletionsListingBase(region), []byte(reply.Body))
			if empty := (*NeoPiEmptyListing)(nil); err == nil || errors.As(err, &empty) {
				return models, err
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
	if result.Provider == "lm-studio" {
		// fetchJson: only a 2xx native answer carries metadata.
		body := func(reply *neoPiCaptureReply) []byte {
			if reply == nil || reply.Status/100 != 2 {
				return nil
			}
			return []byte(reply.Body)
		}
		return NeoPiLmStudioListing(baseURL, []byte(result.Replies[0].Body), body(result.Native.V0), body(result.Native.V1))
	}
	if result.Provider == "siliconflow" || result.Provider == "siliconflow-cn" {
		var modelsDev []byte
		if result.ModelsDev != nil && result.ModelsDev.Status/100 == 2 {
			modelsDev = []byte(result.ModelsDev.Body)
		}
		return NeoPiSiliconFlowListing(result.Provider, baseURL, []byte(result.Replies[0].Body), modelsDev)
	}
	if result.Provider == "cloudflare-ai-gateway" {
		// cloudflareAiGatewayModelManagerOptions lists from its Anthropic base.
		return NeoPiCompletionsListing(result.Provider, NeoPiCloudflareListingBase(result.BaseURL), []byte(result.Replies[0].Body))
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
