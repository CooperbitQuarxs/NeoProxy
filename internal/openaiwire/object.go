package openaiwire

import (
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// object mirrors a JavaScript object: assigning an existing key keeps its
// position, a new key is appended, and deletion removes it.
type object []jsjson.Field

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
