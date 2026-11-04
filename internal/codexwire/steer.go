package codexwire

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// SteerFrame is the response.steer frame NeoPi's CodexSteerPump sends. Only
// plain user messages may steer (toSteerInputItems).
func SteerFrame(previousResponseID string, input []json.RawMessage) ([]byte, error) {
	if previousResponseID == "" {
		return nil, fmt.Errorf("codex steer: previous_response_id is required")
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("codex steer: input is required")
	}
	items := make([]json.RawMessage, len(input))
	for i, raw := range input {
		item := gjson.ParseBytes(raw)
		if item.Get("role").String() != "user" || (item.Get("type").Exists() && item.Get("type").Type != gjson.Null && item.Get("type").String() != "message") {
			return nil, fmt.Errorf("codex steer: only user messages can steer")
		}
		items[i] = jsjson.Normalize(raw)
	}
	return jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("response.steer")},
		jsjson.Field{Name: "previous_response_id", Value: jsjson.Marshal(previousResponseID)},
		jsjson.Field{Name: "input", Value: jsjson.Marshal(items)},
	), nil
}

// SteerPlan is how the request after a steered response continues.
type SteerPlan int

const (
	// SteerDiscard: the request cannot line up with the server's queue; replay in full on a fresh socket.
	SteerDiscard SteerPlan = iota
	// SteerAttach: the server continues on its own; read its successor and send nothing.
	SteerAttach
	// SteerCreate: the server awaits tool output; send only the remaining input.
	SteerCreate
)

// PlanSteeredRequest is planSteeredRequest. delta is the chained input, or nil
// when the chain broke; it returns the input to send for SteerCreate.
func PlanSteeredRequest(delta []json.RawMessage, steering []json.RawMessage) (SteerPlan, []json.RawMessage) {
	if delta == nil {
		return SteerDiscard, nil
	}
	var rest []json.RawMessage
	next := 0
	for _, item := range delta {
		if next < len(steering) && steerItemKey(item) == steerItemKey(steering[next]) {
			next++
			continue
		}
		rest = append(rest, item)
	}
	if next < len(steering) {
		return SteerDiscard, nil
	}
	if len(rest) == 0 {
		return SteerAttach, nil
	}
	for _, item := range rest {
		if itemType := gjson.GetBytes(item, "type"); itemType.Type == gjson.String && strings.HasSuffix(itemType.String(), "_output") {
			return SteerCreate, rest
		}
	}
	return SteerDiscard, nil
}

// steerItemKey identifies a user message by role and content, ignoring image detail.
func steerItemKey(raw json.RawMessage) string {
	item := gjson.ParseBytes(raw)
	if item.Get("role").String() != "user" {
		key := item.Get("call_id").String()
		if key == "" {
			key = item.Get("id").String()
		}
		return "\x00" + item.Get("type").String() + ":" + key
	}
	content := item.Get("content")
	if content.Type == gjson.String {
		return string(jsjson.Marshal([]json.RawMessage{jsjson.Object(
			jsjson.Field{Name: "type", Value: jsjson.Marshal("input_text")},
			jsjson.Field{Name: "text", Value: jsjson.Marshal(content.String())},
		)}))
	}
	if !content.IsArray() {
		return string(jsjson.Normalize([]byte(content.Raw)))
	}
	var parts []json.RawMessage
	for _, part := range content.Array() {
		if !part.IsObject() {
			parts = append(parts, jsjson.Normalize([]byte(part.Raw)))
			continue
		}
		obj := parseObject([]byte(part.Raw))
		obj.del("detail")
		parts = append(parts, obj.bytes())
	}
	return string(jsjson.Marshal(parts))
}
