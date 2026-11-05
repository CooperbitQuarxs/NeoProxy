package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
)

// NeoPi's bundled Ollama Cloud rows decode with their ollama-chat compat and
// omitted output limits, and NeoProxy classifies every id into the identity
// class NeoPi gave it (which picks the row's stream markup healing).
func TestOllamaCloudBundledRowsMatchNeoPi(t *testing.T) {
	reader, err := gzip.NewReader(bytes.NewReader(ollamaCloudCatalogFile))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var export struct {
		Models []struct {
			ID                  string `json:"id"`
			IdentityClass       string `json:"identityClass"`
			OmitMaxOutputTokens bool   `json:"omitMaxOutputTokens"`
			Compat              struct {
				StripThinkingHistory bool `json:"stripThinkingHistory"`
			} `json:"compat"`
		} `json:"models"`
	}
	if err = json.Unmarshal(data, &export); err != nil {
		t.Fatal(err)
	}
	rows, err := OllamaCloudBundledModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(export.Models) || len(rows) == 0 {
		t.Fatalf("rows = %d, export = %d", len(rows), len(export.Models))
	}
	for i, row := range rows {
		want := export.Models[i]
		if row.ID != want.ID || row.API != "ollama-chat" || row.Provider != OllamaCloudProvider {
			t.Fatalf("row %d = %s %s %s, want %s", i, row.ID, row.API, row.Provider, want.ID)
		}
		if row.OllamaCompat == nil || row.OllamaCompat.StripThinkingHistory != want.Compat.StripThinkingHistory || row.OmitMaxOutputTokens != want.OmitMaxOutputTokens {
			t.Errorf("%s compat = %+v omit = %v, want %+v omit = %v", row.ID, row.OllamaCompat, row.OmitMaxOutputTokens, want.Compat, want.OmitMaxOutputTokens)
		}
		if class := neopitaxonomy.Classify(row.Provider, row.ID).Class; class != want.IdentityClass {
			t.Errorf("%s identity class = %q, want %q", row.ID, class, want.IdentityClass)
		}
	}
}
