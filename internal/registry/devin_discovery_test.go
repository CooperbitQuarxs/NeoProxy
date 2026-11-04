package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
)

// TestDevinDiscoveryMatchesNeoPi normalizes a real GetCliModelConfigs
// response (every config enabled) and requires the NeoPi records NeoPi's own
// fetchDevinModels + buildModel produced from it (capture_devin_npi_discovery.ts).
func TestDevinDiscoveryMatchesNeoPi(t *testing.T) {
	compressed, err := os.ReadFile("testdata/devin_cli_model_configs.bin.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	response, err := devinwire.DecodeCliModelConfigs(payload)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/devin_npi_discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		BaseURL string            `json:"baseUrl"`
		Models  []json.RawMessage `json:"models"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{}
	for _, raw := range capture.Models {
		var record map[string]any
		if err = json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		want[record["id"].(string)] = record
	}
	got := map[string]any{}
	for _, info := range DevinDiscoveredModels(response, capture.BaseURL) {
		encoded, errMarshal := json.Marshal(info.NeoPi)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		var record map[string]any
		if err = json.Unmarshal(encoded, &record); err != nil {
			t.Fatal(err)
		}
		got[info.ID] = record
	}
	if len(got) != len(want) {
		t.Errorf("discovered %d models, want %d", len(got), len(want))
	}
	mismatches := 0
	for id, record := range want {
		if !reflect.DeepEqual(got[id], record) {
			if mismatches++; mismatches <= 10 {
				gotJSON, _ := json.Marshal(got[id])
				wantJSON, _ := json.Marshal(record)
				t.Errorf("%s:\n got %s\nwant %s", id, gotJSON, wantJSON)
			}
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			if mismatches++; mismatches <= 10 {
				t.Errorf("unexpected model %s", id)
			}
		}
	}
	if mismatches > 10 {
		t.Errorf("%d mismatches in total", mismatches)
	}
}
