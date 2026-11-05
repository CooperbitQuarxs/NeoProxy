// Package cursorwire is the Go port of NeoPi's Cursor wire layer: the
// protobuf codec (pi-catalog/src/discovery/protobuf.ts) with the messages its
// generated cursor-proto.ts declares, Connect framing, and the client
// identity Cursor's RPCs carry.
//
// The codec reproduces NeoPi's encoding byte for byte: fields go out in
// declaration order (not field-number order), proto3 defaults are omitted, a
// present message field is written even when empty, a scalar oneof variant is
// written even at its default, map entries follow JavaScript property order,
// and fields the declarations do not model are re-emitted last.
package cursorwire

//go:generate go run ./gen

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf8"
)

// Wire types the codec accepts; groups are rejected as NeoPi's reader does.
const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

var errVarint32 = errors.New("Varint exceeds 32 bits")

func appendVarint(b []byte, v uint64) []byte {
	for v > 0x7f {
		b = append(b, byte(v&0x7f)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func appendTag(b []byte, number int32, wireType int) []byte {
	return appendVarint(b, uint64(uint32(number<<3|int32(wireType))))
}

// appendInt32 is Writer.int32: negative values take the 10-byte form.
func appendInt32(b []byte, v int32) []byte {
	if v >= 0 {
		return appendVarint(b, uint64(v))
	}
	return appendVarint(b, uint64(int64(v)))
}

func appendBool(b []byte, v bool) []byte {
	if v {
		return append(b, 1)
	}
	return append(b, 0)
}

func appendDouble(b []byte, v float64) []byte {
	return binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
}

func appendFloat(b []byte, v float32) []byte {
	return binary.LittleEndian.AppendUint32(b, math.Float32bits(v))
}

func appendLengthDelimited(b, v []byte) []byte {
	b = appendVarint(b, uint64(uint32(len(v))))
	return append(b, v...)
}

// reader is NeoPi's protobuf Reader.
type reader struct {
	buf []byte
	pos int
}

func (r *reader) more() bool { return r.pos < len(r.buf) }

// uint32 reads a varint of at most 32 bits; a fifth continuation byte fails.
func (r *reader) uint32() (uint32, error) {
	var result uint32
	shift := 0
	for r.pos < len(r.buf) {
		b := r.buf[r.pos]
		r.pos++
		result |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift >= 32 {
			return 0, errVarint32
		}
	}
	return 0, errors.New("Unexpected end of protobuf varint")
}

// varint64 reads a varint of at most 64 bits.
func (r *reader) varint64() (uint64, error) {
	var result uint64
	shift := 0
	for r.pos < len(r.buf) {
		b := r.buf[r.pos]
		r.pos++
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, errors.New("Varint exceeds 64 bits")
		}
	}
	return 0, errors.New("Unexpected end of protobuf 64-bit varint")
}

func (r *reader) tag() (int32, int, error) {
	tag, err := r.uint32()
	if err != nil {
		return 0, 0, err
	}
	wireType := int(tag & 7)
	if !validWireType(wireType) {
		return 0, 0, fmt.Errorf("Unsupported protobuf wire type %d at byte %d", wireType, r.pos)
	}
	return int32(tag >> 3), wireType, nil
}

func (r *reader) bool() (bool, error) {
	v, err := r.uint32()
	return v != 0, err
}

func (r *reader) int32() (int32, error) {
	v, err := r.varint64()
	return int32(v), err
}

func (r *reader) int64() (int64, error) {
	v, err := r.varint64()
	return int64(v), err
}

func (r *reader) uint64() (uint64, error) { return r.varint64() }

func (r *reader) double() (float64, error) {
	if r.pos+8 > len(r.buf) {
		return 0, errors.New("Unexpected EOF reading double")
	}
	v := math.Float64frombits(binary.LittleEndian.Uint64(r.buf[r.pos:]))
	r.pos += 8
	return v, nil
}

func (r *reader) float() (float32, error) {
	if r.pos+4 > len(r.buf) {
		return 0, errors.New("Unexpected EOF reading float")
	}
	v := math.Float32frombits(binary.LittleEndian.Uint32(r.buf[r.pos:]))
	r.pos += 4
	return v, nil
}

func (r *reader) bytes() ([]byte, error) {
	length, err := r.uint32()
	if err != nil {
		return nil, err
	}
	if r.pos+int(length) > len(r.buf) {
		return nil, errors.New("Unexpected EOF reading bytes")
	}
	out := r.buf[r.pos : r.pos+int(length) : r.pos+int(length)]
	r.pos += int(length)
	return out, nil
}

// string decodes as NeoPi's fatal TextDecoder: invalid UTF-8 fails and a
// leading byte-order mark is dropped.
func (r *reader) string() (string, error) {
	b, err := r.bytes()
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", errors.New("The encoded data was not valid for encoding utf-8")
	}
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:]
	}
	return string(b), nil
}

func (r *reader) skip(wireType int) error {
	switch wireType {
	case wireVarint:
		_, err := r.varint64()
		return err
	case wireFixed64:
		if r.pos+8 > len(r.buf) {
			return errors.New("Unexpected EOF skipping 64-bit")
		}
		r.pos += 8
	case wireBytes:
		_, err := r.bytes()
		return err
	case wireFixed32:
		if r.pos+4 > len(r.buf) {
			return errors.New("Unexpected EOF skipping 32-bit")
		}
		r.pos += 4
	}
	return nil
}

// unknown keeps a field the declarations do not model, re-tagged the way
// NeoPi's writeUnknownFields re-emits it.
func (r *reader) unknown(dst *[]byte, number int32, wireType int) error {
	start := r.pos
	if err := r.skip(wireType); err != nil {
		return err
	}
	*dst = appendTag(*dst, number, wireType)
	*dst = append(*dst, r.buf[start:r.pos]...)
	return nil
}

// packed reports whether a repeated numeric field arrived packed; anything
// else must carry the field's own wire type.
func packed(got, want int) (bool, error) {
	if got == wireBytes && want != wireBytes {
		return true, nil
	}
	return false, expectWire(got, want)
}

func expectWire(got, want int) error {
	if got != want {
		return fmt.Errorf("Unexpected protobuf wire type %d; expected %d", got, want)
	}
	return nil
}

func validWireType(wireType int) bool {
	return wireType == wireVarint || wireType == wireFixed64 || wireType == wireBytes || wireType == wireFixed32
}

// Map is a protobuf map<string, V> in NeoPi's representation, a plain
// JavaScript object: entries keep insertion order except that array-index keys
// come first in ascending numeric order, and a repeated key keeps its slot.
type Map[V any] struct {
	keys   []string
	values map[string]V
}

// Set stores v under k.
func (m *Map[V]) Set(k string, v V) {
	if m.values == nil {
		m.values = map[string]V{}
	}
	if _, ok := m.values[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.values[k] = v
}

// Get returns the value under k.
func (m *Map[V]) Get(k string) (V, bool) {
	v, ok := m.values[k]
	return v, ok
}

// Delete removes k.
func (m *Map[V]) Delete(k string) {
	if _, ok := m.values[k]; !ok {
		return
	}
	delete(m.values, k)
	m.keys = slices.DeleteFunc(m.keys, func(key string) bool { return key == k })
}

// Len is the number of entries.
func (m *Map[V]) Len() int { return len(m.keys) }

// Keys returns the keys in JavaScript property order.
func (m *Map[V]) Keys() []string { return jsPropertyOrder(m.keys) }

// jsPropertyOrder orders object keys as JavaScript enumerates them: canonical
// array indices ascending, then the remaining keys in insertion order.
func jsPropertyOrder(keys []string) []string {
	var indices []string
	for _, key := range keys {
		if isArrayIndex(key) {
			indices = append(indices, key)
		}
	}
	if len(indices) == 0 {
		return slices.Clone(keys)
	}
	slices.SortFunc(indices, func(a, b string) int {
		x, _ := strconv.ParseUint(a, 10, 32)
		y, _ := strconv.ParseUint(b, 10, 32)
		return int(x) - int(y)
	})
	out := append(make([]string, 0, len(keys)), indices...)
	for _, key := range keys {
		if !isArrayIndex(key) {
			out = append(out, key)
		}
	}
	return out
}

// isArrayIndex reports a canonical integer string below 2^32-1.
func isArrayIndex(key string) bool {
	if key == "" || len(key) > 10 || (len(key) > 1 && key[0] == '0') {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < '0' || key[i] > '9' {
			return false
		}
	}
	v, err := strconv.ParseUint(key, 10, 64)
	return err == nil && v < math.MaxUint32
}

func appendMapEntry(b []byte, number int32, entry []byte) []byte {
	b = appendTag(b, number, wireBytes)
	return appendLengthDelimited(b, entry)
}

// readMapEntry walks one map entry: field 1 is the key, field 2 the value
// handed to value, other fields are skipped.
func readMapEntry(data []byte, value func(r *reader, wireType int) error) (string, error) {
	r := reader{buf: data}
	key := ""
	for r.more() {
		tag, err := r.uint32()
		if err != nil {
			return "", err
		}
		number, wireType := int32(tag>>3), int(tag&7)
		if !validWireType(wireType) {
			return "", fmt.Errorf("Unsupported wire type %d in map entry", wireType)
		}
		switch number {
		case 1:
			if err = expectWire(wireType, wireBytes); err != nil {
				return "", err
			}
			if key, err = r.string(); err != nil {
				return "", err
			}
		case 2:
			if err = value(&r, wireType); err != nil {
				return "", err
			}
		default:
			if err = r.skip(wireType); err != nil {
				return "", err
			}
		}
	}
	return key, nil
}
