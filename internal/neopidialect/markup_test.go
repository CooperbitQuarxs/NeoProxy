package neopidialect

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type markupCase struct {
	Name    string        `json:"name"`
	Pattern string        `json:"pattern"`
	Chunks  []string      `json:"chunks"`
	Feeds   [][]HealEvent `json:"feeds"`
	Flush   []HealEvent   `json:"flush"`
	Closed  bool          `json:"closed"`
}

// withoutIDs drops the healed tool-call ids, which NeoPi mints at random.
func withoutIDs(events []HealEvent) []HealEvent {
	out := make([]HealEvent, len(events))
	for i, event := range events {
		if event.Kind == KindToolCall && !strings.HasPrefix(event.ID, "call_") {
			event.Name = "<bad id " + event.ID + ">"
		}
		event.ID = ""
		out[i] = event
	}
	return out
}

// TestMarkupHealingMatchesNeoPiCapture replays NeoPi's own StreamMarkupHealing
// for the kimi, dsml and qwen tool-call patterns (testdata/
// capture_markup_healing.ts) chunk by chunk.
func TestMarkupHealingMatchesNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/markup_healing.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		CaseCount int          `json:"caseCount"`
		Cases     []markupCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.CaseCount != len(capture.Cases) || len(capture.Cases) == 0 {
		t.Fatalf("capture caseCount=%d, cases=%d", capture.CaseCount, len(capture.Cases))
	}
	for _, tc := range capture.Cases {
		t.Run(tc.Pattern+"/"+tc.Name, func(t *testing.T) {
			healer := NewHealer(tc.Pattern)
			for i, chunk := range tc.Chunks {
				requireEventsEqual(t, tc.Name, i, tc.Feeds[i], withoutIDs(healer.Feed(chunk)))
			}
			requireEventsEqual(t, tc.Name, len(tc.Chunks), tc.Flush, withoutIDs(healer.Flush()))
			if healer.SectionClosed() != tc.Closed {
				t.Fatalf("%s: sectionClosed %v, NeoPi %v", tc.Name, healer.SectionClosed(), tc.Closed)
			}
		})
	}
}
