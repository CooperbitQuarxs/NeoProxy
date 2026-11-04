package neopool

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
)

// TestDevinUsageWindows projects NeoPi's Devin reports, built from the
// GetUserStatus responses NeoPi's usage capture was taken against, onto the
// windows routing gates on.
func TestDevinUsageWindows(t *testing.T) {
	data, err := os.ReadFile("../auth/devin/testdata/neopi_devin_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Fixtures []struct {
			Name     string `json:"name"`
			Response string `json:"response"`
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	type window struct {
		used     float64
		resetsAt int64
	}
	want := map[string]map[string]window{
		// Quota-billed: both dated windows gate; no credit buckets.
		"quota-plan": {"day": {0.45, 1_790_100_000_000}, "week": {0.20, 1_790_500_000_000}},
		// A spent bucket without a grant is used up, outranking prompt credits at 24%.
		"credit-plan": {"month": {1, 1_792_592_000_000}},
		// The hidden daily window is dropped; a millisecond reset is kept as is.
		"hidden-quota": {"week": {0, 1_790_500_000_000}},
		// Overspent flex credits cap at fully used; an undated quota-billed day still gates.
		"nested-plan-info": {"month": {1, 0}, "day": {1, 0}, "week": {1, 0}},
	}
	for _, fixture := range capture.Fixtures {
		expected, ok := want[fixture.Name]
		if !ok {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			payload, _ := base64.StdEncoding.DecodeString(fixture.Response)
			response, errDecode := devinwire.DecodeUserStatus(payload)
			if errDecode != nil {
				t.Fatal(errDecode)
			}
			windows := devinUsageWindows(devinauth.BuildUsage(response))
			if len(windows) != len(expected) {
				t.Fatalf("windows = %+v, want kinds %v", windows, expected)
			}
			for _, w := range windows {
				exp, ok := expected[w.Kind]
				if !ok || !w.Known || w.Used != exp.used || w.ResetsAt != exp.resetsAt {
					t.Errorf("%s window = %+v, want used %v resets %d", w.Kind, w, exp.used, exp.resetsAt)
				}
			}
		})
	}
}
