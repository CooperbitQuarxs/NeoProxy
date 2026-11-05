package codexwire

import (
	"encoding/json"
	"math"

	"github.com/tidwall/gjson"
)

// FactoryDroidToolSchema is the parameter schema Factory's Gemini route
// declares for a tool: normalizeSchemaForFactoryDroid(dereferenceJsonSchema(
// toolWireSchema(tool))), the restricted Schema shape the proxy accepts.
func FactoryDroidToolSchema(parameters gjson.Result) json.RawMessage {
	value := dereferenceSchema(postProcessSchema(upgradeSchema(decodeTree(parameters))))
	node, ok := asMap(value)
	if !ok {
		return encodeTree(value)
	}
	out := copyFactoryDroidSchema(node)
	if out == nil {
		out = newJmap()
	}
	settleFactoryDroidMergedUnions(out)
	return encodeTree(out)
}

// factoryDroidAllowedKeys are the schema keywords the CLI copies verbatim,
// in the order it copies them.
var factoryDroidAllowedKeys = []string{"type", "title", "description", "required", "format", "minimum", "maximum",
	"minLength", "maxLength", "pattern", "minItems", "maxItems", "default", "example"}

// factoryDroidAnyOfSiblings are the fields the proxy accepts beside anyOf.
var factoryDroidAnyOfSiblings = map[string]bool{"title": true, "description": true, "default": true, "example": true}

// factoryDroidStricterBound keeps the tighter of two bounds in a conjunction.
var factoryDroidStricterBound = map[string]func(a, b float64) float64{
	"minimum": math.Max, "minLength": math.Max, "minItems": math.Max,
	"maximum": math.Min, "maxLength": math.Min, "maxItems": math.Min,
}

// jsStrictEqual is JavaScript's === over decoded JSON: objects and arrays
// are equal only to themselves.
func jsStrictEqual(left, right any) bool {
	switch a := left.(type) {
	case *jmap:
		b, ok := right.(*jmap)
		return ok && a == b
	case []any:
		b, ok := right.([]any)
		return ok && len(a) > 0 && len(b) > 0 && &a[0] == &b[0] && len(a) == len(b)
	}
	switch right.(type) {
	case *jmap, []any:
		return false
	}
	return left == right
}

func jsIncludes(values []any, value any) bool {
	for _, entry := range values {
		if jsStrictEqual(entry, value) {
			return true
		}
	}
	return false
}

func jsDedupe(values []any) []any {
	out := []any{}
	for index, entry := range values {
		first := index
		for i := 0; i < index; i++ {
			if jsStrictEqual(values[i], entry) {
				first = i
				break
			}
		}
		if first == index {
			out = append(out, entry)
		}
	}
	return out
}

func stringifySchemaValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return string(encodeTree(value))
}

func typeIncludesNull(node *jmap) bool {
	switch typed := node.get("type").(type) {
	case string:
		return typed == "null"
	case []any:
		return jsIncludes(typed, "null")
	}
	return false
}

// assign is Object.assign(target, source).
func assign(target, source *jmap) {
	for _, key := range source.keys {
		target.set(key, source.vals[key])
	}
}

// mergeFactoryDroidSchemas combines two schemas; required unions for a
// conjunction and intersects for alternatives.
func mergeFactoryDroidSchemas(left, right *jmap, union bool) *jmap {
	merged := left.clone()
	for _, key := range right.keys {
		value := right.vals[key]
		switch {
		case key == "properties":
			leftProperties, _ := asMap(merged.get("properties"))
			if leftProperties == nil {
				leftProperties = newJmap()
			}
			rightProperties, _ := asMap(value)
			if rightProperties == nil {
				rightProperties = newJmap()
			}
			properties := leftProperties.clone()
			for _, name := range rightProperties.keys {
				schema := rightProperties.vals[name]
				leftSchema, leftIsMap := asMap(leftProperties.get(name))
				rightSchema, rightIsMap := asMap(schema)
				if leftIsMap && rightIsMap {
					properties.set(name, mergeFactoryDroidSchemas(leftSchema, rightSchema, union))
				} else {
					properties.set(name, schema)
				}
			}
			merged.set("properties", properties)
		case key == "required":
			// Combined below: an intersection must also see a missing right side.
		case union && factoryDroidStricterBound[key] != nil && isNumber(value):
			if current, ok := merged.get(key).(float64); ok {
				merged.set(key, factoryDroidStricterBound[key](current, value.(float64)))
			} else {
				merged.set(key, value)
			}
		case key == "enum" && isArray(left.get("enum")) && isArray(value):
			merged.set("enum", jsDedupe(append(append([]any{}, left.get("enum").([]any)...), value.([]any)...)))
		case union && key == "anyOf" && isArray(merged.get("anyOf")) && isArray(value):
			// Two retained unions in one conjunction keep every compatible pairing.
			if pairs := intersectFactoryDroidUnionBranches(mapsOf(merged.get("anyOf").([]any)), mapsOf(value.([]any))); len(pairs) > 0 {
				merged.set("anyOf", pairs)
			}
		case !merged.has(key):
			merged.set(key, value)
		}
	}
	leftRequired, _ := left.get("required").([]any)
	rightRequired, _ := right.get("required").([]any)
	if union {
		if isArray(left.get("required")) || isArray(right.get("required")) {
			merged.set("required", jsDedupe(append(append([]any{}, leftRequired...), rightRequired...)))
		}
	} else {
		shared := []any{}
		for _, entry := range leftRequired {
			if jsIncludes(rightRequired, entry) {
				shared = append(shared, entry)
			}
		}
		shared = jsDedupe(shared)
		if len(shared) > 0 {
			merged.set("required", shared)
		} else {
			merged.del("required")
		}
	}
	return merged
}

func isNumber(value any) bool { _, ok := value.(float64); return ok }
func isArray(value any) bool  { _, ok := value.([]any); return ok }

// mapsOf keeps the object branches of a union (non-objects never merge).
func mapsOf(values []any) []*jmap {
	out := make([]*jmap, 0, len(values))
	for _, value := range values {
		if node, ok := asMap(value); ok {
			out = append(out, node)
		}
	}
	return out
}

// intersectFactoryDroidUnionBranches is the branch-wise conjunction of two
// unions: each same-typed (or untyped) pair merged, enums intersected.
func intersectFactoryDroidUnionBranches(left, right []*jmap) []any {
	pairs := []any{}
	for _, a := range left {
		for _, b := range right {
			if a.has("type") && b.has("type") && !jsStrictEqual(a.get("type"), b.get("type")) {
				continue
			}
			pair := mergeFactoryDroidSchemas(a, b, true)
			if aEnum, ok := a.get("enum").([]any); ok {
				if bEnum, isArr := b.get("enum").([]any); isArr {
					allowed := []any{}
					for _, value := range aEnum {
						if jsIncludes(bEnum, value) {
							allowed = append(allowed, value)
						}
					}
					if len(allowed) == 0 {
						continue
					}
					pair.set("enum", allowed)
				}
			}
			pairs = append(pairs, pair)
		}
	}
	return pairs
}

// withoutUndefinedRequired drops required names the schema's own properties
// lack: the proxy 400s on them.
func withoutUndefinedRequired(branch *jmap) *jmap {
	required, ok := branch.get("required").([]any)
	if !ok {
		return branch
	}
	properties, _ := asMap(branch.get("properties"))
	kept := []any{}
	for _, name := range required {
		if text, isString := name.(string); isString && properties != nil && properties.has(text) {
			kept = append(kept, name)
		}
	}
	rest := branch.clone()
	rest.del("required")
	if len(kept) > 0 {
		rest.set("required", kept)
	}
	return rest
}

func onlyAnyOfSiblings(node *jmap) bool {
	for _, key := range node.keys {
		if !factoryDroidAnyOfSiblings[key] {
			return false
		}
	}
	return true
}

// settleFactoryDroidUnion folds a union's non-null branches into out.
func settleFactoryDroidUnion(out *jmap, branches []*jmap, nullable bool) {
	nonNull := branches
	mixedTypes := true
	types := map[string]bool{}
	for _, branch := range nonNull {
		kind, ok := branch.get("type").(string)
		if !ok {
			mixedTypes = false
			break
		}
		types[kind] = true
	}
	mixedTypes = mixedTypes && len(types) > 1
	if mixedTypes && onlyAnyOfSiblings(out) {
		kept := make([]any, 0, len(nonNull))
		for _, branch := range nonNull {
			kept = append(kept, withoutUndefinedRequired(branch))
		}
		out.set("anyOf", kept)
		if nullable {
			out.set("nullable", true)
		}
		return
	}
	if mixedTypes {
		// Branches of different types never merge into one Schema: keep only
		// the branches of the node's own type, else of the first branch's.
		var ownType any
		if list, ok := out.get("type").([]any); ok {
			for _, entry := range list {
				if text, isString := entry.(string); isString && text != "null" {
					ownType = text
					break
				}
			}
		} else if out.has("type") && out.get("type") != nil {
			ownType = out.get("type")
		} else if _, ok := asMap(out.get("properties")); ok {
			ownType = "object"
		}
		if ownType == nil {
			ownType = nonNull[0].get("type")
		}
		filtered := []*jmap{}
		for _, branch := range nonNull {
			if jsStrictEqual(branch.get("type"), ownType) {
				filtered = append(filtered, branch)
			}
		}
		nonNull = filtered
	} else if kind, ok := out.get("type").(string); ok {
		// An untyped branch keeps the union unmixed, but a branch of another
		// type still must not lend its fields to this node.
		compatible := []*jmap{}
		for _, branch := range nonNull {
			if !branch.has("type") || jsStrictEqual(branch.get("type"), kind) {
				compatible = append(compatible, branch)
			}
		}
		if len(compatible) > 0 {
			nonNull = compatible
		}
	}
	var collapsed *jmap
	for _, branch := range nonNull {
		if collapsed == nil {
			collapsed = branch.clone()
		} else {
			collapsed = mergeFactoryDroidSchemas(collapsed, branch, false)
		}
	}
	if collapsed == nil {
		return
	}
	collapsed.set("nullable", nullable)
	assign(out, mergeFactoryDroidSchemas(out, collapsed, true))
	// A pattern cannot be conjoined; the union's own, narrower one wins.
	if pattern, ok := collapsed.get("pattern").(string); ok {
		out.set("pattern", pattern)
	}
	// Narrowing can drop the branch that defined a required property.
	if mixedTypes && isArray(out.get("required")) {
		if kept, ok := withoutUndefinedRequired(out).get("required").([]any); ok {
			out.set("required", kept)
		} else {
			out.del("required")
		}
	}
}

// copyFactoryDroidSchema projects one schema node onto the allowed fields.
func copyFactoryDroidSchema(node *jmap) *jmap {
	out := newJmap()
	for _, key := range factoryDroidAllowedKeys {
		if node.has(key) {
			out.set(key, node.get(key))
		}
	}
	if node.has("const") {
		out.set("enum", []any{stringifySchemaValue(node.get("const"))})
	}
	if values, ok := node.get("enum").([]any); ok {
		enum := make([]any, 0, len(values))
		for _, value := range values {
			enum = append(enum, stringifySchemaValue(value))
		}
		out.set("enum", enum)
	}
	if properties, ok := asMap(node.get("properties")); ok {
		copied := newJmap()
		for _, name := range properties.keys {
			if schema, isMap := asMap(properties.vals[name]); isMap {
				copied.set(name, copyFactoryDroidSchema(schema))
			}
		}
		out.set("properties", copied)
	}
	if items, ok := asMap(node.get("items")); ok {
		out.set("items", copyFactoryDroidSchema(items))
	}
	// allOf merges into this node's own copy first, so the union handling
	// below sees every field that will sit beside it.
	if branches, ok := node.get("allOf").([]any); ok {
		for _, branch := range branches {
			if schema, isMap := asMap(branch); isMap {
				assign(out, mergeFactoryDroidSchemas(out, copyFactoryDroidSchema(schema), true))
			}
		}
	}
	unionKey := ""
	if isArray(node.get("anyOf")) {
		unionKey = "anyOf"
	} else if isArray(node.get("oneOf")) {
		unionKey = "oneOf"
	}
	if unionKey != "" {
		branches := []*jmap{}
		for _, branch := range node.get(unionKey).([]any) {
			if schema, isMap := asMap(branch); isMap {
				branches = append(branches, copyFactoryDroidSchema(schema))
			}
		}
		nonNull := []*jmap{}
		for _, branch := range branches {
			if !typeIncludesNull(branch) {
				nonNull = append(nonNull, branch)
			}
		}
		settleFactoryDroidUnion(out, nonNull, len(nonNull) < len(branches))
	}
	// The Schema proto takes a single string type: a null member becomes
	// nullable, the first non-null type wins. A list naming no type leaves
	// type undefined: absent on the wire, yet present, so none is inferred.
	typeUndefined := false
	if list, ok := out.get("type").([]any); ok {
		var types, nonNullTypes []string
		for _, entry := range list {
			if text, isString := entry.(string); isString {
				types = append(types, text)
				if text != "null" {
					nonNullTypes = append(nonNullTypes, text)
				}
			}
		}
		for _, kind := range types {
			if kind == "null" {
				out.set("nullable", true)
				break
			}
		}
		switch {
		case len(nonNullTypes) > 0:
			out.set("type", nonNullTypes[0])
		case len(types) > 0:
			out.set("type", types[0])
		default:
			out.del("type")
			typeUndefined = true
		}
	}
	// The proto requires a type; infer one the way the CLI's copier does.
	if !out.has("type") && !typeUndefined {
		if _, ok := asMap(out.get("properties")); ok {
			out.set("type", "object")
		} else if out.has("items") {
			out.set("type", "array")
		} else if out.has("enum") {
			out.set("type", "string")
		}
	}
	return out
}

// settleFactoryDroidMergedUnions settles a kept anyOf that gained structural
// siblings when an enclosing allOf merged into it, at any depth.
func settleFactoryDroidMergedUnions(node *jmap) {
	if properties, ok := asMap(node.get("properties")); ok {
		for _, name := range properties.keys {
			if schema, isMap := asMap(properties.vals[name]); isMap {
				settleFactoryDroidMergedUnions(schema)
			}
		}
	}
	if items, ok := asMap(node.get("items")); ok {
		settleFactoryDroidMergedUnions(items)
	}
	anyOf, ok := node.get("anyOf").([]any)
	if !ok {
		return
	}
	for _, branch := range anyOf {
		if schema, isMap := asMap(branch); isMap {
			settleFactoryDroidMergedUnions(schema)
		}
	}
	// A union left with one branch (an allOf intersection) is that branch.
	single := len(anyOf) == 1
	nullable, _ := node.get("nullable").(bool)
	rest := node.clone()
	rest.del("anyOf")
	rest.del("nullable")
	if !single && onlyAnyOfSiblings(rest) {
		return
	}
	node.del("anyOf")
	node.del("nullable")
	settleFactoryDroidUnion(node, mapsOf(anyOf), nullable)
	if _, isMap := asMap(node.get("properties")); !node.has("type") && isMap {
		node.set("type", "object")
	}
	// The collapse merges branches, which can leave new unions beside fields.
	settleFactoryDroidMergedUnions(node)
}
