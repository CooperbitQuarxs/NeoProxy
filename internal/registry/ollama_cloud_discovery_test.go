package registry

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// ollamaCloudDiscoveryCapture is testdata/ollama_cloud_discovery.json:
// NeoPi's OWN resolveProviderModels(ollamaCloudModelManagerOptions) against
// a scripted Ollama (capture_ollama_cloud_discovery.ts).
type ollamaCloudDiscoveryCapture struct {
	Scenarios []struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseUrl"`
		Tags    struct {
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		} `json:"tags"`
		Shows map[string]struct {
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		} `json:"shows"`
		Requests []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			Body   string `json:"body"`
		} `json:"requests"`
		Source string `json:"source"`
		Models []struct {
			ID            string   `json:"id"`
			Name          string   `json:"name"`
			API           string   `json:"api"`
			Provider      string   `json:"provider"`
			BaseURL       string   `json:"baseUrl"`
			Reasoning     bool     `json:"reasoning"`
			Input         []string `json:"input"`
			ContextWindow int      `json:"contextWindow"`
			MaxTokens     int      `json:"maxTokens"`
			Thinking      *struct {
				Efforts        []string          `json:"efforts"`
				EffortMap      map[string]string `json:"effortMap"`
				Mode           string            `json:"mode"`
				RequiresEffort bool              `json:"requiresEffort"`
				EffortRouting  map[string]string `json:"effortRouting"`
			} `json:"thinking"`
			Compat struct {
				StripThinkingHistory bool `json:"stripThinkingHistory"`
			} `json:"compat"`
			OmitMaxOutputTokens bool    `json:"omitMaxOutputTokens"`
			RequestModelID      *string `json:"requestModelId"`
		} `json:"models"`
	} `json:"scenarios"`
}

// rawBody is a scripted reply body as it went over the wire: a JSON string
// stood for itself, anything else was JSON-encoded.
func rawBody(body json.RawMessage) []byte {
	var text string
	if json.Unmarshal(body, &text) == nil {
		return []byte(text)
	}
	return body
}

// An /api/tags listing and its /api/show answers resolve into the roster
// NeoPi's model manager produced: capabilities over references, discovered
// context windows, the trusted and capped output limits, global references,
// the additive merge over the bundled rows in their order, collapsed
// thinking pairs, and the bundled roster for an empty listing.
func TestOllamaCloudListingMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/ollama_cloud_discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture ollamaCloudDiscoveryCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Scenarios) == 0 {
		t.Fatal("capture has no scenarios")
	}
	for _, tc := range capture.Scenarios {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Tags.Status != 200 {
				t.Skip("a refused listing keeps the bundled roster (executor-level)")
			}
			base := "https://ollama.com"
			if tc.BaseURL != "" {
				base = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(tc.BaseURL), "/"), "/api")
			}
			// The ids NeoPi asked /api/show about, in order.
			_, ids, ok := OllamaCloudTagIDs(rawBody(tc.Tags.Body))
			if !ok {
				t.Fatal("tags body unreadable")
			}
			var asked []string
			for _, request := range tc.Requests {
				if request.Path == "/api/show" {
					var show struct {
						Model string `json:"model"`
					}
					_ = json.Unmarshal([]byte(request.Body), &show)
					asked = append(asked, show.Model)
				}
			}
			var listed []string
			for _, id := range ids {
				if id != "" {
					listed = append(listed, id)
				}
			}
			if !reflect.DeepEqual(asked, listed) && !(len(asked) == 0 && len(listed) == 0) {
				t.Errorf("show ids = %q, want %q", listed, asked)
			}
			shows := map[string][]byte{}
			for id, reply := range tc.Shows {
				if reply.Status == 200 {
					shows[id] = rawBody(reply.Body)
				}
			}
			models, errListing := OllamaCloudListing(base, rawBody(tc.Tags.Body), shows)
			var empty *NeoPiEmptyListing
			if errors.As(errListing, &empty) {
				models, errListing = empty.Models, nil
			}
			if errListing != nil {
				t.Fatal(errListing)
			}
			if len(models) != len(tc.Models) {
				got := make([]string, len(models))
				for i, model := range models {
					got[i] = model.ID
				}
				t.Fatalf("models = %d %q, want %d", len(models), got, len(tc.Models))
			}
			for i, info := range models {
				want := tc.Models[i]
				row := info.NeoPi
				if row.ID != want.ID || row.Name != want.Name || row.API != want.API || row.Provider != want.Provider || row.BaseURL != want.BaseURL {
					t.Errorf("model %d = %s %q %s %s %s, want %s %q %s %s %s", i, row.ID, row.Name, row.API, row.Provider, row.BaseURL, want.ID, want.Name, want.API, want.Provider, want.BaseURL)
					continue
				}
				if row.Reasoning != want.Reasoning || !reflect.DeepEqual(row.Input, want.Input) || row.ContextWindow != want.ContextWindow || row.MaxTokens != want.MaxTokens {
					t.Errorf("%s: reasoning %v input %v context %d max %d, want %v %v %d %d", row.ID, row.Reasoning, row.Input, row.ContextWindow, row.MaxTokens, want.Reasoning, want.Input, want.ContextWindow, want.MaxTokens)
				}
				if row.OllamaCompat == nil || row.OllamaCompat.StripThinkingHistory != want.Compat.StripThinkingHistory || row.OmitMaxOutputTokens != want.OmitMaxOutputTokens {
					t.Errorf("%s: compat %+v omit %v, want %+v omit %v", row.ID, row.OllamaCompat, row.OmitMaxOutputTokens, want.Compat, want.OmitMaxOutputTokens)
				}
				wantRequest := ""
				if want.RequestModelID != nil {
					wantRequest = *want.RequestModelID
				}
				if row.RequestModelID != wantRequest {
					t.Errorf("%s: request id %q, want %q", row.ID, row.RequestModelID, wantRequest)
				}
				switch {
				case want.Thinking == nil && row.Thinking != nil:
					t.Errorf("%s: thinking %+v, want none", row.ID, row.Thinking)
				case want.Thinking != nil && row.Thinking == nil:
					t.Errorf("%s: no thinking, want %+v", row.ID, want.Thinking)
				case want.Thinking != nil:
					if !reflect.DeepEqual(row.Thinking.Efforts, want.Thinking.Efforts) || row.Thinking.Mode != want.Thinking.Mode || row.Thinking.RequiresEffort != want.Thinking.RequiresEffort ||
						len(row.Thinking.EffortMap)+len(want.Thinking.EffortMap) > 0 && !reflect.DeepEqual(row.Thinking.EffortMap, want.Thinking.EffortMap) ||
						len(row.Thinking.EffortRouting)+len(want.Thinking.EffortRouting) > 0 && !reflect.DeepEqual(row.Thinking.EffortRouting, want.Thinking.EffortRouting) {
						t.Errorf("%s: thinking %+v, want %+v", row.ID, row.Thinking, want.Thinking)
					}
				}
			}
		})
	}
}
