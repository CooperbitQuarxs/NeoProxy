package devinwire

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"unicode/utf16"
)

// DefaultBaseURL is Codeium/Windsurf's Cascade API host.
const DefaultBaseURL = "https://server.codeium.com"

// RPC paths NeoPi calls.
const (
	ChatPath        = "/exa.api_server_pb.ApiServerService/GetChatMessage"
	AssignModelPath = "/exa.api_server_pb.ApiServerService/AssignModel"
	UserJWTPath     = "/exa.auth_pb.AuthService/GetUserJwt"
)

// Connect frame flags.
const (
	FlagCompressed byte = 0x01
	FlagEndStream  byte = 0x02
)

// MaxFramePayload bounds one frame's declared length.
const MaxFramePayload = 16 * 1024 * 1024

// MinCompressedBodyBytes is NeoPi's MIN_COMPRESSED_BODY_BYTES: smaller chat
// requests go out plain.
const MinCompressedBodyBytes = 1024

const sessionTokenPrefix = "devin-session-token$"

// deviceOS is NeoPi's Metadata.os vocabulary for the running platform.
func deviceOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	}
	return "linux"
}

// EncodeCredential prefixes a bare JWT-shaped session token; every other key
// goes out as stored.
func EncodeCredential(apiKey string) string {
	key := strings.TrimSpace(apiKey)
	if key == "" || strings.HasPrefix(key, sessionTokenPrefix) {
		return key
	}
	if len(strings.Split(key, ".")) == 3 {
		return sessionTokenPrefix + key
	}
	return key
}

// CLIMetadata is the released Devin CLI identity NeoPi sends on GetUserJwt,
// AssignModel, GetChatMessage and GetUserStatus.
func CLIMetadata(apiKey, userJWT string) Metadata {
	return Metadata{IDEName: "devin-cli", IDEVersion: "3000.11.205", IDEType: "chisel", ExtensionName: "chisel",
		ExtensionVersion: "3000.11.205", APIKey: EncodeCredential(apiKey), Locale: "en", OS: deviceOS(), UserJWT: userJWT}
}

// DiscoveryMetadata is the dev-channel identity NeoPi sends on
// GetCliModelConfigs.
func DiscoveryMetadata(apiKey string) Metadata {
	return Metadata{IDEName: "chisel", IDEVersion: "0.0.0-dev", ExtensionName: "chisel", ExtensionVersion: "0.0.0-dev",
		APIKey: EncodeCredential(apiKey), Locale: "en", OS: deviceOS()}
}

// DeterministicUUID formats the leading 128 bits of seed's SHA-256 as an
// 8-4-4-4-12 id.
func DeterministicUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Frame wraps one Connect message.
func Frame(payload []byte, flags byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:5], uint32(len(payload)))
	copy(out[5:], payload)
	return out
}

// Gzip compresses a request body.
func Gzip(data []byte) ([]byte, error) {
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Gunzip decompresses a gzipped payload.
func Gunzip(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}

// DecodeUnary is decodeDevinUnaryMessage's body selection: the payload as is
// when it decodes, else its gunzipped form.
func DecodeUnary[T any](payload []byte, decode func([]byte) (T, error)) (T, bool) {
	if out, err := decode(payload); err == nil {
		return out, true
	}
	if raw, err := Gunzip(payload); err == nil {
		if out, errDecode := decode(raw); errDecode == nil {
			return out, true
		}
	}
	var zero T
	return zero, false
}

// FrameReader splits a Connect stream into frames.
type FrameReader struct {
	r io.Reader
}

// NewFrameReader reads frames from r.
func NewFrameReader(r io.Reader) *FrameReader { return &FrameReader{r: r} }

// Next returns the next frame, or io.EOF at a clean end of the stream.
func (f *FrameReader) Next() (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(f.r, header[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return 0, nil, fmt.Errorf("devin: truncated Connect frame header")
		}
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:5])
	if length > MaxFramePayload {
		return 0, nil, fmt.Errorf("Devin Connect frame length %d exceeds %d-byte cap", length, MaxFramePayload)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(f.r, payload); err != nil {
		return 0, nil, fmt.Errorf("devin: truncated Connect frame: %w", err)
	}
	return header[0], payload, nil
}

// TrailerError is a Connect end-of-stream error.
type TrailerError struct {
	Code, Message, Formatted, Detail, Raw string
}

const maxTrailerEvidence = 2000

// truncateEvidence cuts at 2000 UTF-16 units, as NeoPi's slice does.
func truncateEvidence(text string) string {
	units := utf16.Encode([]rune(text))
	if len(units) <= maxTrailerEvidence {
		return text
	}
	return string(utf16.Decode(units[:maxTrailerEvidence])) + "…"
}

// ParseTrailerError is readConnectTrailerError: the trailer's
// { error: { code, message, details } }, or nil.
func ParseTrailerError(text string) *TrailerError {
	if text == "" {
		return nil
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(text), &parsed) != nil {
		return nil
	}
	errValue, ok := parsed["error"].(map[string]any)
	if !ok {
		return nil
	}
	code, _ := errValue["code"].(string)
	message, _ := errValue["message"].(string)
	if code == "" && message == "" {
		return nil
	}
	out := &TrailerError{Code: code, Message: message, Raw: truncateEvidence(text)}
	out.Formatted = "Devin stream error"
	if code != "" {
		out.Formatted += " " + code
	}
	out.Formatted += ": " + message
	if details, ok := errValue["details"].([]any); ok {
		if detail := summarizeDetails(details); detail != "" {
			out.Detail = detail
			out.Formatted += " [details: " + detail + "]"
		}
	}
	return out
}

func summarizeDetails(details []any) string {
	summary := ""
	for _, entry := range details {
		record, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := record["type"].(string)
		value, _ := record["value"].(string)
		evidence := value
		if debug, present := record["debug"]; present {
			if text, isText := debug.(string); isText {
				evidence = text
			} else if encoded, err := json.Marshal(debug); err == nil {
				evidence = string(encoded)
			}
		}
		part := ""
		switch {
		case kind != "" && evidence != "":
			part = truncateEvidence(kind) + ": " + truncateEvidence(evidence)
		case kind != "":
			part = truncateEvidence(kind)
		case evidence != "":
			part = truncateEvidence(evidence)
		}
		if part == "" {
			continue
		}
		next := part
		if summary != "" {
			next = summary + "; " + part
		}
		if len(utf16.Encode([]rune(next))) > maxTrailerEvidence {
			return truncateEvidence(next)
		}
		summary = next
	}
	return summary
}
