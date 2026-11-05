package openaiwire

import (
	"encoding/json"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// nonVisionImagePlaceholder is NON_VISION_IMAGE_PLACEHOLDER (vision-guard.ts).
const nonVisionImagePlaceholder = "[image omitted: model does not support vision]"

// omitUnsupportedImages is convertResponsesInputContent's vision guard.
// Native user/developer provider payloads bypass content conversion; constructed
// content and tool outputs still replace unsupported images with a placeholder.
func omitUnsupportedImages(items []json.RawMessage, model *registry.NeoPiModel, nativeUserHistory bool) []json.RawMessage {
	if model == nil || slices.Contains(model.Input, "image") {
		return items
	}
	out := make([]json.RawMessage, len(items))
	for i, item := range items {
		out[i] = item
		switch gjson.GetBytes(item, "type").String() {
		case "", "message":
			role := gjson.GetBytes(item, "role").String()
			if nativeUserHistory && (role == "user" || role == "developer") {
				continue
			}
			if content := gjson.GetBytes(item, "content"); content.IsArray() {
				if replaced, ok := withoutImages(content); ok {
					if next, err := sjson.SetRawBytes(item, "content", replaced); err == nil {
						out[i] = next
					}
				}
			}
		case "function_call_output", "custom_tool_call_output":
			if output := gjson.GetBytes(item, "output"); output.IsArray() {
				if replaced, ok := withoutImages(output); ok {
					if next, err := sjson.SetRawBytes(item, "output", replaced); err == nil {
						out[i] = next
					}
				}
			}
		}
	}
	return out
}

// withoutImages drops the `input_image` parts of a content list and appends the
// placeholder once; ok is false when the list had none.
func withoutImages(content gjson.Result) (json.RawMessage, bool) {
	var kept []json.RawMessage
	omitted := false
	for _, part := range content.Array() {
		if part.Get("type").String() == "input_image" {
			omitted = true
			continue
		}
		kept = append(kept, json.RawMessage(part.Raw))
	}
	if !omitted {
		return nil, false
	}
	kept = append(kept, jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("input_text")},
		jsjson.Field{Name: "text", Value: jsjson.Marshal(nonVisionImagePlaceholder)},
	))
	return jsjson.Marshal(kept), true
}

// NormalizeConstructedUserMessages removes the message type introduced by
// protocol translation for constructed user turns. Native Responses history
// must not pass through this conversion.
func NormalizeConstructedUserMessages(items []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(items))
	for i, item := range items {
		out[i] = item
		if gjson.GetBytes(item, "type").String() == "message" && gjson.GetBytes(item, "role").String() == "user" && !gjson.GetBytes(item, "id").Exists() {
			if next, err := sjson.DeleteBytes(item, "type"); err == nil {
				out[i] = next
			}
		}
	}
	return out
}
