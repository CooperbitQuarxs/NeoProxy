package registry

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type gitlabDuoAgentCapturedRow struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	API                 string    `json:"api"`
	Provider            string    `json:"provider"`
	BaseURL             string    `json:"baseUrl"`
	Reasoning           bool      `json:"reasoning"`
	Input               []string  `json:"input"`
	ContextWindow       int       `json:"contextWindow"`
	MaxTokens           *int      `json:"maxTokens"`
	OmitMaxOutputTokens bool      `json:"omitMaxOutputTokens"`
	Cost                NeoPiCost `json:"cost"`
}

// TestGitLabDuoAgentRowsMatchNeoPi pins the fallback seed and discovered
// rows to NeoPi's own buildModel output
// (testdata/capture_gitlab_duo_agent_rows.ts).
func TestGitLabDuoAgentRowsMatchNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/gitlab_duo_agent_rows.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Fallback   gitlabDuoAgentCapturedRow `json:"fallback"`
		Discovered []struct {
			Ref struct {
				Name string `json:"name"`
				Ref  string `json:"ref"`
			} `json:"ref"`
			Row gitlabDuoAgentCapturedRow `json:"row"`
		} `json:"discovered"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	check := func(label string, got *NeoPiModel, want gitlabDuoAgentCapturedRow) {
		t.Helper()
		if want.MaxTokens != nil {
			t.Fatalf("%s: NeoPi row has maxTokens %d; the port only models an absent limit", label, *want.MaxTokens)
		}
		row := gitlabDuoAgentCapturedRow{ID: got.ID, Name: got.Name, API: got.API, Provider: got.Provider, BaseURL: got.BaseURL,
			Reasoning: got.Reasoning, Input: got.Input, ContextWindow: got.ContextWindow, OmitMaxOutputTokens: got.OmitMaxOutputTokens, Cost: got.cost}
		if got.MaxTokens != 0 {
			row.MaxTokens = &got.MaxTokens
		}
		if !reflect.DeepEqual(row, want) {
			t.Fatalf("%s:\n got %+v\nwant %+v", label, row, want)
		}
	}
	seed := GitLabDuoAgentSeedModels()
	if len(seed) != 1 {
		t.Fatalf("seed rows = %d, NeoPi has one", len(seed))
	}
	check("fallback", seed[0].NeoPi, capture.Fallback)
	for _, entry := range capture.Discovered {
		check(entry.Ref.Ref, GitLabDuoAgentRow(entry.Ref.Ref, entry.Ref.Name, "https://gitlab.com/", entry.Row.ContextWindow), entry.Row)
	}
}
