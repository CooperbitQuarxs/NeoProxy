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

type githubCopilotDiscoveryCapture struct {
	Compats []json.RawMessage `json:"compats"`
	Results []struct {
		Scenario string `json:"scenario"`
		Models   struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		} `json:"models"`
		Requests []struct {
			URL string `json:"url"`
		} `json:"requests"`
		Source   string `json:"source"`
		Resolved []struct {
			ID             string         `json:"id"`
			API            string         `json:"api"`
			Name           string         `json:"name"`
			Reasoning      bool           `json:"reasoning"`
			Input          []string       `json:"input"`
			ContextWindow  *int           `json:"contextWindow"`
			MaxTokens      *int           `json:"maxTokens"`
			BaseURL        string         `json:"baseUrl"`
			RequestModelID string         `json:"requestModelId"`
			CompatIndex    int            `json:"compatIndex"`
			Thinking       *NeoPiThinking `json:"thinking"`
			Cost           NeoPiCost      `json:"cost"`
		} `json:"resolved"`
	} `json:"results"`
}

// The port resolves each captured /models answer into the roster NeoPi's own
// GitHub Copilot model manager resolved (testdata/
// capture_github_copilot_discovery.ts): the same rows in the same order, each
// on the same API and endpoint with the same limits, modalities, ladder,
// tariff, wire id and compat; and NeoPi's failed or empty listings keep the
// bundled roster.
func TestGitHubCopilotDiscoveryMatchesNeoPi(t *testing.T) {
	file, err := os.ReadFile("testdata/github_copilot_discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture githubCopilotDiscoveryCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Results) == 0 {
		t.Fatal("capture holds no scenarios")
	}
	for _, result := range capture.Results {
		t.Run(result.Scenario, func(t *testing.T) {
			// The listing base is where NeoPi sent GET /models.
			var requestBaseURL string
			for _, request := range result.Requests {
				if base, ok := strings.CutSuffix(request.URL, "/models"); ok {
					requestBaseURL = base
				}
			}
			if requestBaseURL == "" {
				t.Fatal("capture recorded no /models request")
			}
			if result.Models.Status/100 != 2 {
				if result.Source != "bundled" {
					t.Fatalf("NeoPi served %s after HTTP %d", result.Source, result.Models.Status)
				}
				return
			}
			got, err := GitHubCopilotListing(requestBaseURL, []byte(result.Models.Body))
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
			var gotIDs, wantIDs []string
			for _, info := range got {
				gotIDs = append(gotIDs, info.ID)
			}
			for _, want := range result.Resolved {
				wantIDs = append(wantIDs, want.ID)
			}
			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("roster\n port %v\nNeoPi %v", gotIDs, wantIDs)
			}
			for i, want := range result.Resolved {
				row := got[i].NeoPi
				if row.API != want.API || row.Name != want.Name || row.Reasoning != want.Reasoning || row.BaseURL != want.BaseURL || row.RequestModelID != want.RequestModelID {
					t.Errorf("%s: api %s name %q reasoning %v base %q wire %q; NeoPi %s %q %v %q %q", want.ID, row.API, row.Name, row.Reasoning, row.BaseURL,
						row.RequestModelID, want.API, want.Name, want.Reasoning, want.BaseURL, want.RequestModelID)
				}
				if !reflect.DeepEqual(row.Input, want.Input) {
					t.Errorf("%s: input %v, NeoPi %v", want.ID, row.Input, want.Input)
				}
				if row.ContextWindow != neoPiCaptureLimit(want.ContextWindow) || row.MaxTokens != neoPiCaptureLimit(want.MaxTokens) {
					t.Errorf("%s: limits %d/%d, NeoPi %d/%d", want.ID, row.ContextWindow, row.MaxTokens, neoPiCaptureLimit(want.ContextWindow), neoPiCaptureLimit(want.MaxTokens))
				}
				if !reflect.DeepEqual(row.Thinking, want.Thinking) {
					t.Errorf("%s: thinking %+v, NeoPi %+v", want.ID, row.Thinking, want.Thinking)
				}
				if row.cost != want.Cost {
					t.Errorf("%s: cost %+v, NeoPi %+v", want.ID, row.cost, want.Cost)
				}
				var gotCompat, wantCompat any
				switch want.API {
				case "openai-completions":
					gotCompat, wantCompat = row.CompletionsCompat, &NeoPiCompletionsCompat{}
				case "anthropic-messages":
					gotCompat, wantCompat = row.AnthropicCompat, &NeoPiAnthropicCompat{}
				case "openai-responses":
					gotCompat, wantCompat = row.Compat, &NeoPiCompat{}
				}
				if err := json.Unmarshal(capture.Compats[want.CompatIndex], wantCompat); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(gotCompat, wantCompat) {
					t.Errorf("%s: compat %+v, NeoPi %+v", want.ID, gotCompat, wantCompat)
				}
			}
		})
	}
}

// The models a Copilot sign-in enables are NeoPi's: each bundled row's wire
// id, once, in bundle order, as loginGitHubCopilot derived them (the auth
// package's capture of NeoPi's own sign-in).
func TestGitHubCopilotPolicyModelsMatchNeoPi(t *testing.T) {
	data, err := os.ReadFile("../auth/githubcopilot/testdata/neopi_github_copilot_auth.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		ModelIDs []string `json:"modelIds"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.ModelIDs) == 0 {
		t.Fatal("capture names no enabled models")
	}
	if got := GitHubCopilotPolicyModelIDs(); !reflect.DeepEqual(got, capture.ModelIDs) {
		t.Errorf("policy models = %v,\nNeoPi enabled %v", got, capture.ModelIDs)
	}
}
