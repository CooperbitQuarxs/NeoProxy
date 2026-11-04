package codex

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func TestCodexNeoPiRefreshFailureCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_refresh_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		FailureClassification []struct {
			Message    string `json:"message"`
			Definitive bool   `json:"definitive"`
		} `json:"failureClassification"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	for _, sample := range capture.FailureClassification {
		t.Run(sample.Message, func(t *testing.T) {
			if got := util.IsDefinitiveOAuthRefreshFailure(sample.Message); got != sample.Definitive {
				t.Fatalf("definitive=%v; NeoPi source=%v", got, sample.Definitive)
			}
		})
	}
}
