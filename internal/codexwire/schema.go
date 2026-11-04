package codexwire

import (
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

var (
	responsesSchemaArrayKeys = map[string]bool{"anyOf": true, "oneOf": true, "allOf": true, "prefixItems": true}
	responsesSchemaMapKeys   = map[string]bool{"properties": true, "patternProperties": true, "dependencies": true, "dependentSchemas": true, "$defs": true, "definitions": true}
	responsesSchemaValueKeys = map[string]bool{"items": true, "additionalItems": true, "contains": true, "contentSchema": true, "propertyNames": true, "if": true, "then": true, "else": true, "not": true, "additionalProperties": true, "unevaluatedItems": true, "unevaluatedProperties": true}
)

const patternPropertiesFallback = ".*"

func hasUnsupportedRegexLookaround(pattern string) bool {
	start := strings.Index(pattern, "(?")
	for start != -1 {
		escapes := 0
		for i := start - 1; i >= 0 && pattern[i] == '\\'; i-- {
			escapes++
		}
		if escapes%2 == 0 && start+2 < len(pattern) {
			operator := pattern[start+2 : start+3]
			if operator == "<" && start+4 <= len(pattern) {
				operator = pattern[start+2 : start+4]
			}
			switch operator {
			case "=", "!", "<=", "<!":
				return true
			}
		}
		next := strings.Index(pattern[start+2:], "(?")
		if next == -1 {
			break
		}
		start += 2 + next
	}
	return false
}

func declaresObjectType(value gjson.Result) bool {
	if value.Type == gjson.String {
		return value.String() == "object"
	}
	for _, variant := range value.Array() {
		if variant.Type == gjson.String && variant.String() == "object" {
			return true
		}
	}
	return false
}

// sanitizeResponsesSchema is NeoPi's sanitizeSchemaForOpenAIResponses: unions
// become anyOf, unsupported lookarounds are dropped, object nodes gain properties.
func sanitizeResponsesSchema(value gjson.Result) json.RawMessage {
	if !value.IsObject() {
		return jsjson.Normalize([]byte(value.Raw))
	}
	if len(value.Map()) == 0 {
		return json.RawMessage("true")
	}
	var out object
	value.ForEach(func(key, child gjson.Result) bool {
		name := key.String()
		if name == "oneOf" && child.IsArray() {
			return true
		}
		if name == "pattern" && child.Type == gjson.String && hasUnsupportedRegexLookaround(child.String()) {
			return true
		}
		switch {
		case name == "patternProperties" && child.IsObject():
			out.set(name, sanitizeResponsesSchemaMap(child, true))
		case responsesSchemaMapKeys[name] && child.IsObject():
			out.set(name, sanitizeResponsesSchemaMap(child, false))
		case responsesSchemaArrayKeys[name] && child.IsArray():
			out.set(name, sanitizeResponsesSchemaArray(child))
		case responsesSchemaValueKeys[name] && child.IsObject():
			out.set(name, sanitizeResponsesSchema(child))
		default:
			out.set(name, jsjson.Normalize([]byte(child.Raw)))
		}
		return true
	})
	if oneOf := value.Get("oneOf"); oneOf.IsArray() {
		rewritten := sanitizeResponsesSchemaArray(oneOf)
		if existing := out.get("anyOf"); existing.IsArray() {
			merged := append(append([]json.RawMessage{}, rawElements(existing)...), rawElements(gjson.ParseBytes(rewritten))...)
			out.set("anyOf", jsjson.Marshal(merged))
		} else {
			out.set("anyOf", rewritten)
		}
	}
	if declaresObjectType(value.Get("type")) && !value.Get("properties").Exists() {
		out.set("properties", json.RawMessage("{}"))
	}
	if len(out) == 0 {
		return json.RawMessage("true")
	}
	return out.bytes()
}

func rawElements(array gjson.Result) []json.RawMessage {
	var out []json.RawMessage
	for _, element := range array.Array() {
		out = append(out, json.RawMessage(element.Raw))
	}
	return out
}

func sanitizeResponsesSchemaArray(value gjson.Result) json.RawMessage {
	var out []json.RawMessage
	for _, element := range value.Array() {
		out = append(out, sanitizeResponsesSchema(element))
	}
	if out == nil {
		return json.RawMessage("[]")
	}
	return jsjson.Marshal(out)
}

func sanitizeResponsesSchemaMap(value gjson.Result, stripRegexKeys bool) json.RawMessage {
	var out object
	value.ForEach(func(key, child gjson.Result) bool {
		next := sanitizeResponsesSchema(child)
		if stripRegexKeys && hasUnsupportedRegexLookaround(key.String()) {
			existing := out.get(patternPropertiesFallback)
			switch {
			case !existing.Exists():
				out.set(patternPropertiesFallback, next)
			case existing.IsObject() && existing.Get("anyOf").IsArray() && len(existing.Map()) == 1:
				out.set(patternPropertiesFallback, jsjson.Object(jsjson.Field{Name: "anyOf", Value: jsjson.Marshal(append(rawElements(existing.Get("anyOf")), next))}))
			default:
				out.set(patternPropertiesFallback, jsjson.Object(jsjson.Field{Name: "anyOf", Value: jsjson.Marshal([]json.RawMessage{json.RawMessage(existing.Raw), next})}))
			}
			return true
		}
		out.set(key.String(), next)
		return true
	})
	return out.bytes()
}
