// Package jsjson serializes JSON byte-for-byte like JavaScript's
// JSON.stringify, for wire formats ported from NeoPi.
package jsjson

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// Field is one property of an insertion-ordered object. An empty Value omits
// the property, mirroring JSON.stringify skipping undefined.
type Field struct {
	Name  string
	Value json.RawMessage
}

// Marshal encodes value like JSON.stringify: no HTML escaping, and U+2028/U+2029
// emitted literally. Go maps encode with sorted keys; use Object for insertion order.
func Marshal(value any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
	data := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	if !bytes.Contains(data, []byte(`\u202`)) {
		return data
	}
	// Preserve escaped backslashes so a literal "\\u2028" stays literal.
	out := make([]byte, 0, len(data))
	for index := 0; index < len(data); index++ {
		if data[index] == '\\' && index+1 < len(data) {
			if data[index+1] == '\\' {
				out = append(out, '\\', '\\')
				index++
				continue
			}
			if index+5 < len(data) && bytes.Equal(data[index+1:index+5], []byte("u202")) && (data[index+5] == '8' || data[index+5] == '9') {
				out = append(out, 0xe2, 0x80, 0xa8+data[index+5]-'8')
				index += 5
				continue
			}
		}
		out = append(out, data[index])
	}
	return out
}

// Object encodes fields in the given order.
func Object(fields ...Field) json.RawMessage {
	out := []byte{'{'}
	for _, field := range fields {
		if len(field.Value) == 0 {
			continue
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, Marshal(field.Name)...)
		out = append(out, ':')
		out = append(out, field.Value...)
	}
	return append(out, '}')
}

// Normalize re-serializes raw JSON as JSON.stringify(JSON.parse(raw)) would:
// key order is kept, insignificant whitespace dropped, and strings and numbers
// take JavaScript's canonical spelling. Duplicate keys keep the last value in
// the first key's position, as JSON.parse does.
func Normalize(raw []byte) json.RawMessage {
	return normalizeValue(gjson.ParseBytes(raw))
}

func normalizeValue(value gjson.Result) json.RawMessage {
	switch {
	case value.IsObject():
		var fields []Field
		value.ForEach(func(key, child gjson.Result) bool {
			name := key.String()
			for i := range fields {
				if fields[i].Name == name {
					fields[i].Value = normalizeValue(child)
					return true
				}
			}
			fields = append(fields, Field{Name: name, Value: normalizeValue(child)})
			return true
		})
		return Object(fields...)
	case value.IsArray():
		out := []byte{'['}
		for i, child := range value.Array() {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, normalizeValue(child)...)
		}
		return append(out, ']')
	case value.Type == gjson.String:
		return Marshal(value.String())
	case value.Type == gjson.Number:
		return json.RawMessage(Number(value.Float()))
	case value.Type == gjson.True:
		return json.RawMessage("true")
	case value.Type == gjson.False:
		return json.RawMessage("false")
	default:
		return json.RawMessage("null")
	}
}

// Number spells a float64 like JavaScript's Number.prototype.toString.
func Number(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		text := strconv.FormatFloat(f, 'e', -1, 64)
		mantissa, exponent, _ := strings.Cut(text, "e")
		sign := exponent[0]
		digits := strings.TrimLeft(exponent[1:], "0")
		return mantissa + "e" + string(sign) + digits
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ToNumber is pi-catalog's toNumber: a finite JSON number, or a non-blank
// string that JavaScript's Number() reads as a finite value, including the
// 0x, 0o and 0b integer prefixes.
func ToNumber(value any) (float64, bool) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case string:
		value = strings.TrimSpace(value)
		if len(value) > 2 && value[0] == '0' && strings.ContainsRune("xXoObB", rune(value[1])) {
			integer, err := strconv.ParseUint(value, 0, 64)
			if err != nil {
				return 0, false
			}
			number = float64(integer)
		} else {
			var err error
			if number, err = strconv.ParseFloat(value, 64); err != nil {
				return 0, false
			}
		}
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}
