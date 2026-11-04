package util

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func TestDecodeResponseBodyPortableEncodings(t *testing.T) {
	plain := []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	for _, encoding := range []string{"gzip", "deflate", "raw-deflate", "br", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			var compressed bytes.Buffer
			var writer io.WriteCloser
			switch encoding {
			case "gzip":
				writer = gzip.NewWriter(&compressed)
			case "deflate":
				writer = zlib.NewWriter(&compressed)
			case "raw-deflate":
				var err error
				writer, err = flate.NewWriter(&compressed, flate.DefaultCompression)
				if err != nil {
					t.Fatal(err)
				}
			case "br":
				writer = brotli.NewWriter(&compressed)
			case "zstd":
				var err error
				writer, err = zstd.NewWriter(&compressed)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := writer.Write(plain); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			header := encoding
			if encoding == "raw-deflate" {
				header = "deflate"
			}
			decoded, err := DecodeResponseBody(io.NopCloser(bytes.NewReader(compressed.Bytes())), header)
			if err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if err = decoded.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, plain) {
				t.Fatalf("decoded response differs: %q", actual)
			}
		})
	}
}

func TestDecodeResponseBodyRequiresEncodingHeader(t *testing.T) {
	// Fetch does not sniff compression magic when Content-Encoding is absent.
	body := []byte{0x1f, 0x8b, 0x08, 0, 1, 2, 3}
	decoded, err := DecodeResponseBody(io.NopCloser(bytes.NewReader(body)), "")
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	actual, err := io.ReadAll(decoded)
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("unadvertised compression changed: %x %v", actual, err)
	}
}
