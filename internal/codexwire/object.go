package codexwire

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// object mirrors a JavaScript object: assigning an existing key keeps its
// position, a new key is appended, and deletion removes it.
type object []jsjson.Field

// parseObject reads a JSON object as JSON.parse would, re-serializing its
// values in JSON.stringify form so passthrough fields match NeoPi byte-for-byte.
func parseObject(raw []byte) object {
	var out object
	gjson.ParseBytes(raw).ForEach(func(key, value gjson.Result) bool {
		out.set(key.String(), jsjson.Normalize([]byte(value.Raw)))
		return true
	})
	return out
}

func (o object) index(name string) int {
	for i := range o {
		if o[i].Name == name {
			return i
		}
	}
	return -1
}

func (o object) get(name string) gjson.Result {
	if i := o.index(name); i >= 0 {
		return gjson.ParseBytes(o[i].Value)
	}
	return gjson.Result{}
}

func (o object) has(name string) bool { return o.index(name) >= 0 }

func (o *object) set(name string, value json.RawMessage) {
	if i := o.index(name); i >= 0 {
		(*o)[i].Value = value
		return
	}
	*o = append(*o, jsjson.Field{Name: name, Value: value})
}

func (o *object) del(name string) {
	if i := o.index(name); i >= 0 {
		*o = append((*o)[:i], (*o)[i+1:]...)
	}
}

func (o object) bytes() json.RawMessage { return jsjson.Object(o...) }
