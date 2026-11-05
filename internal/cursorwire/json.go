package cursorwire

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// JSON values here are JavaScript's: nil, bool, float64, string, []any and
// *JSONObject, whose properties enumerate in JavaScript order.

// JSONObject is a JavaScript object of JSON values.
type JSONObject struct{ Map[any] }

// ParseJSON is JSON.parse: ok is false for text that is not one JSON value.
func ParseJSON(text string) (any, bool) {
	if !gjson.Valid(text) {
		return nil, false
	}
	return FromGJSON(gjson.Parse(text)), true
}

// FromGJSON converts a parsed value; a repeated key keeps its first position
// and its last value, as JSON.parse does.
func FromGJSON(value gjson.Result) any {
	switch {
	case value.IsObject():
		object := &JSONObject{}
		value.ForEach(func(key, child gjson.Result) bool {
			object.Set(key.String(), FromGJSON(child))
			return true
		})
		return object
	case value.IsArray():
		items := []any{}
		for _, child := range value.Array() {
			items = append(items, FromGJSON(child))
		}
		return items
	case value.Type == gjson.String:
		return value.String()
	case value.Type == gjson.Number:
		return value.Float()
	case value.Type == gjson.True:
		return true
	case value.Type == gjson.False:
		return false
	}
	return nil
}

// StringifyJSON is JSON.stringify of a JSON value.
func StringifyJSON(value any) string {
	return string(appendJSON(nil, value))
}

func appendJSON(b []byte, value any) []byte {
	switch v := value.(type) {
	case nil:
		return append(b, "null"...)
	case bool:
		if v {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case float64:
		return append(b, jsjson.Number(v)...)
	case string:
		return append(b, jsjson.Marshal(v)...)
	case []any:
		b = append(b, '[')
		for i, item := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSON(b, item)
		}
		return append(b, ']')
	case *JSONObject:
		b = append(b, '{')
		for i, key := range v.Keys() {
			if i > 0 {
				b = append(b, ',')
			}
			item, _ := v.Get(key)
			b = append(b, jsjson.Marshal(key)...)
			b = append(b, ':')
			b = appendJSON(b, item)
		}
		return append(b, '}')
	}
	panic(fmt.Sprintf("cursorwire: %T is not a JSON value", value))
}

// EncodeJSONValue is encodeJsonValue: the value as google.protobuf.Value.
func EncodeJSONValue(value any) []byte { return appendJSONValue(nil, value) }

func appendJSONValue(b []byte, value any) []byte {
	switch v := value.(type) {
	case nil:
		b = appendTag(b, 1, wireVarint)
		return appendVarint(b, 0)
	case float64:
		b = appendTag(b, 2, wireFixed64)
		return appendDouble(b, v)
	case string:
		b = appendTag(b, 3, wireBytes)
		return appendLengthDelimited(b, []byte(v))
	case bool:
		b = appendTag(b, 4, wireVarint)
		return appendBool(b, v)
	case []any:
		var list []byte
		for _, item := range v {
			list = appendTag(list, 1, wireBytes)
			list = appendLengthDelimited(list, appendJSONValue(nil, item))
		}
		b = appendTag(b, 6, wireBytes)
		return appendLengthDelimited(b, list)
	case *JSONObject:
		var structBytes []byte
		for _, key := range v.Keys() {
			item, _ := v.Get(key)
			entry := appendTag(nil, 1, wireBytes)
			entry = appendLengthDelimited(entry, []byte(key))
			entry = appendTag(entry, 2, wireBytes)
			entry = appendLengthDelimited(entry, appendJSONValue(nil, item))
			structBytes = appendTag(structBytes, 1, wireBytes)
			structBytes = appendLengthDelimited(structBytes, entry)
		}
		b = appendTag(b, 5, wireBytes)
		return appendLengthDelimited(b, structBytes)
	}
	panic(fmt.Sprintf("cursorwire: %T is not a JSON value", value))
}

// DecodeJSONValue is decodeJsonValue: google.protobuf.Value bytes as a JSON
// value; the last kind field wins and an empty value is null.
func DecodeJSONValue(data []byte) (any, error) {
	r := reader{buf: data}
	var value any
	for r.more() {
		tag, err := r.uint32()
		if err != nil {
			return nil, err
		}
		number, wireType := int32(tag>>3), int(tag&7)
		if !validWireType(wireType) {
			return nil, fmt.Errorf("Unsupported wire type %d in google.protobuf.Value", wireType)
		}
		switch number {
		case 1:
			if err = expectWire(wireType, wireVarint); err != nil {
				return nil, err
			}
			if _, err = r.uint32(); err != nil {
				return nil, err
			}
			value = nil
		case 2:
			if err = expectWire(wireType, wireFixed64); err != nil {
				return nil, err
			}
			if value, err = r.double(); err != nil {
				return nil, err
			}
		case 3:
			if err = expectWire(wireType, wireBytes); err != nil {
				return nil, err
			}
			if value, err = r.string(); err != nil {
				return nil, err
			}
		case 4:
			if err = expectWire(wireType, wireVarint); err != nil {
				return nil, err
			}
			if value, err = r.bool(); err != nil {
				return nil, err
			}
		case 5:
			if err = expectWire(wireType, wireBytes); err != nil {
				return nil, err
			}
			nested, errBytes := r.bytes()
			if errBytes != nil {
				return nil, errBytes
			}
			if value, err = decodeJSONStruct(nested); err != nil {
				return nil, err
			}
		case 6:
			if err = expectWire(wireType, wireBytes); err != nil {
				return nil, err
			}
			nested, errBytes := r.bytes()
			if errBytes != nil {
				return nil, errBytes
			}
			if value, err = decodeJSONList(nested); err != nil {
				return nil, err
			}
		default:
			if err = r.skip(wireType); err != nil {
				return nil, err
			}
		}
	}
	return value, nil
}

func decodeJSONStruct(data []byte) (*JSONObject, error) {
	r := reader{buf: data}
	object := &JSONObject{}
	for r.more() {
		tag, err := r.uint32()
		if err != nil {
			return nil, err
		}
		number, wireType := int32(tag>>3), int(tag&7)
		if !validWireType(wireType) {
			return nil, fmt.Errorf("Unsupported wire type %d in Struct", wireType)
		}
		if number != 1 {
			if err = r.skip(wireType); err != nil {
				return nil, err
			}
			continue
		}
		if err = expectWire(wireType, wireBytes); err != nil {
			return nil, err
		}
		entry, err := r.bytes()
		if err != nil {
			return nil, err
		}
		e := reader{buf: entry}
		key := ""
		var item any
		for e.more() {
			entryTag, errTag := e.uint32()
			if errTag != nil {
				return nil, errTag
			}
			switch entryTag >> 3 {
			case 1:
				if key, err = e.string(); err != nil {
					return nil, err
				}
			case 2:
				nested, errBytes := e.bytes()
				if errBytes != nil {
					return nil, errBytes
				}
				if item, err = DecodeJSONValue(nested); err != nil {
					return nil, err
				}
			default:
				if wire := int(entryTag & 7); validWireType(wire) {
					if err = e.skip(wire); err != nil {
						return nil, err
					}
				}
			}
		}
		object.Set(key, item)
	}
	return object, nil
}

func decodeJSONList(data []byte) ([]any, error) {
	r := reader{buf: data}
	list := []any{}
	for r.more() {
		tag, err := r.uint32()
		if err != nil {
			return nil, err
		}
		number, wireType := int32(tag>>3), int(tag&7)
		if !validWireType(wireType) {
			return nil, fmt.Errorf("Unsupported wire type %d in ListValue", wireType)
		}
		if number != 1 {
			if err = r.skip(wireType); err != nil {
				return nil, err
			}
			continue
		}
		if err = expectWire(wireType, wireBytes); err != nil {
			return nil, err
		}
		nested, err := r.bytes()
		if err != nil {
			return nil, err
		}
		item, err := DecodeJSONValue(nested)
		if err != nil {
			return nil, err
		}
		list = append(list, item)
	}
	return list, nil
}
