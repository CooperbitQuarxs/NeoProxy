package registry

import (
	"encoding/json"
	"reflect"
	"testing"
)

// factoryDroidDiscoveryCapture is testdata/factory_droid_discovery.json.gz:
// NeoPi's OWN fetchFactoryDroidModels over synthetic flag and settings
// payloads, and factoryDroidSeedModels per account scope.
type factoryDroidDiscoveryCapture struct {
	Scenarios []struct {
		Name     string             `json:"name"`
		Scope    factoryDroidScope  `json:"scope"`
		Flags    factoryDroidReply  `json:"flags"`
		Settings factoryDroidReply  `json:"settings"`
		Models   []factoryDroidSpec `json:"models"`
	} `json:"scenarios"`
	Seeds []struct {
		Scope  factoryDroidScope  `json:"scope"`
		Models []factoryDroidSpec `json:"models"`
	} `json:"seeds"`
}

type factoryDroidScope struct {
	Region          string `json:"region"`
	InferenceRegion string `json:"inferenceRegion"`
	OrgID           string `json:"orgId"`
}

type factoryDroidReply struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

// factoryDroidSpec is the subset of a ModelSpec<"factory-droid-agent"> the
// port carries.
type factoryDroidSpec struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	BaseURL       string         `json:"baseUrl"`
	Reasoning     bool           `json:"reasoning"`
	Input         []string       `json:"input"`
	Cost          NeoPiCost      `json:"cost"`
	ContextWindow int            `json:"contextWindow"`
	MaxTokens     int            `json:"maxTokens"`
	Thinking      *NeoPiThinking `json:"thinking"`
	OrgID         string         `json:"factoryDroidOrgId"`
	Credits       *float64       `json:"factoryDroidCredits"`
	APIProviders  []string       `json:"factoryDroidApiProviders"`
	RoutingSource string         `json:"factoryDroidRoutingSource"`
}

func factoryDroidSpecOf(row *NeoPiModel) factoryDroidSpec {
	return factoryDroidSpec{ID: row.ID, Name: row.Name, BaseURL: row.BaseURL, Reasoning: row.Reasoning, Input: row.Input, Cost: row.cost,
		ContextWindow: row.ContextWindow, MaxTokens: row.MaxTokens, Thinking: row.Thinking, OrgID: row.FactoryDroid.OrgID,
		Credits: row.FactoryDroid.Credits, APIProviders: row.FactoryDroid.APIProviders, RoutingSource: row.FactoryDroid.RoutingSource}
}

func compareFactoryDroidSpecs(t *testing.T, got []*NeoPiModel, want []factoryDroidSpec) {
	t.Helper()
	if len(got) != len(want) {
		ids := make([]string, len(got))
		for i, row := range got {
			ids[i] = row.ID
		}
		t.Fatalf("%d models %v, NeoPi returned %d", len(got), ids, len(want))
	}
	for i := range want {
		spec := factoryDroidSpecOf(got[i])
		if !reflect.DeepEqual(spec, want[i]) {
			gotJSON, _ := json.Marshal(spec)
			wantJSON, _ := json.Marshal(want[i])
			t.Errorf("model %d:\n got %s\nwant %s", i, gotJSON, wantJSON)
		}
	}
}

func TestFactoryDroidDiscoveryParity(t *testing.T) {
	var capture factoryDroidDiscoveryCapture
	readGzipJSON(t, "testdata/factory_droid_discovery.json.gz", &capture)
	if len(capture.Scenarios) == 0 || len(capture.Seeds) == 0 {
		t.Fatal("empty capture")
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			ok2xx := func(status int) bool { return status >= 200 && status < 300 }
			var settingsBody []byte
			if ok2xx(scenario.Settings.Status) {
				settingsBody = []byte(scenario.Settings.Body)
			}
			settings, answered := FactoryDroidSettings{}, false
			if ok2xx(scenario.Flags.Status) {
				settings, answered = ParseFactoryDroidSettings([]byte(scenario.Flags.Body), settingsBody)
			}
			if scenario.Models == nil {
				if answered {
					t.Fatal("discovery answered; NeoPi returned null")
				}
				return
			}
			if !answered {
				t.Fatal("discovery unanswered; NeoPi returned models")
			}
			scope := FactoryDroidScope{Region: scenario.Scope.Region, InferenceRegion: scenario.Scope.InferenceRegion, OrgID: scenario.Scope.OrgID}
			compareFactoryDroidSpecs(t, FactoryDroidDiscoveredModels(scope, settings), scenario.Models)
		})
	}
	for _, seed := range capture.Seeds {
		t.Run("seed/"+seed.Scope.Region+"/"+seed.Scope.InferenceRegion, func(t *testing.T) {
			compareFactoryDroidSpecs(t, FactoryDroidSeedModels(FactoryDroidScope{Region: seed.Scope.Region, InferenceRegion: seed.Scope.InferenceRegion}), seed.Models)
		})
	}
}
