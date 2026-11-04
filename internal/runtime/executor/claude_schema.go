package executor

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// NeoPi uses Anthropic's supported schema subset and spills unsupported
// constraints into descriptions. Strict decoding is deliberately limited to
// python/edit/find, with a request-wide optional/union grammar budget.
func neoPiSchemaType(schema gjson.Result) string {
	t := schema.Get("type")
	if t.Type == gjson.String {
		return t.String()
	}
	for _, candidate := range t.Array() {
		if candidate.Type == gjson.String && candidate.String() != "null" {
			return candidate.String()
		}
	}
	if schema.Get("properties").IsObject() {
		return "object"
	}
	if schema.Get("items").Exists() || schema.Get("prefixItems").IsArray() {
		return "array"
	}
	return ""
}

func neoPiSchemaKeyword(key string) bool {
	switch key {
	case "$ref", "$defs", "$schema", "definitions", "type", "anyOf", "allOf", "enum", "const", "description", "title", "default", "nullable":
		return true
	}
	return false
}

func neoPiNormalizeSchema(schema gjson.Result, root bool) json.RawMessage {
	if schema.IsArray() {
		out := make([]json.RawMessage, 0, len(schema.Array()))
		for _, value := range schema.Array() {
			out = append(out, neoPiNormalizeSchema(value, false))
		}
		return jsjson.Marshal(out)
	}
	if !schema.IsObject() {
		return []byte(schema.Raw)
	}
	typeName := neoPiSchemaType(schema)
	var fields []jsjson.Field
	var spill []string
	schema.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		keep := neoPiSchemaKeyword(name)
		switch typeName {
		case "object":
			keep = keep || name == "properties" || name == "required" || name == "additionalProperties"
		case "array":
			keep = keep || name == "items" || name == "prefixItems" || name == "minItems"
		case "string":
			keep = keep || name == "format"
		}
		if root && (name == "anyOf" || name == "allOf" || name == "oneOf") {
			keep = false
		}
		if name == "format" && typeName == "string" && value.Type == gjson.String {
			switch value.String() {
			case "date-time", "time", "date", "duration", "email", "hostname", "uri", "ipv4", "ipv6", "uuid":
			default:
				keep = false
			}
		}
		if name == "minItems" && typeName == "array" && (value.Type != gjson.Number || (value.Float() != 0 && value.Float() != 1)) {
			keep = false
		}
		if !keep {
			spill = append(spill, name+": "+value.Raw)
			return true
		}
		raw := json.RawMessage(value.Raw)
		switch name {
		case "properties", "$defs", "definitions":
			if value.IsObject() {
				var children []jsjson.Field
				value.ForEach(func(k, v gjson.Result) bool {
					children = append(children, jsjson.Field{Name: k.String(), Value: neoPiNormalizeSchema(v, false)})
					return true
				})
				raw = jsjson.Object(children...)
			}
		case "items", "prefixItems", "anyOf", "allOf", "oneOf":
			raw = neoPiNormalizeSchema(value, false)
		case "additionalProperties":
			if value.IsObject() {
				raw = neoPiNormalizeSchema(value, false)
				if string(raw) == "{}" {
					raw = []byte("true")
				}
			}
		}
		fields = append(fields, jsjson.Field{Name: name, Value: raw})
		return true
	})
	if typeName == "object" && !schema.Get("additionalProperties").Exists() {
		fields = append(fields, jsjson.Field{Name: "additionalProperties", Value: []byte("false")})
	}
	out := jsjson.Object(fields...)
	if len(spill) > 0 {
		description := schema.Get("description").String()
		suffix := "{" + strings.Join(spill, ", ") + "}"
		if description != "" {
			description += "\n\n"
		}
		out = neoPiSet(out, "description", description+suffix)
	}
	return out
}

func neoPiStrictIncompatible(schema gjson.Result) bool {
	if schema.IsObject() {
		for _, key := range []string{"oneOf", "allOf", "$ref", "patternProperties", "propertyNames"} {
			if schema.Get(key).Exists() {
				return true
			}
		}
	}
	found := false
	if schema.IsObject() || schema.IsArray() {
		schema.ForEach(func(_, child gjson.Result) bool { found = neoPiStrictIncompatible(child); return !found })
	}
	return found
}

type neoPiStrictBudget struct{ optional, unions int }

func neoPiStrictSchema(schema gjson.Result, budget *neoPiStrictBudget) (json.RawMessage, bool) {
	if schema.IsArray() {
		out := make([]json.RawMessage, 0, len(schema.Array()))
		for _, child := range schema.Array() {
			normalized, ok := neoPiStrictSchema(child, budget)
			if !ok {
				return nil, false
			}
			out = append(out, normalized)
		}
		return jsjson.Marshal(out), true
	}
	if !schema.IsObject() {
		return []byte(schema.Raw), true
	}
	defining := false
	for _, key := range []string{"type", "properties", "additionalProperties", "items", "prefixItems", "enum", "const", "$ref", "anyOf", "allOf", "oneOf", "$defs", "definitions"} {
		if schema.Get(key).Exists() {
			defining = true
			break
		}
	}
	if !defining {
		return nil, false
	}
	if neoPiSchemaType(schema) == "object" && schema.Get("additionalProperties").Raw != "false" {
		return nil, false
	}
	if schema.Get("type").IsArray() || schema.Get("anyOf").IsArray() {
		if budget.unions <= 0 {
			return nil, false
		}
		budget.unions--
	}
	out := []byte(schema.Raw)
	if properties := schema.Get("properties"); properties.IsObject() {
		required := map[string]bool{}
		for _, key := range schema.Get("required").Array() {
			if key.Type == gjson.String {
				required[key.String()] = true
			}
		}
		var fields []jsjson.Field
		nextRequired := []string{}
		ok := true
		properties.ForEach(func(key, child gjson.Result) bool {
			normalized, valid := neoPiStrictSchema(child, budget)
			if !valid {
				ok = false
				return false
			}
			if required[key.String()] {
				nextRequired = append(nextRequired, key.String())
			} else if budget.optional > 0 {
				budget.optional--
			} else {
				normalized, valid = neoPiNullableSchema(normalized, budget)
				if !valid {
					ok = false
					return false
				}
				nextRequired = append(nextRequired, key.String())
			}
			fields = append(fields, jsjson.Field{Name: key.String(), Value: normalized})
			return true
		})
		if !ok {
			return nil, false
		}
		out = neoPiSetRaw(out, "properties", jsjson.Object(fields...))
		out = neoPiSet(out, "required", nextRequired)
	}
	for _, name := range []string{"items", "prefixItems", "anyOf", "allOf", "oneOf"} {
		value := schema.Get(name)
		if value.IsObject() || value.IsArray() {
			normalized, ok := neoPiStrictSchema(value, budget)
			if !ok {
				return nil, false
			}
			out = neoPiSetRaw(out, name, normalized)
		}
	}
	for _, name := range []string{"$defs", "definitions"} {
		if value := schema.Get(name); value.IsObject() {
			var fields []jsjson.Field
			ok := true
			value.ForEach(func(key, child gjson.Result) bool {
				normalized, valid := neoPiStrictSchema(child, budget)
				if !valid {
					ok = false
					return false
				}
				fields = append(fields, jsjson.Field{Name: key.String(), Value: normalized})
				return true
			})
			if !ok {
				return nil, false
			}
			out = neoPiSetRaw(out, name, jsjson.Object(fields...))
		}
	}
	return out, true
}

func neoPiNullableSchema(schema []byte, budget *neoPiStrictBudget) (json.RawMessage, bool) {
	value := gjson.ParseBytes(schema)
	if types := value.Get("type"); types.IsArray() {
		for _, t := range types.Array() {
			if t.String() == "null" {
				return schema, true
			}
		}
		return neoPiSet(schema, "type.-1", "null"), true
	}
	if variants := value.Get("anyOf"); variants.IsArray() {
		for _, v := range variants.Array() {
			if v.Get("type").String() == "null" {
				return schema, true
			}
		}
		return neoPiSetRaw(schema, "anyOf.-1", []byte(`{"type":"null"}`)), true
	}
	if budget.unions <= 0 {
		return nil, false
	}
	budget.unions--
	return jsjson.Object(jsjson.Field{Name: "anyOf", Value: jsjson.Marshal([]json.RawMessage{schema, []byte(`{"type":"null"}`)})}), true
}
