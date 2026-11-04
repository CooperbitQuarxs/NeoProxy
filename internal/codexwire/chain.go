package codexwire

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// transportState is CodexWebSocketSessionState: the append baseline, the
// x-models-etag the backend last echoed, and the session's socket.
type transportState struct {
	canAppend         bool
	lastRequest       object
	lastResponseID    string
	lastResponseItems []object
	modelsETag        string

	socket              *Socket
	disableWebsocket    bool
	fallbackCount       int
	accepted            *acceptedSteering
	steeringUnsupported bool
}

// acceptedSteering is steering the server accepted for the last response,
// valid only on the socket that accepted it.
type acceptedSteering struct {
	socket *Socket
	steers []acceptedSteer
}

type acceptedSteer struct {
	id    string
	items []json.RawMessage
}

func (state *transportState) resetAppend() {
	state.canAppend = false
	state.lastRequest = nil
	state.lastResponseID = ""
	state.lastResponseItems = nil
}

func jsonValue(raw json.RawMessage) any {
	var value any
	if len(raw) == 0 {
		return nil
	}
	_ = json.Unmarshal(raw, &value)
	return value
}

// deepEqualsWithout is NeoPi's key-order-insensitive deepEqualsWithout.
func deepEqualsWithout(a, b object, omit map[string]bool) bool {
	for _, field := range a {
		if omit[field.Name] {
			continue
		}
		other := b.index(field.Name)
		if other < 0 || !reflect.DeepEqual(jsonValue(field.Value), jsonValue(b[other].Value)) {
			return false
		}
	}
	for _, field := range b {
		if !omit[field.Name] && !a.has(field.Name) {
			return false
		}
	}
	return true
}

var (
	chainTopLevelExclude = map[string]bool{"input": true, "client_metadata": true, "service_tier": true}
	itemLifecycleExclude = map[string]bool{"status": true}
	replayItemExclude    = map[string]bool{"status": true, "id": true}
)

// deltaInput is buildResponsesDeltaInput: the items appended beyond the
// previous request plus its response, or nil when the chain must break.
func deltaInput(previous object, previousItems []object, current object) []object {
	if previous == nil || !previous.get("input").IsArray() || !current.get("input").IsArray() {
		return nil
	}
	if !deepEqualsWithout(previous, current, chainTopLevelExclude) {
		return nil
	}
	prior := append(parseItems(previous.get("input")), previousItems...)
	items := parseItems(current.get("input"))
	if len(items) <= len(prior) {
		return nil
	}
	for index, item := range prior {
		omit := itemLifecycleExclude
		switch item.get("type").String() {
		case "message", "function_call", "custom_tool_call":
			omit = replayItemExclude
		}
		if !deepEqualsWithout(item, items[index], omit) {
			return nil
		}
	}
	return items[len(prior):]
}

func isEmptyAssistantMessage(item object) bool {
	if item.get("type").String() != "message" || item.get("role").String() != "assistant" {
		return false
	}
	content := item.get("content")
	if content.Type == gjson.String {
		return strings.TrimSpace(content.String()) == ""
	}
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "output_text":
			if strings.TrimSpace(part.Get("text").String()) != "" {
				return false
			}
		case "refusal":
			if strings.TrimSpace(part.Get("refusal").String()) != "" {
				return false
			}
		}
	}
	return true
}

// transport returns the session's transport state; callers hold s.mu.
func (s *Sessions) transport(key string) *transportState {
	if s.transports == nil {
		s.transports = map[string]*transportState{}
	}
	state := s.transports[key]
	if state == nil {
		state = &transportState{}
		s.transports[key] = state
	}
	return state
}

// chainedBody is buildCodexChainedRequestBody: the request as a delta over
// previous_response_id when the append baseline still prefixes it. A failed
// prefix check breaks the chain. Callers hold s.mu.
func chainedBody(state *transportState, body object) object {
	if state == nil || !state.canAppend {
		return body
	}
	ultrafast := func(o object) bool { return o.get("service_tier").String() == "ultrafast" }
	if ultrafast(state.lastRequest) != ultrafast(body) {
		return body
	}
	if delta := deltaInput(state.lastRequest, state.lastResponseItems, body); len(delta) > 0 && state.lastResponseID != "" {
		chained := append(object(nil), body...)
		chained.set("previous_response_id", jsjson.Marshal(state.lastResponseID))
		chained.set("input", itemsJSON(delta))
		return chained
	}
	state.resetAppend()
	state.modelsETag = ""
	return body
}

// websocketFrame wraps a chained body as response.create. Connection-scoped
// values that HTTP would carry as headers ride in client_metadata. Callers hold s.mu.
func websocketFrame(request *Request, chained object) object {
	metadata := parseObject([]byte(chained.get("client_metadata").Raw))
	if request.lite {
		metadata.set("ws_request_header_x_openai_internal_codex_responses_lite", jsjson.Marshal("true"))
	}
	if turnState := request.session.turnStates[request.SessionKey]; turnState != "" {
		metadata.set("x-codex-turn-state", jsjson.Marshal(turnState))
	}
	frame := object{{Name: "type", Value: jsjson.Marshal("response.create")}}
	for _, field := range chained {
		frame.set(field.Name, field.Value)
	}
	frame.set("client_metadata", metadata.bytes())
	return frame
}
