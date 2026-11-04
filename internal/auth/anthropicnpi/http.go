package anthropicnpi

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// Bun's fetch defaults are part of the captured OAuth/bootstrap requests. The
// inference SDK has its own explicitly pinned headers in the executor.
func fetchHeaders(request *http.Request) {
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	request.Header.Set("Connection", "keep-alive")
	request.Header.Set("User-Agent", "Bun/1.3.14")
}

func decodeJSON(response *http.Response, target any) error {
	var reader io.Reader = response.Body
	switch strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		decoded, err := gzip.NewReader(reader)
		if err != nil {
			return err
		}
		defer decoded.Close()
		reader = decoded
	case "deflate":
		buffer := bufio.NewReader(reader)
		prefix, _ := buffer.Peek(2)
		if len(prefix) == 2 && (int(prefix[0])*256+int(prefix[1]))%31 == 0 {
			decoded, err := zlib.NewReader(buffer)
			if err != nil {
				return err
			}
			defer decoded.Close()
			reader = decoded
		} else {
			decoded := flate.NewReader(buffer)
			defer decoded.Close()
			reader = decoded
		}
	case "br":
		reader = brotli.NewReader(reader)
	case "zstd":
		decoded, err := zstd.NewReader(reader)
		if err != nil {
			return err
		}
		defer decoded.Close()
		reader = decoded
	default:
		return fmt.Errorf("unsupported OAuth response encoding")
	}
	return json.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(target)
}
