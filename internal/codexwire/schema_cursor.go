package codexwire

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// SanitizeSchemaForCursor is sanitizeSchemaForCursor: a tool's wire schema
// projected onto the subset Cursor's MCP catalog accepts, with every
// anyOf/oneOf/allOf folded out while only widening acceptance.
func SanitizeSchemaForCursor(schema json.RawMessage) json.RawMessage {
	return encodeTree(projectSchemaForCursor(dereferenceSchema(decodeTree(gjson.ParseBytes(schema))), false))
}

var schemaCompositionCombiners = []string{"allOf", "anyOf", "oneOf"}

func projectSchemaForCursor(value any, insideSchemaMap bool) any {
	if items, ok := value.([]any); ok {
		out := make([]any, len(items))
		for i, entry := range items {
			out[i] = projectSchemaForCursor(entry, false)
		}
		return out
	}
	node, ok := asMap(value)
	if !ok {
		return value
	}
	result := newJmap()
	for _, key := range node.keys {
		entry := node.vals[key]
		// A negation cannot be widened faithfully; dropping it only broadens.
		if !insideSchemaMap && key == "not" && containsSchemaCombiner(entry, false) {
			continue
		}
		if kind := classifyNormalizeChild(key, entry, insideSchemaMap); kind != "" {
			result.set(key, projectSchemaForCursor(entry, kind == "map"))
		} else {
			result.set(key, entry)
		}
	}
	if insideSchemaMap {
		return result
	}
	return projectNodeCombinersForCursor(result)
}

// containsSchemaCombiner reports a composition keyword in a schema position.
func containsSchemaCombiner(value any, insideSchemaMap bool) bool {
	if items, ok := value.([]any); ok {
		for _, entry := range items {
			if containsSchemaCombiner(entry, false) {
				return true
			}
		}
		return false
	}
	node, ok := asMap(value)
	if !ok {
		return false
	}
	if !insideSchemaMap {
		for _, combiner := range schemaCompositionCombiners {
			if _, isArray := node.get(combiner).([]any); isArray {
				return true
			}
		}
	}
	for _, key := range node.keys {
		if kind := classifyNormalizeChild(key, node.vals[key], insideSchemaMap); kind != "" && containsSchemaCombiner(node.vals[key], kind == "map") {
			return true
		}
	}
	return false
}

func projectNodeCombinersForCursor(node *jmap) *jmap {
	current := node
	for changed := true; changed; {
		changed = false
		for _, combiner := range schemaCompositionCombiners {
			if _, isArray := current.get(combiner).([]any); !isArray {
				continue
			}
			source := current
			merged := mergeObjectCombinerVariants(current, combiner)
			if merged != current {
				current = merged
				if properties, ok := asMap(current.get("properties")); ok {
					if combiner != "allOf" {
						sourceProperties, _ := asMap(source.get("properties"))
						if sourceProperties == nil {
							sourceProperties = newJmap()
						}
						sourceVariants, _ := source.get(combiner).([]any)
						for _, name := range properties.keys {
							if sourceProperties.has(name) {
								properties.set(name, sourceProperties.get(name))
								continue
							}
							var widened any
							haveWidened := false
							for _, raw := range sourceVariants {
								variant, _ := asMap(raw)
								variantProperties, _ := asMap(variant.get("properties"))
								var constraint any
								switch additional := variant.get("additionalProperties"); {
								case variantProperties != nil && variantProperties.has(name):
									constraint = variantProperties.get(name)
								case additional == false:
									continue
								default:
									if additionalMap, isMap := asMap(additional); isMap {
										constraint = additionalMap
									} else {
										constraint = newJmap()
									}
								}
								if !haveWidened {
									widened, haveWidened = constraint, true
								} else {
									widened = mergePropertySchemas(widened, constraint)
								}
							}
							if !haveWidened {
								widened = newJmap()
							}
							properties.set(name, widened)
						}
					}
					for _, name := range properties.keys {
						properties.set(name, projectSchemaForCursor(properties.get(name), false))
					}
				}
			} else {
				current = current.clone()
				current.del(combiner)
			}
			changed = true
		}
	}
	return current
}

// mergeObjectCombinerVariants folds object-shaped combiner branches into the
// node's own properties; any other branch shape leaves the node unchanged.
func mergeObjectCombinerVariants(schema *jmap, combiner string) *jmap {
	raw, _ := schema.get(combiner).([]any)
	if len(raw) == 0 {
		return schema
	}
	variants := make([]*jmap, 0, len(raw))
	for _, entry := range raw {
		variant, ok := asMap(entry)
		if !ok {
			return schema
		}
		_, propsIsMap := asMap(variant.get("properties"))
		_, requiredIsArray := variant.get("required").([]any)
		hasObjectShape := propsIsMap || requiredIsArray || variant.has("additionalProperties")
		variantType, hasType := variant.vals["type"]
		if !hasType && !hasObjectShape {
			return schema
		}
		if hasType && variantType != "object" {
			return schema
		}
		if variant.has("properties") && !propsIsMap {
			return schema
		}
		if variant.has("required") && !requiredIsArray {
			return schema
		}
		variants = append(variants, variant)
	}
	mergedProperties := newJmap()
	ownProperties, _ := asMap(schema.get("properties"))
	if ownProperties == nil {
		ownProperties = newJmap()
	}
	for _, name := range ownProperties.keys {
		mergedProperties.set(name, ownProperties.get(name))
	}
	for _, variant := range variants {
		properties, _ := asMap(variant.get("properties"))
		if properties == nil {
			continue
		}
		for _, name := range properties.keys {
			if existing, ok := mergedProperties.vals[name]; ok {
				mergedProperties.set(name, mergePropertySchemas(existing, properties.get(name)))
			} else {
				mergedProperties.set(name, properties.get(name))
			}
		}
	}
	next := schema.clone()
	next.del(combiner)
	next.set("type", "object")
	next.set("properties", mergedProperties)

	stringsOf := func(value any) []string {
		var out []string
		items, _ := value.([]any)
		for _, item := range items {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	}
	var combined []string
	if combiner == "allOf" {
		seen := map[string]bool{}
		for _, variant := range variants {
			for _, name := range stringsOf(variant.get("required")) {
				if !seen[name] {
					seen[name] = true
					combined = append(combined, name)
				}
			}
		}
	} else {
		for i, variant := range variants {
			required := stringsOf(variant.get("required"))
			if i == 0 {
				combined = required
				continue
			}
			keep := map[string]bool{}
			for _, name := range required {
				keep[name] = true
			}
			filtered := combined[:0:0]
			for _, name := range combined {
				if keep[name] {
					filtered = append(filtered, name)
				}
			}
			combined = filtered
		}
	}
	safe := map[string]bool{}
	for _, name := range combined {
		if mergedProperties.has(name) {
			safe[name] = true
		}
	}
	for _, name := range stringsOf(schema.get("required")) {
		if ownProperties.has(name) && mergedProperties.has(name) {
			safe[name] = true
		}
	}
	var required []any
	for _, name := range mergedProperties.keys {
		if safe[name] {
			required = append(required, name)
		}
	}
	if len(required) > 0 {
		next.set("required", required)
	} else {
		next.del("required")
	}
	return next
}

// mergePropertySchemas is mergePropertySchemas (equality.ts).
func mergePropertySchemas(existing, incoming any) any {
	if jsonEqual(existing, incoming) {
		return existing
	}
	if merged := mergeCompatibleEnumSchemas(existing, incoming); merged != nil {
		return merged
	}
	anyOfVariants := func(schema any) []any {
		if node, ok := asMap(schema); ok {
			if variants, isArray := node.get("anyOf").([]any); isArray {
				return variants
			}
		}
		return []any{schema}
	}
	merged := append([]any(nil), anyOfVariants(existing)...)
	for _, variant := range anyOfVariants(incoming) {
		seen := false
		for _, have := range merged {
			if jsonEqual(have, variant) {
				seen = true
				break
			}
		}
		if !seen {
			merged = append(merged, variant)
		}
	}
	if len(merged) == 1 {
		return merged[0]
	}
	out := newJmap()
	out.set("anyOf", merged)
	return out
}

func mergeCompatibleEnumSchemas(existing, incoming any) *jmap {
	left, ok := asMap(existing)
	right, ok2 := asMap(incoming)
	if !ok || !ok2 {
		return nil
	}
	leftEnum, ok := left.get("enum").([]any)
	rightEnum, ok2 := right.get("enum").([]any)
	if !ok || !ok2 || !jsonEqual(left.get("type"), right.get("type")) {
		return nil
	}
	if len(left.keys) != len(right.keys) {
		return nil
	}
	for _, key := range left.keys {
		if key == "enum" {
			continue
		}
		if !right.has(key) || !jsonEqual(left.get(key), right.get(key)) {
			return nil
		}
	}
	merged := append([]any(nil), leftEnum...)
	for _, value := range rightEnum {
		seen := false
		for _, have := range merged {
			if jsonEqual(have, value) {
				seen = true
				break
			}
		}
		if !seen {
			merged = append(merged, value)
		}
	}
	out := left.clone()
	out.set("enum", merged)
	return out
}
