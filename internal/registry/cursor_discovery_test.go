package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicatalog"
)

func readGzipJSON(t *testing.T, path string, target any) {
	t.Helper()
	compressed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

// TestCursorRosterMatchesNeoPi resolves live discovery bodies
// (testdata/cursor_discovery_bodies.json.gz) the way NeoPi's model manager
// does and requires the rosters NeoPi's own resolveProviderModels produced
// from them (capture_cursor_npi_discovery.ts), per failing-RPC scenario:
// fetchCursorUsableModels' order, and every final row's fields and order.
func TestCursorRosterMatchesNeoPi(t *testing.T) {
	var bodies map[string]string
	readGzipJSON(t, "testdata/cursor_discovery_bodies.json.gz", &bodies)
	var capture struct {
		Now       int64 `json:"now"`
		Scenarios []struct {
			Name       string            `json:"name"`
			Fail       []string          `json:"fail"`
			Discovered []string          `json:"discovered"`
			Models     []json.RawMessage `json:"models"`
		} `json:"scenarios"`
	}
	readGzipJSON(t, "testdata/cursor_npi_discovery.json.gz", &capture)
	now := time.UnixMilli(capture.Now)
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			body := func(name string) []byte {
				if slices.Contains(scenario.Fail, name) {
					return nil
				}
				data, err := base64.StdEncoding.DecodeString(bodies[name])
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			responses := CursorDiscoveryResponses{Usable: body("GetUsableModels"), Available: body("AvailableModels"), Default: body("GetDefaultModelForCli")}
			discovered, ok := cursorDiscoveredSpecs(responses, "", cursorPolicy{now: capture.Now})
			if ok != (scenario.Discovered != nil) {
				t.Fatalf("discovery succeeded = %v, want %v", ok, scenario.Discovered != nil)
			}
			var ids []string
			for _, spec := range discovered {
				ids = append(ids, spec.ID)
			}
			if !slices.Equal(ids, scenario.Discovered) {
				t.Errorf("discovered order:\n got %v\nwant %v", ids, scenario.Discovered)
			}

			roster, _ := CursorRoster(responses, "", now)
			var gotIDs, wantIDs []string
			want := map[string]any{}
			for _, raw := range scenario.Models {
				var record map[string]any
				if err := json.Unmarshal(raw, &record); err != nil {
					t.Fatal(err)
				}
				id := record["id"].(string)
				wantIDs = append(wantIDs, id)
				want[id] = record
			}
			got := map[string]any{}
			for _, spec := range roster {
				gotIDs = append(gotIDs, spec.ID)
				got[spec.ID] = specRecordJSON(t, spec)
			}
			if !slices.Equal(gotIDs, wantIDs) {
				t.Errorf("roster order:\n got %v\nwant %v", gotIDs, wantIDs)
			}
			mismatches := 0
			for _, id := range wantIDs {
				if !reflect.DeepEqual(got[id], want[id]) {
					if mismatches++; mismatches <= 8 {
						gotJSON, _ := json.Marshal(got[id])
						wantJSON, _ := json.Marshal(want[id])
						t.Errorf("%s:\n got %s\nwant %s", id, gotJSON, wantJSON)
					}
				}
			}
			if mismatches > 8 {
				t.Errorf("%d mismatching rows in total", mismatches)
			}
		})
	}
}

func specRecordJSON(t *testing.T, spec *neopicatalog.Spec) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err = json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	return record
}
