package neopitaxonomy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"testing"
)

// TestClassifyMatchesNeoPi replays what NeoPi's own classifyModel answered
// (testdata/export_taxonomy.ts) for every bundled (provider, id) pair and a
// set of probe ids under every provider.
func TestClassifyMatchesNeoPi(t *testing.T) {
	raw, err := os.ReadFile("testdata/classify_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var cases [][]any
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("capture holds no cases")
	}
	failures := 0
	for _, c := range cases {
		provider, id := c[0].(string), c[1].(string)
		want := Identity{Class: c[2].(string), Family: c[3].(string), Effort: c[5].(string), ThinkingVariant: c[6].(bool), LogicalID: c[7].(string)}
		if revision := c[4].(string); revision != "" {
			parsed, ok := ParseRevisionPrefix(revision)
			if !ok || parsed.String() != revision {
				t.Fatalf("%s/%q: unparseable captured revision %q", provider, id, revision)
			}
			want.Revision, want.HasRevision = parsed, true
		}
		if got := Classify(provider, id); got != want {
			failures++
			if failures <= 40 {
				t.Errorf("%s/%q: got %+v, NeoPi %+v", provider, id, got, want)
			}
		}
	}
	if failures > 0 {
		t.Errorf("%d of %d cases differ", failures, len(cases))
	}
}
