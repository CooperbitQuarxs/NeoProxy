package cursorwire

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
)

// Wire constants from pi-catalog/src/wire/cursor.ts.
const (
	// DefaultBaseURL hosts Cursor's Connect RPCs and account API.
	DefaultBaseURL = "https://api2.cursor.sh"
	// ClientVersion is the released Cursor agent CLI build whose protocol
	// surface NeoPi mirrors; the service gates protocol features on it.
	ClientVersion = "cli-2026.09.02-c22c1a3"

	RunPath             = "/agent.v1.AgentService/Run"
	RunSSEPath          = "/agent.v1.AgentService/RunSSE"
	BidiAppendPath      = "/aiserver.v1.BidiService/BidiAppend"
	GetUsableModelsPath = "/agent.v1.AgentService/GetUsableModels"
	GetDefaultModelPath = "/agent.v1.AgentService/GetDefaultModelForCli"
	AvailableModelsPath = "/aiserver.v1.AiService/AvailableModels"
)

// Content types cursorClientHeaders accepts.
const (
	ContentTypeProto        = "application/proto"
	ContentTypeConnectProto = "application/connect+proto"
)

// SetClientHeaders sets cursorClientHeaders: the headers Cursor's
// authenticated CLI RPCs carry.
func SetClientHeaders(h http.Header, apiKey, contentType string) {
	h.Set("Content-Type", contentType)
	h.Set("Authorization", "Bearer "+apiKey)
	h.Set("X-Cursor-Client-Type", "cli")
	h.Set("X-Cursor-Client-Version", ClientVersion)
	h.Set("X-Ghost-Mode", "true")
}

// BunUserAgent is the user agent NeoPi's runtime fetch sends.
const BunUserAgent = "Bun/1.3.14"

// SetFetchHeaders adds the headers Bun's fetch sends on its own.
func SetFetchHeaders(h http.Header) {
	h.Set("Accept", "*/*")
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	h.Set("Connection", "keep-alive")
	h.Set("User-Agent", BunUserAgent)
}

// Connect envelope flags (providers/connect-frame.ts).
const (
	FlagCompressed byte = 0x01
	FlagEndStream  byte = 0x02
)

// Frame is frameConnectMessage: flag byte, big-endian length, payload.
func Frame(payload []byte, flags byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

// FrameReader splits a Connect stream into frames, uncapped as NeoPi's
// ConnectFrameDecoder is for Cursor.
type FrameReader struct{ r io.Reader }

// NewFrameReader reads frames from r.
func NewFrameReader(r io.Reader) *FrameReader { return &FrameReader{r: r} }

// Next returns the next frame, or io.EOF when the stream ends between frames;
// bytes of an unfinished frame at the end are dropped, as the decoder leaves
// them buffered.
func (f *FrameReader) Next() (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(f.r, header[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}
	payload := make([]byte, binary.BigEndian.Uint32(header[1:5]))
	if _, err := io.ReadFull(f.r, payload); err != nil {
		if err == io.ErrUnexpectedEOF {
			return 0, nil, io.EOF
		}
		return 0, nil, fmt.Errorf("cursor: read Connect frame: %w", err)
	}
	return header[0], payload, nil
}

// DeterministicUUID is deterministicUuid: the leading 128 bits of seed's
// SHA-256 in the 8-4-4-4-12 layout.
func DeterministicUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
