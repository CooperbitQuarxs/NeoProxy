package codexwire

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// sanitizeAssistantReplay is sanitizeOpenAIResponsesAssistantHistoryItemsForReplay
// (packages/ai/src/utils.ts). It returns nil for a response with no
// replayable assistant output.
func sanitizeAssistantReplay(raw []json.RawMessage, supportsImageDetailOriginal bool) []object {
	items := make([]object, len(raw))
	for i, item := range raw {
		items[i] = parseObject(item)
	}
	items = dropMalformedToolCalls(items)
	linked := computerLinkedReasoning(items, false)
	var sanitized []object
	for index, item := range items {
		if replayed := sanitizeReplayItem(item, supportsImageDetailOriginal, linked[index]); replayed != nil {
			sanitized = append(sanitized, replayed)
		}
	}
	for _, item := range sanitized {
		switch item.get("type").String() {
		case "message", "function_call", "custom_tool_call", "compaction", "compaction_summary":
			item.del("status")
		}
	}
	replayable, empty := false, false
	for _, item := range sanitized {
		if item.get("type").String() == "reasoning" {
			continue
		}
		if isEmptyAssistantMessage(item) {
			empty = true
			continue
		}
		replayable = true
	}
	if !replayable {
		return nil
	}
	if !empty {
		return sanitized
	}
	return dropEmptyAssistantMessages(sanitized)
}

// SanitizeHistoryReplay is sanitizeOpenAIResponsesHistoryItemsForReplay for a
// model without native computer use: malformed tool calls are dropped, each
// item is reduced to its replayable shape, and output-only statuses are
// stripped. Items are re-serialized in JSON.stringify form.
func SanitizeHistoryReplay(raw []json.RawMessage, supportsImageDetailOriginal bool) []json.RawMessage {
	items := make([]object, len(raw))
	for i, item := range raw {
		items[i] = parseObject(item)
	}
	items = dropMalformedToolCalls(items)
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		replayed := sanitizeReplayItem(item, supportsImageDetailOriginal, false)
		if replayed == nil {
			continue
		}
		switch replayed.get("type").String() {
		case "message", "function_call", "custom_tool_call", "compaction", "compaction_summary":
			replayed.del("status")
		}
		out = append(out, replayed.bytes())
	}
	return out
}

func itemType(item object) (string, bool) {
	kind := item.get("type")
	return kind.String(), kind.Type == gjson.String
}

// isClientInputBoundary is isOpenAIResponsesClientInputBoundary.
func isClientInputBoundary(item object) bool {
	kind, typed := itemType(item)
	role := item.get("role")
	if kind == "message" {
		return role.String() != "assistant"
	}
	if !item.has("type") && role.Type == gjson.String {
		return role.String() != "assistant"
	}
	if !typed {
		return false
	}
	switch kind {
	case "input_text", "input_image", "input_file", "input_audio", "function_call_output", "custom_tool_call_output",
		"computer_call_output", "local_shell_call_output", "shell_call_output", "apply_patch_call_output",
		"mcp_approval_response", "compaction", "compaction_summary", "compaction_trigger", "item_reference":
		return true
	case "additional_tools":
		return role.String() != "assistant"
	case "tool_search_output":
		return item.get("execution").String() != "server"
	}
	return false
}

// isMalformedToolCallName is transform-messages.ts isMalformedToolCallName.
func isMalformedToolCallName(name gjson.Result) bool {
	if name.Type != gjson.String || name.String() == "" {
		return true
	}
	text := name.String()
	if len(utf16.Encode([]rune(text))) > 128 {
		return true
	}
	return strings.IndexFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' || unicode.Is(unicode.Cc, r) }) >= 0
}

func isMalformedToolCall(item object) bool {
	kind, _ := itemType(item)
	return (kind == "function_call" || kind == "custom_tool_call") && isMalformedToolCallName(item.get("name"))
}

func isToolResult(kind string) bool {
	switch kind {
	case "function_call_output", "custom_tool_call_output", "computer_call_output", "local_shell_call_output", "shell_call_output", "apply_patch_call_output":
		return true
	}
	return false
}

// dropMalformedToolCalls is dropMalformedOpenAIResponsesToolCalls: each call
// with a malformed name and only its answering output in the same window.
func dropMalformedToolCalls(items []object) []object {
	malformed := false
	for _, item := range items {
		if isMalformedToolCall(item) {
			malformed = true
			break
		}
	}
	if !malformed {
		return items
	}
	queues := map[string][]bool{}
	var kept []object
	for _, item := range items {
		kind, _ := itemType(item)
		callID := item.get("call_id")
		switch kind {
		case "function_call", "custom_tool_call":
			bad := isMalformedToolCall(item)
			if callID.Type == gjson.String {
				key := strings.TrimSuffix(kind, "_call") + "\x00" + callID.String()
				queues[key] = append(queues[key], bad)
			}
			if !bad {
				kept = append(kept, item)
			}
			continue
		case "function_call_output", "custom_tool_call_output":
			if callID.Type == gjson.String {
				key := strings.TrimSuffix(kind, "_call_output") + "\x00" + callID.String()
				if queue := queues[key]; len(queue) > 0 {
					drop := queue[0]
					if queues[key] = queue[1:]; len(queues[key]) == 0 {
						delete(queues, key)
					}
					if drop {
						continue
					}
				}
			}
			kept = append(kept, item)
			continue
		}
		if !isToolResult(kind) && isClientInputBoundary(item) {
			clear(queues)
		}
		kept = append(kept, item)
	}
	return kept
}

// computerLinkedReasoning is collectOpenAIResponsesComputerLinkedReasoningItems,
// by item index.
func computerLinkedReasoning(items []object, requireLaterOutput bool) map[int]bool {
	var withLaterOutput map[int]bool
	if requireLaterOutput {
		withLaterOutput = map[int]bool{}
		later := map[string]bool{}
		for index := len(items) - 1; index >= 0; index-- {
			item := items[index]
			kind, _ := itemType(item)
			callID := item.get("call_id")
			if kind == "computer_call_output" && callID.Type == gjson.String {
				later[callID.String()] = true
			} else if kind == "computer_call" && item.get("id").Type == gjson.String && callID.Type == gjson.String && later[callID.String()] {
				withLaterOutput[index] = true
			}
		}
	}
	linked := map[int]bool{}
	var reasoning []int
	for index, item := range items {
		kind, _ := itemType(item)
		switch {
		case isClientInputBoundary(item):
			reasoning = reasoning[:0]
		case kind == "reasoning":
			reasoning = append(reasoning, index)
		case kind == "computer_call" && item.get("id").Type == gjson.String && (withLaterOutput == nil || withLaterOutput[index]):
			for _, reasoningIndex := range reasoning {
				linked[reasoningIndex] = true
			}
		}
	}
	return linked
}

// sanitizeReplayItem is sanitizeOpenAIResponsesHistoryItemForReplay.
func sanitizeReplayItem(item object, supportsImageDetailOriginal, preserveReasoningID bool) object {
	if isMalformedToolCall(item) {
		return nil
	}
	kind, _ := itemType(item)
	switch kind {
	case "function_call":
		args := item.get("arguments")
		if args.Type != gjson.String || strings.TrimSpace(args.String()) == "" || !json.Valid([]byte(args.String())) {
			return nil
		}
	case "item_reference":
		return nil
	case "image_generation_call":
		id, result := item.get("id"), item.get("result")
		if id.Type != gjson.String || result.Type != gjson.String || result.String() == "" {
			return nil
		}
		return object{
			{Name: "id", Value: jsjson.Marshal(truncateItemID(id.String(), "ig"))},
			{Name: "type", Value: jsjson.Marshal("image_generation_call")},
			{Name: "status", Value: jsjson.Marshal("completed")},
			{Name: "result", Value: json.RawMessage(result.Raw)},
		}
	case "reasoning":
		out := object{{Name: "type", Value: jsjson.Marshal("reasoning")}}
		if id := item.get("id"); preserveReasoningID && id.Type == gjson.String {
			out.set("id", json.RawMessage(id.Raw))
		}
		if summary := item.get("summary"); summary.IsArray() {
			out.set("summary", json.RawMessage(summary.Raw))
		}
		if content := item.get("content"); content.IsArray() {
			out.set("content", json.RawMessage(content.Raw))
		}
		if encrypted := item.get("encrypted_content"); encrypted.Type == gjson.String || encrypted.Type == gjson.Null && item.has("encrypted_content") {
			out.set("encrypted_content", json.RawMessage(encrypted.Raw))
		}
		return out
	}
	out := append(object(nil), item...)
	if !(kind == "computer_call" && item.get("id").Type == gjson.String) {
		out.del("id")
	}
	return clampImageDetail(out, supportsImageDetailOriginal)
}

func truncateItemID(id, prefix string) string {
	if len(utf16.Encode([]rune(id))) <= 64 {
		return id
	}
	return prefix + "_" + bunHash36(id)
}

// clampImageDetail is clampReplayItemImageDetail.
func clampImageDetail(item object, supportsOriginal bool) object {
	if supportsOriginal {
		return item
	}
	kind, _ := itemType(item)
	if kind == "input_image" && item.get("detail").String() == "original" {
		item.set("detail", jsjson.Marshal("auto"))
		return item
	}
	content := item.get("content")
	if kind != "message" || !content.IsArray() {
		return item
	}
	changed := false
	var parts []json.RawMessage
	for _, part := range content.Array() {
		if part.IsObject() && part.Get("type").String() == "input_image" && part.Get("detail").String() == "original" {
			clamped := parseObject([]byte(part.Raw))
			clamped.set("detail", jsjson.Marshal("auto"))
			parts = append(parts, clamped.bytes())
			changed = true
			continue
		}
		parts = append(parts, json.RawMessage(part.Raw))
	}
	if changed {
		item.set("content", jsjson.Marshal(parts))
	}
	return item
}

// dropEmptyAssistantMessages is dropEmptyAssistantMessagesForReplay.
func dropEmptyAssistantMessages(items []object) []object {
	var kept, pending []object
	removed := false
	for _, item := range items {
		if isClientInputBoundary(item) {
			pending, removed = nil, false
			kept = append(kept, item)
			continue
		}
		if kind, _ := itemType(item); kind == "reasoning" {
			if removed {
				pending = nil
			}
			removed = false
			pending = append(pending, item)
			continue
		}
		if isEmptyAssistantMessage(item) {
			removed = true
			continue
		}
		kept = append(kept, pending...)
		pending, removed = nil, false
		kept = append(kept, item)
	}
	return kept
}
