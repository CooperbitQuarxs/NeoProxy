package bedrockwire

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

type eventstreamCapture struct {
	CRC32   map[string]uint32 `json:"crc32"`
	Results []struct {
		Name     string   `json:"name"`
		Single   bool     `json:"single"`
		Chunks   []string `json:"chunks"`
		Messages []struct {
			Headers map[string]string `json:"headers"`
			Payload string            `json:"payload"`
		} `json:"messages"`
		Error string `json:"error"`
	} `json:"results"`
}

// chunkReader answers each Read with the next captured chunk, as the
// response body's reader delivered it to NeoPi.
type chunkReader struct{ chunks [][]byte }

func (r *chunkReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	if r.chunks[0] = r.chunks[0][n:]; len(r.chunks[0]) == 0 {
		r.chunks = r.chunks[1:]
	}
	return n, nil
}

// TestEventStreamMatchesNeoPi replays NeoPi's own decoder over the captured
// byte streams: the messages decoded before any failure, then whether the
// stream failed.
func TestEventStreamMatchesNeoPi(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_eventstream.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var capture eventstreamCapture
	if err := json.NewDecoder(zr).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if got := crc32.ChecksumIEEE([]byte("123456789")); got != capture.CRC32["123456789"] {
		t.Fatalf("crc32 = %d, NeoPi %d", got, capture.CRC32["123456789"])
	}
	if len(capture.Results) == 0 {
		t.Fatal("no captured cases")
	}
	for _, result := range capture.Results {
		t.Run(result.Name, func(t *testing.T) {
			chunks := make([][]byte, len(result.Chunks))
			for i, chunk := range result.Chunks {
				if chunks[i], err = hex.DecodeString(chunk); err != nil {
					t.Fatal(err)
				}
			}
			var got []Message
			var gotErr error
			if result.Single {
				var message Message
				if message, gotErr = DecodeMessage(chunks[0]); gotErr == nil {
					got = append(got, message)
				}
			} else {
				decoder := NewDecoder(&chunkReader{chunks: chunks})
				for {
					message, errNext := decoder.Next()
					if errors.Is(errNext, io.EOF) {
						break
					}
					if errNext != nil {
						gotErr = errNext
						break
					}
					got = append(got, message)
				}
			}
			if len(got) != len(result.Messages) {
				t.Fatalf("decoded %d messages (err %v), NeoPi %d (error %q)", len(got), gotErr, len(result.Messages), result.Error)
			}
			for i, want := range result.Messages {
				if !reflect.DeepEqual(got[i].Headers, want.Headers) {
					t.Errorf("message %d headers = %q, NeoPi %q", i, got[i].Headers, want.Headers)
				}
				if payload := base64.StdEncoding.EncodeToString(got[i].Payload); payload != want.Payload {
					t.Errorf("message %d payload = %s, NeoPi %s", i, payload, want.Payload)
				}
			}
			if (gotErr != nil) != (result.Error != "") {
				t.Fatalf("err = %v, NeoPi error %q", gotErr, result.Error)
			}
			var frame *FrameError
			if gotErr != nil && !errors.As(gotErr, &frame) {
				t.Errorf("err = %v, want a FrameError", gotErr)
			}
			// NeoPi's own frame errors carry its message; its header reader's
			// RangeError/TypeError texts are Bun's and not reproduced.
			if want, ok := strings.CutPrefix(result.Error, "EventStreamFrameError: "); ok && gotErr.Error() != want {
				t.Errorf("err = %q, NeoPi %q", gotErr.Error(), want)
			}
		})
	}
}
