package cursor

import (
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

type cachedModels struct {
	models  []UsableModel
	fetched time.Time
}

// modelCache holds each signed-in account's discovered models by auth ID; the
// registry publishes them and the executor reads each slug's max-mode flag.
var modelCache sync.Map

// RememberModels records the models discovered for one auth.
func RememberModels(authID string, models []UsableModel) {
	modelCache.Store(authID, cachedModels{models: append([]UsableModel(nil), models...), fetched: time.Now()})
}

// CachedModels returns the models last discovered for one auth and when.
func CachedModels(authID string) ([]UsableModel, time.Time, bool) {
	v, ok := modelCache.Load(authID)
	if !ok {
		return nil, time.Time{}, false
	}
	c := v.(cachedModels)
	return append([]UsableModel(nil), c.models...), c.fetched, true
}

// LookupModel finds one discovered slug for an auth.
func LookupModel(authID, slug string) (UsableModel, bool) {
	v, ok := modelCache.Load(authID)
	if !ok {
		return UsableModel{}, false
	}
	for _, m := range v.(cachedModels).models {
		if m.ID == slug {
			return m, true
		}
	}
	return UsableModel{}, false
}

// DecodeUsableModels decodes a GetUsableModelsResponse body, raw or wrapped in
// one Connect envelope.
//
//	message GetUsableModelsResponse { repeated ModelDetails models = 1; }
//	message ModelDetails { string model_id = 1; string display_name = 4; optional bool max_mode = 7; … }
func DecodeUsableModels(body []byte) ([]UsableModel, error) {
	if payload, ok := connectUnaryPayload(body); ok {
		body = payload
	}
	var out []UsableModel
	seen := map[string]bool{}
	err := walkFields(body, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
		if num != 1 || typ != protowire.BytesType {
			return nil
		}
		var m UsableModel
		errModel := walkFields(value, func(n protowire.Number, t protowire.Type, v []byte, varint uint64) error {
			switch {
			case n == 1 && t == protowire.BytesType:
				m.ID = strings.TrimSpace(string(v))
			case n == 4 && t == protowire.BytesType:
				m.DisplayName = strings.TrimSpace(string(v))
			case n == 7 && t == protowire.VarintType:
				m.MaxMode = varint != 0
			}
			return nil
		})
		if errModel != nil {
			return errModel
		}
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// connectUnaryPayload unwraps the first non-trailer Connect envelope, the way
// NeoPi's unary decoder tolerates both raw and enveloped bodies.
func connectUnaryPayload(body []byte) ([]byte, bool) {
	for off := 0; off+5 <= len(body); {
		flags := body[off]
		n := int(binary.BigEndian.Uint32(body[off+1 : off+5]))
		end := off + 5 + n
		if n < 0 || end > len(body) || flags&0x01 != 0 {
			return nil, false
		}
		if flags&0x02 == 0 {
			return body[off+5 : end], true
		}
		off = end
	}
	return nil, false
}

func walkFields(b []byte, fn func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return errors.New("cursor: malformed protobuf tag")
		}
		b = b[n:]
		var value []byte
		var varint uint64
		switch typ {
		case protowire.VarintType:
			varint, n = protowire.ConsumeVarint(b)
		case protowire.BytesType:
			value, n = protowire.ConsumeBytes(b)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return errors.New("cursor: malformed protobuf field")
		}
		b = b[n:]
		if err := fn(num, typ, value, varint); err != nil {
			return err
		}
	}
	return nil
}
