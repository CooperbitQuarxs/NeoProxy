package helps

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"fmt"
	"io"
	"strings"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

type compositeReadCloser struct {
	io.Reader
	closers []func() error
}

func (c *compositeReadCloser) Close() error {
	var firstErr error
	for i := range c.closers {
		if c.closers[i] == nil {
			continue
		}
		if err := c.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DecodeResponseBody decodes HTTP response content encodings in reverse order.
func DecodeResponseBody(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error) {
	if body == nil {
		return nil, fmt.Errorf("response body is nil")
	}
	if contentEncoding == "" {
		return body, nil
	}
	encodings := strings.Split(contentEncoding, ",")
	reader := io.Reader(body)
	decoderClosers := make([]func() error, 0, len(encodings))
	cleanup := func() {
		for i := len(decoderClosers) - 1; i >= 0; i-- {
			_ = decoderClosers[i]()
		}
		_ = body.Close()
	}
	for index := len(encodings) - 1; index >= 0; index-- {
		encoding := strings.TrimSpace(strings.ToLower(encodings[index]))
		switch encoding {
		case "", "identity":
			continue
		case "gzip":
			gzipReader, errGzip := gzip.NewReader(reader)
			if errGzip != nil {
				cleanup()
				return nil, fmt.Errorf("failed to create gzip reader: %w", errGzip)
			}
			reader = gzipReader
			decoderClosers = append(decoderClosers, gzipReader.Close)
		case "deflate":
			deflateReader, errDeflate := newDeflateReader(reader)
			if errDeflate != nil {
				cleanup()
				return nil, errDeflate
			}
			reader = deflateReader
			decoderClosers = append(decoderClosers, deflateReader.Close)
		case "br":
			reader = brotli.NewReader(reader)
		case "zstd":
			decoder, errZstd := zstd.NewReader(reader)
			if errZstd != nil {
				cleanup()
				return nil, fmt.Errorf("failed to create zstd reader: %w", errZstd)
			}
			reader = decoder
			decoderClosers = append(decoderClosers, func() error {
				decoder.Close()
				return nil
			})
		default:
			cleanup()
			return nil, fmt.Errorf("unsupported content encoding %q", encoding)
		}
	}
	if len(decoderClosers) == 0 && reader == body {
		return body, nil
	}
	closers := make([]func() error, 0, len(decoderClosers)+1)
	for index := len(decoderClosers) - 1; index >= 0; index-- {
		closers = append(closers, decoderClosers[index])
	}
	closers = append(closers, body.Close)
	return &compositeReadCloser{Reader: reader, closers: closers}, nil
}

func newDeflateReader(reader io.Reader) (io.ReadCloser, error) {
	buffered := bufio.NewReader(reader)
	header, errPeek := buffered.Peek(2)
	if errPeek == nil && isZlibHeader(header) {
		zlibReader, errZlib := zlib.NewReader(buffered)
		if errZlib != nil {
			return nil, fmt.Errorf("failed to create zlib deflate reader: %w", errZlib)
		}
		return zlibReader, nil
	}
	return flate.NewReader(buffered), nil
}

func isZlibHeader(header []byte) bool {
	if len(header) < 2 {
		return false
	}
	cmf, flg := header[0], header[1]
	return cmf&0x0f == 8 && cmf>>4 <= 7 && (uint16(cmf)<<8|uint16(flg))%31 == 0
}
