// Package bedrockwire ports NeoPi's Amazon Bedrock Converse wire
// (packages/ai/src/providers/amazon-bedrock.ts and aws-eventstream.ts): the
// application/vnd.amazon.eventstream decoder, the ConverseStream request body
// and the stream mapping onto cursorwire output.
package bedrockwire

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strconv"
	"unicode/utf8"
)

const (
	preludeLen       = 8
	preludeCRCLen    = 4
	messageCRCLen    = 4
	headerBlockStart = preludeLen + preludeCRCLen
	minMessageLen    = headerBlockStart + messageCRCLen
)

// FrameError is NeoPi's EventStreamFrameError, and also stands for the
// RangeError/TypeError its header reader throws past the end of a header.
type FrameError struct{ Detail string }

func (e *FrameError) Error() string { return "eventstream: " + e.Detail }

// Message is one decoded eventstream message. Header values are strings, as
// NeoPi surfaces every typed value.
type Message struct {
	Headers map[string]string
	Payload []byte
}

func frameError(format string, args ...any) error {
	return &FrameError{Detail: fmt.Sprintf(format, args...)}
}

// DecodeMessage is decodeMessage: one fully buffered message, rejected when
// the framing is malformed or either CRC32 mismatches.
func DecodeMessage(frame []byte) (Message, error) {
	if len(frame) < minMessageLen {
		return Message{}, frameError("frame too short")
	}
	total := binary.BigEndian.Uint32(frame[0:4])
	if uint64(total) != uint64(len(frame)) {
		return Message{}, frameError("framed length %d != buffer %d", total, len(frame))
	}
	headersLen := uint64(binary.BigEndian.Uint32(frame[4:8]))
	if crc32.ChecksumIEEE(frame[:preludeLen]) != binary.BigEndian.Uint32(frame[8:12]) {
		return Message{}, frameError("prelude CRC mismatch")
	}
	end := len(frame) - messageCRCLen
	if crc32.ChecksumIEEE(frame[:end]) != binary.BigEndian.Uint32(frame[end:]) {
		return Message{}, frameError("message CRC mismatch")
	}
	// Uint8Array.subarray clamps: an oversized header length takes the rest of
	// the frame as headers and leaves an empty payload.
	headersEnd := uint64(len(frame))
	if headerBlockStart+headersLen < headersEnd {
		headersEnd = headerBlockStart + headersLen
	}
	payload := []byte{}
	if int(headersEnd) < end {
		payload = frame[headersEnd:end]
	}
	headers, err := parseHeaders(frame, headerBlockStart, int(headersEnd))
	if err != nil {
		return Message{}, err
	}
	return Message{Headers: headers, Payload: payload}, nil
}

// parseHeaders is parseHeaders over frame[start:end]. Fixed-width reads past
// the end throw as DataView does; string, long and timestamp reads clamp as
// subarray does. A byte array is read from the frame's own buffer, as
// Buffer.from(buf.buffer, …) does for a frame decoded on its own.
func parseHeaders(frame []byte, start, end int) (map[string]string, error) {
	out := map[string]string{}
	buf := frame[start:end]
	sub := func(p, n int) []byte {
		if p >= len(buf) {
			return nil
		}
		return buf[p:min(p+n, len(buf))]
	}
	need := func(p, n int) error {
		if p+n > len(buf) {
			return frameError("header read out of range at %d", p)
		}
		return nil
	}
	set := func(name, value string) {
		// Assigning a string to a plain object's __proto__ is a no-op.
		if name != "__proto__" {
			out[name] = value
		}
	}
	p := 0
	for p < len(buf) {
		nameLen := int(buf[p])
		p++
		name := decodeUTF8(sub(p, nameLen))
		p += nameLen
		if err := need(p, 1); err != nil {
			return nil, err
		}
		kind := buf[p]
		p++
		switch kind {
		case 0:
			set(name, "true")
		case 1:
			set(name, "false")
		case 2:
			if err := need(p, 1); err != nil {
				return nil, err
			}
			set(name, strconv.Itoa(int(int8(buf[p]))))
			p++
		case 3:
			if err := need(p, 2); err != nil {
				return nil, err
			}
			set(name, strconv.Itoa(int(int16(binary.BigEndian.Uint16(buf[p:])))))
			p += 2
		case 4:
			if err := need(p, 4); err != nil {
				return nil, err
			}
			set(name, strconv.Itoa(int(int32(binary.BigEndian.Uint32(buf[p:])))))
			p += 4
		case 5:
			set(name, bigIntString(sub(p, 8)))
			p += 8
		case 6:
			if err := need(p, 2); err != nil {
				return nil, err
			}
			n := int(binary.BigEndian.Uint16(buf[p:]))
			p += 2
			if start+p+n > len(frame) {
				return nil, frameError("byte array header out of range at %d", p)
			}
			set(name, base64.StdEncoding.EncodeToString(frame[start+p:start+p+n]))
			p += n
		case 7:
			if err := need(p, 2); err != nil {
				return nil, err
			}
			n := int(binary.BigEndian.Uint16(buf[p:]))
			p += 2
			set(name, decodeUTF8(sub(p, n)))
			p += n
		case 8:
			ms, signed := bigInt(sub(p, 8))
			stamp, ok := isoTimestamp(ms, signed)
			if !ok {
				return nil, frameError("invalid timestamp header")
			}
			set(name, stamp)
			p += 8
		case 9:
			if err := need(p, 16); err != nil {
				return nil, err
			}
			const hex = "0123456789abcdef"
			var b []byte
			for i, c := range buf[p : p+16] {
				if i == 4 || i == 6 || i == 8 || i == 10 {
					b = append(b, '-')
				}
				b = append(b, hex[c>>4], hex[c&15])
			}
			set(name, string(b))
			p += 16
		default:
			return nil, frameError("unknown header value type %d", kind)
		}
	}
	return out, nil
}

// bigInt is bigIntFromBytes: big-endian, sign-extended only for a full eight
// bytes. A short (clamped) read stays unsigned; signed reports which.
func bigInt(b []byte) (value uint64, signed bool) {
	for _, c := range b {
		value = value<<8 | uint64(c)
	}
	return value, len(b) == 8
}

func bigIntString(b []byte) string {
	value, signed := bigInt(b)
	if signed {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatUint(value, 10)
}

// isoTimestamp is new Date(Number(ms)).toISOString(): false beyond the Date
// range (±8.64e15 ms), years outside 0–9999 in the expanded ±YYYYYY form.
func isoTimestamp(raw uint64, signed bool) (string, bool) {
	const limit = 8_640_000_000_000_000
	var ms int64
	if signed {
		ms = int64(raw)
	} else if raw > limit {
		return "", false
	} else {
		ms = int64(raw)
	}
	if ms > limit || ms < -limit {
		return "", false
	}
	days, rem := ms/86_400_000, ms%86_400_000
	if rem < 0 {
		days--
		rem += 86_400_000
	}
	year, month, day := civilFromDays(days)
	var yearText string
	switch {
	case year >= 0 && year <= 9999:
		yearText = fmt.Sprintf("%04d", year)
	case year < 0:
		yearText = fmt.Sprintf("-%06d", -year)
	default:
		yearText = fmt.Sprintf("+%06d", year)
	}
	return fmt.Sprintf("%s-%02d-%02dT%02d:%02d:%02d.%03dZ", yearText, month, day,
		rem/3_600_000, rem/60_000%60, rem/1000%60, rem%1000), true
}

// civilFromDays converts days since 1970-01-01 to a proleptic Gregorian date.
func civilFromDays(z int64) (year, month, day int64) {
	z += 719468
	era := z / 146097
	if z < 0 && z%146097 != 0 {
		era--
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	day = doy - (153*mp+2)/5 + 1
	month = mp + 3
	if month > 12 {
		month -= 12
	}
	year = yoe + era*400
	if month <= 2 {
		year++
	}
	return year, month, day
}

// decodeUTF8 is a default TextDecoder: a leading byte-order mark is dropped
// and each maximal ill-formed subpart becomes one U+FFFD.
func decodeUTF8(b []byte) string {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	if utf8.Valid(b) {
		return string(b)
	}
	out := make([]byte, 0, len(b)+8)
	for i := 0; i < len(b); {
		if r, size := utf8.DecodeRune(b[i:]); r != utf8.RuneError || size > 1 {
			out = append(out, b[i:i+size]...)
			i += size
			continue
		}
		i += maximalSubpart(b[i:])
		out = utf8.AppendRune(out, utf8.RuneError)
	}
	return string(out)
}

// maximalSubpart is the length of the ill-formed prefix WHATWG's decoder
// replaces with one U+FFFD: a lead byte plus the continuation bytes that
// were still valid for it.
func maximalSubpart(b []byte) int {
	var need int
	lower, upper := byte(0x80), byte(0xBF)
	switch c := b[0]; {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c >= 0xE0 && c <= 0xEF:
		need = 2
		if c == 0xE0 {
			lower = 0xA0
		} else if c == 0xED {
			upper = 0x9F
		}
	case c >= 0xF0 && c <= 0xF4:
		need = 3
		if c == 0xF0 {
			lower = 0x90
		} else if c == 0xF4 {
			upper = 0x8F
		}
	default:
		return 1
	}
	n := 1
	for ; n <= need && n < len(b); n++ {
		if b[n] < lower || b[n] > upper {
			break
		}
		lower, upper = 0x80, 0xBF
	}
	return n
}

// Decoder is decodeEventStream over a response body: messages may span reads
// and one read may carry many. Next returns io.EOF after a clean end and a
// FrameError for leftover bytes.
type Decoder struct {
	r    io.Reader
	buf  []byte
	off  int
	done bool
	read []byte
}

// NewDecoder reads eventstream messages from r.
func NewDecoder(r io.Reader) *Decoder { return &Decoder{r: r, read: make([]byte, 32*1024)} }

// Next returns the next message.
func (d *Decoder) Next() (Message, error) {
	for {
		if rest := d.buf[d.off:]; len(rest) >= 4 {
			total := binary.BigEndian.Uint32(rest)
			if total < minMessageLen {
				return Message{}, frameError("total length %d below minimum", total)
			}
			if uint64(len(rest)) >= uint64(total) {
				// The buffer is compacted by later reads; the message keeps its own bytes.
				frame := append([]byte(nil), rest[:total]...)
				d.off += int(total)
				return DecodeMessage(frame)
			}
		}
		if d.done {
			if d.off < len(d.buf) {
				return Message{}, frameError("truncated message at end of stream")
			}
			return Message{}, io.EOF
		}
		if d.off > 0 {
			d.buf = append(d.buf[:0], d.buf[d.off:]...)
			d.off = 0
		}
		n, err := d.r.Read(d.read)
		d.buf = append(d.buf, d.read[:n]...)
		if errors.Is(err, io.EOF) {
			d.done = true
		} else if err != nil {
			return Message{}, err
		}
	}
}
