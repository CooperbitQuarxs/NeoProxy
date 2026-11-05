package cursorwire

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// TestCodecMatchesNeoPi replays testdata/codec_capture.json.gz, NeoPi's own
// protobuf codec run over every declared message (capture_cursor_codec.ts):
// a fully populated encoding must decode and re-encode to itself, and a
// reordered encoding with unknown fields spliced in must re-encode exactly as
// NeoPi's decode + encode did.
func TestCodecMatchesNeoPi(t *testing.T) {
	compressed, err := os.ReadFile("testdata/codec_capture.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Cases []struct {
			Type      string `json:"type"`
			Choice    int    `json:"choice"`
			Encoded   string `json:"encoded"`
			Raw       string `json:"raw"`
			Reencoded string `json:"reencoded"`
		} `json:"cases"`
	}
	if err = json.NewDecoder(zr).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Cases) == 0 {
		t.Fatal("empty capture")
	}
	covered := map[string]bool{}
	failures := 0
	for _, c := range capture.Cases {
		newMessage, ok := generatedMessages[c.Type]
		if !ok {
			t.Fatalf("no generated message %s", c.Type)
		}
		covered[c.Type] = true
		for _, pair := range [][2]string{{c.Encoded, c.Encoded}, {c.Raw, c.Reencoded}} {
			in, _ := base64.StdEncoding.DecodeString(pair[0])
			want, _ := base64.StdEncoding.DecodeString(pair[1])
			message := newMessage()
			if err = message.Unmarshal(in); err != nil {
				failures++
				t.Errorf("%s#%d: unmarshal: %v", c.Type, c.Choice, err)
				continue
			}
			if got := message.Marshal(); !bytes.Equal(got, want) {
				if failures++; failures <= 10 {
					t.Errorf("%s#%d:\n got %x\nwant %x", c.Type, c.Choice, got, want)
				}
			}
		}
	}
	if len(covered) != len(generatedMessages) {
		t.Errorf("capture covers %d of %d messages", len(covered), len(generatedMessages))
	}
	if failures > 10 {
		t.Errorf("%d failures in total", failures)
	}
}
