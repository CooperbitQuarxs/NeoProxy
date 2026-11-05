package neopicompat

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"sort"
	"testing"
)

type policyCapture struct {
	Compats []json.RawMessage `json:"compats"`
	Cases   []struct {
		Label string `json:"label"`
		Spec  struct {
			ID        string         `json:"id"`
			API       string         `json:"api"`
			Provider  string         `json:"provider"`
			BaseURL   string         `json:"baseUrl"`
			Reasoning bool           `json:"reasoning"`
			Thinking  map[string]any `json:"thinking"`
			Compat    map[string]any `json:"compat"`
		} `json:"spec"`
		CompatIndex int            `json:"compatIndex"`
		Thinking    map[string]any `json:"thinking"`
		Reasoning   bool           `json:"reasoning"`
		Error       string         `json:"error"`
	} `json:"cases"`
}

// normalize drops nil-valued keys (NeoPi's undefined, which JSON omits) so a
// resolved record compares against its captured JSON.
func normalize(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			if item == nil {
				continue
			}
			out[key] = normalize(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = normalize(v[i])
		}
		return out
	case int:
		return float64(v)
	}
	return value
}

// roundTrip passes a resolved value through JSON, as the capture did.
func roundTrip(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(normalize(value))
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every completions spec derived from NeoPi's bundled catalog resolves to the
// compat, thinking and reasoning NeoPi's own resolveModelPolicy produced:
// rows as authored, rows reported as non-reasoning, authored ladders, and ids
// the bundle lacks.
func TestResolveMatchesNeoPi(t *testing.T) {
	raw, err := os.ReadFile("testdata/policy_capture.json.gz")
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
	var capture policyCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	compats := make([]any, len(capture.Compats))
	for i, compat := range capture.Compats {
		if err = json.Unmarshal(compat, &compats[i]); err != nil {
			t.Fatal(err)
		}
	}
	mismatched := map[string]int{}
	failures := 0
	for _, c := range capture.Cases {
		spec := Spec{Provider: c.Spec.Provider, API: c.Spec.API, ID: c.Spec.ID, BaseURL: c.Spec.BaseURL,
			Reasoning: c.Spec.Reasoning, Thinking: c.Spec.Thinking, Compat: c.Spec.Compat}
		policy, err := Resolve(spec)
		if c.Error != "" {
			if err == nil {
				t.Errorf("%s %s/%s: resolved, NeoPi threw %s", c.Label, spec.Provider, spec.ID, c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s %s/%s: %v", c.Label, spec.Provider, spec.ID, err)
			continue
		}
		got, want := roundTrip(t, policy.Compat).(map[string]any), compats[c.CompatIndex].(map[string]any)
		keys := map[string]bool{}
		for key := range got {
			keys[key] = true
		}
		for key := range want {
			keys[key] = true
		}
		bad := false
		for key := range keys {
			if !reflect.DeepEqual(got[key], want[key]) {
				mismatched["compat."+key]++
				bad = true
				if mismatched["compat."+key] <= 2 {
					t.Logf("%s %s/%s compat.%s = %v, NeoPi %v", c.Label, spec.Provider, spec.ID, key, got[key], want[key])
				}
			}
		}
		var gotThinking any
		if policy.Thinking != nil {
			gotThinking = roundTrip(t, policy.Thinking)
		}
		var wantThinking any
		if c.Thinking != nil {
			wantThinking = roundTrip(t, c.Thinking)
		}
		if !reflect.DeepEqual(gotThinking, wantThinking) {
			mismatched["thinking"]++
			bad = true
			if mismatched["thinking"] <= 3 {
				t.Logf("%s %s/%s thinking = %v, NeoPi %v", c.Label, spec.Provider, spec.ID, gotThinking, wantThinking)
			}
		}
		if policy.Reasoning != c.Reasoning {
			mismatched["reasoning"]++
			bad = true
		}
		if bad {
			failures++
		}
	}
	if failures > 0 {
		keys := make([]string, 0, len(mismatched))
		for key := range mismatched {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			t.Logf("%s: %d", key, mismatched[key])
		}
		t.Fatalf("%d of %d cases differ from NeoPi", failures, len(capture.Cases))
	}
	t.Logf("%d cases match NeoPi", len(capture.Cases))
}
