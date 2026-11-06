package registry

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
)

// Every descriptor's filter, limit fallbacks, per-model transport and
// transform, runner kinds, duplicate properties and junk values map a
// models.dev payload into the rows NeoPi's own mapModelsDevToModels produced
// (testdata/capture_neopi_modelsdev_global.ts), except the
// reference-isolated providers', which no reference map keeps.
func TestNeoPiModelsDevMapAllMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_modelsdev_global.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Payload string `json:"payload"`
		Rows    []struct {
			ID            string         `json:"id"`
			Name          string         `json:"name"`
			API           string         `json:"api"`
			Provider      string         `json:"provider"`
			BaseURL       string         `json:"baseUrl"`
			Reasoning     bool           `json:"reasoning"`
			Input         []string       `json:"input"`
			ContextWindow *int           `json:"contextWindow"`
			MaxTokens     *int           `json:"maxTokens"`
			Cost          NeoPiCost      `json:"cost"`
			Kind          string         `json:"kind"`
			Compat        map[string]any `json:"compat"`
		} `json:"rows"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	neoPiReferencesOnce.Do(loadNeoPiReferences)
	if neoPiReferencesErr != nil {
		t.Fatal(neoPiReferencesErr)
	}
	want := capture.Rows[:0:0]
	for _, row := range capture.Rows {
		if !slices.Contains(neoPiReferenceIsolated, row.Provider) {
			want = append(want, row)
		}
	}
	got := neoPiModelsDevMapAll([]byte(capture.Payload))
	if len(got) != len(want) {
		ids := make([]string, len(got))
		for i, row := range got {
			ids[i] = row.Provider + "/" + row.ID
		}
		t.Fatalf("rows = %d %q, want %d", len(got), ids, len(want))
	}
	limit := func(value *int) int {
		if value == nil {
			return 0
		}
		return *value
	}
	// Compat compares as JSON, as NeoPi's numbers decode to float64.
	normalize := func(compat map[string]any) map[string]any {
		if len(compat) == 0 {
			return nil
		}
		raw, _ := json.Marshal(compat)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	for i, row := range got {
		w := want[i]
		if row.ID != w.ID || row.Provider != w.Provider || row.Name != w.Name || row.API != w.API || row.BaseURL != w.BaseURL || row.Kind != w.Kind {
			t.Errorf("row %d = %s/%s %q %s %s kind %q, want %s/%s %q %s %s kind %q", i, row.Provider, row.ID, row.Name, row.API, row.BaseURL, row.Kind,
				w.Provider, w.ID, w.Name, w.API, w.BaseURL, w.Kind)
			continue
		}
		if row.Reasoning != w.Reasoning || !reflect.DeepEqual(row.Input, w.Input) || row.ContextWindow != limit(w.ContextWindow) || row.MaxTokens != limit(w.MaxTokens) || row.cost != w.Cost {
			t.Errorf("%s/%s: reasoning %v input %v context %d max %d cost %+v, want %v %v %d %d %+v", row.Provider, row.ID, row.Reasoning, row.Input,
				row.ContextWindow, row.MaxTokens, row.cost, w.Reasoning, w.Input, limit(w.ContextWindow), limit(w.MaxTokens), w.Cost)
		}
		if got, want := normalize(row.CompatConfig), normalize(w.Compat); !reflect.DeepEqual(got, want) {
			t.Errorf("%s/%s: compat %v, want %v", row.Provider, row.ID, got, want)
		}
	}
}
