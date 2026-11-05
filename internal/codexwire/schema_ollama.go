package codexwire

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// ollamaSchemaValueKeys is OLLAMA_SCHEMA_VALUE_KEYS: keywords whose value is a
// single subschema (or, for items, an array of them).
var ollamaSchemaValueKeys = setOf("items", "additionalItems", "contains", "contentSchema", "propertyNames",
	"if", "then", "else", "not", "additionalProperties", "unevaluatedItems", "unevaluatedProperties")

// SanitizeSchemaForOllama is sanitizeSchemaForOllama: a tool's wire schema
// rewritten into forms Ollama's Go /api/chat tool parser can unmarshal into
// its object-shaped Schema. Boolean subschemas become an explicit union of
// every JSON type, boolean additionalProperties/unevaluatedProperties are
// dropped, and a type array becomes a single type or an anyOf of types.
func SanitizeSchemaForOllama(schema json.RawMessage) json.RawMessage {
	return encodeTree(sanitizeOllamaNode(decodeTree(gjson.ParseBytes(schema))))
}

// ollamaOpenSubschema is OPEN_SUBSCHEMA_WIDENING, fresh per use.
func ollamaOpenSubschema() *jmap {
	variants := make([]any, 0, 6)
	for _, kind := range []string{"string", "number", "boolean", "object", "array", "null"} {
		variant := newJmap()
		variant.set("type", kind)
		variants = append(variants, variant)
	}
	out := newJmap()
	out.set("anyOf", variants)
	return out
}

func sanitizeOllamaNode(value any) any {
	switch typed := value.(type) {
	case bool:
		if typed {
			return ollamaOpenSubschema()
		}
		out := newJmap()
		out.set("not", ollamaOpenSubschema())
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeOllamaNode(item)
		}
		return out
	}
	node, ok := asMap(value)
	if !ok {
		return value
	}
	out := newJmap()
	var typeAlternatives []any
	for _, key := range node.keys {
		child := node.get(key)
		if _, isBool := child.(bool); isBool && (key == "additionalProperties" || key == "unevaluatedProperties") {
			continue
		}
		if variants, isArray := child.([]any); isArray && key == "type" {
			var unique []string
			seen := map[string]bool{}
			for _, entry := range variants {
				if name, isString := entry.(string); isString && !seen[name] {
					seen[name] = true
					unique = append(unique, name)
				}
			}
			var nonNull []string
			for _, name := range unique {
				if name != "null" {
					nonNull = append(nonNull, name)
				}
			}
			switch {
			case len(nonNull) > 1:
				for _, name := range unique {
					alternative := newJmap()
					alternative.set("type", name)
					typeAlternatives = append(typeAlternatives, alternative)
				}
			case len(nonNull) == 1:
				out.set("type", nonNull[0])
			case len(unique) > 0:
				out.set("type", unique[0])
			case len(variants) > 0:
				out.set("type", variants[0])
			}
			// An empty array leaves type undefined, which JSON.stringify omits.
			continue
		}
		next := child
		if entries, isMap := asMap(child); isMap && normalizeSubschemaMapKeys[key] {
			mapped := newJmap()
			for _, childKey := range entries.keys {
				mapped.set(childKey, sanitizeOllamaNode(entries.get(childKey)))
			}
			next = mapped
		} else if items, isArray := child.([]any); isArray && normalizeSubschemaArrayKeys[key] {
			next = sanitizeOllamaNode(items)
		} else if ollamaSchemaValueKeys[key] {
			next = sanitizeOllamaNode(child)
		}
		out.set(key, next)
	}
	if typeAlternatives != nil {
		union := newJmap()
		union.set("anyOf", typeAlternatives)
		allOf := []any{union}
		if existing, isArray := out.get("allOf").([]any); isArray {
			allOf = append(allOf, existing...)
		}
		out.set("allOf", allOf)
	}
	return out
}
