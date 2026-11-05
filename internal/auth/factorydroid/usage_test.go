package factorydroid

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

// ParseUsage reads billing limits the way NeoPi's parseFactoryDroidUsage
// does: pools, windows, lapsed windows, unreadable percents, the extra
// balance and the no-token-billing note (testdata/capture_neopi_factory_usage.ts).
func TestParseUsageMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_factory_usage.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		FetchedAt int64 `json:"fetchedAt"`
		Cases     []struct {
			Name    string          `json:"name"`
			Payload json.RawMessage `json:"payload"`
			Report  *Usage          `json:"report"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got, ok := ParseUsage(tc.Payload, time.UnixMilli(capture.FetchedAt))
			if tc.Report == nil {
				if ok {
					t.Fatalf("parsed %+v; NeoPi returned null", got)
				}
				return
			}
			if !ok {
				t.Fatal("no usage; NeoPi returned a report")
			}
			if !reflect.DeepEqual(got, tc.Report) {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(tc.Report)
				t.Errorf("usage:\n got %s\nwant %s", gotJSON, wantJSON)
			}
		})
	}
}
