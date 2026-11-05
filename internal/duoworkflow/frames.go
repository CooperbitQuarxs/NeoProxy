package duoworkflow

import (
	"fmt"
	"math"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
)

// The workflow socket's frames are JSON objects GitLab's Workhorse relays
// from the Duo Workflow Service. These helpers read them as NeoPi's
// getRecord / getRecordString do: an array reads as an object with no named
// properties, and a number reads as its JavaScript spelling.

// actionNames are GITLAB_DUO_WORKFLOW_ACTION_NAMES.
var actionNames = []string{"runMCPTool", "run_mcp_tool"}

// contextAgentPriority is GITLAB_DUO_WORKFLOW_CONTEXT_AGENT_PRIORITY.
var contextAgentPriority = []string{"Chat Agent", "context_builder"}

// parseFrame is parseGitLabDuoWorkflowSocketData: nil for a frame that is no
// JSON object.
func parseFrame(data string) *cursorwire.JSONObject {
	value, ok := cursorwire.ParseJSON(data)
	if !ok {
		return nil
	}
	return asRecord(value)
}

func asRecord(value any) *cursorwire.JSONObject {
	switch v := value.(type) {
	case *cursorwire.JSONObject:
		return v
	case []any:
		return &cursorwire.JSONObject{}
	}
	return nil
}

// getRecord is getRecord: the property when it is an object.
func getRecord(object *cursorwire.JSONObject, key string) *cursorwire.JSONObject {
	if object == nil {
		return nil
	}
	value, _ := object.Get(key)
	return asRecord(value)
}

// getRecordString is getRecordString: a string or number property, as text.
func getRecordString(object *cursorwire.JSONObject, key string) (string, bool) {
	if object == nil {
		return "", false
	}
	value, _ := object.Get(key)
	switch v := value.(type) {
	case string:
		return v, true
	case float64:
		return jsjson.Number(v), true
	}
	return "", false
}

func getNestedRecordString(object *cursorwire.JSONObject, parent, key string) (string, bool) {
	return getRecordString(getRecord(object, parent), key)
}

// firstRecordString is a `??` chain of getRecordString reads.
func firstRecordString(reads ...func() (string, bool)) (string, bool) {
	for _, read := range reads {
		if value, ok := read(); ok {
			return value, true
		}
	}
	return "", false
}

func recordString(object *cursorwire.JSONObject, key string) func() (string, bool) {
	return func() (string, bool) { return getRecordString(object, key) }
}

// stringField is stringField: a string property with non-blank text.
func stringField(object *cursorwire.JSONObject, key string) (string, bool) {
	if object == nil {
		return "", false
	}
	value, _ := object.Get(key)
	text, ok := value.(string)
	if !ok || jsTrim(text) == "" {
		return "", false
	}
	return text, true
}

// numberField is numberField: a finite, non-negative number property.
func numberField(object *cursorwire.JSONObject, key string) (float64, bool) {
	value, _ := object.Get(key)
	number, ok := value.(float64)
	if !ok || math.IsInf(number, 0) || math.IsNaN(number) || number < 0 {
		return 0, false
	}
	return number, true
}

// jsTrim is String.prototype.trim.
func jsTrim(text string) string {
	return strings.TrimFunc(text, isJSSpace)
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// frameStatus is the status a frame carries at its root, in its
// workflowStatus or in its newCheckpoint.
func frameStatus(event *cursorwire.JSONObject) (string, bool) {
	return firstRecordString(
		recordString(event, "status"),
		func() (string, bool) { return getNestedRecordString(event, "workflowStatus", "status") },
		func() (string, bool) { return getNestedRecordString(event, "newCheckpoint", "status") },
	)
}

// Checkpoint entry kinds.
const (
	kindText     = "text"
	kindThinking = "thinking"
	kindBoundary = "boundary"
)

// checkpointEntry is GitLabDuoWorkflowCheckpointEntry: an agent message or
// a request/tool boundary.
type checkpointEntry struct {
	kind       string
	messageKey string
	content    string
}

type contextUsage struct {
	used, window float64
}

// checkpoint is GitLabDuoWorkflowCheckpointContent; contentLength counts
// UTF-16 units, as the checkpoint string's length does.
type checkpoint struct {
	entries       []checkpointEntry
	contentLength int
	contextUsage  *contextUsage
}

// extractCheckpoint is extractGitLabDuoWorkflowCheckpoint.
func extractCheckpoint(event *cursorwire.JSONObject) *checkpoint {
	action := getRecord(event, "action")
	snapshot := getRecord(action, "newCheckpoint")
	if snapshot == nil {
		snapshot = getRecord(event, "newCheckpoint")
	}
	if snapshot == nil {
		snapshot = getRecord(event, "checkpoint")
	}
	if snapshot == nil {
		return nil
	}
	directText, hasDirect := firstRecordString(
		recordString(snapshot, "message"),
		recordString(snapshot, "text"),
		recordString(snapshot, "content"),
		func() (string, bool) { return getNestedRecordString(snapshot, "checkpoint", "message") },
		func() (string, bool) { return getNestedRecordString(snapshot, "checkpoint", "text") },
	)
	usage := extractContextUsage(event, action, snapshot)
	if hasDirect && directText != "" {
		return &checkpoint{
			entries:       []checkpointEntry{{kind: kindText, messageKey: "direct:text", content: directText}},
			contentLength: utf16Length(directText),
			contextUsage:  usage,
		}
	}
	if checkpointJSON, ok := getRecordString(snapshot, "checkpoint"); ok && checkpointJSON != "" {
		if content := extractCheckpointEntries(checkpointJSON); content != nil {
			if usage != nil {
				content.contextUsage = usage
			}
			return content
		}
	}
	if usage != nil {
		return &checkpoint{contextUsage: usage}
	}
	return nil
}

// extractContextUsage is extractGitLabDuoWorkflowContextUsage: the first
// source's agent_context_usage, the chat agent preferred.
func extractContextUsage(sources ...*cursorwire.JSONObject) *contextUsage {
	for _, source := range sources {
		usageMap := getRecord(source, "agent_context_usage")
		if usageMap == nil {
			continue
		}
		for _, preferred := range contextAgentPriority {
			value, _ := usageMap.Get(preferred)
			if usage := agentUsage(value); usage != nil {
				return usage
			}
		}
		for _, key := range usageMap.Keys() {
			value, _ := usageMap.Get(key)
			if usage := agentUsage(value); usage != nil {
				return usage
			}
		}
	}
	return nil
}

func agentUsage(value any) *contextUsage {
	record := asRecord(value)
	if record == nil {
		return nil
	}
	used, ok := numberField(record, "total_tokens")
	if !ok {
		return nil
	}
	window, ok := numberField(record, "max_tokens")
	if !ok || window <= 0 {
		return nil
	}
	return &contextUsage{used: used, window: window}
}

// extractCheckpointEntries is extractGitLabCheckpointEntries over the
// serialized LangGraph checkpoint: ui_chat_log's agent messages (reasoning
// as thinking) and its request/tool boundaries.
func extractCheckpointEntries(checkpointJSON string) *checkpoint {
	parsed, _ := cursorwire.ParseJSON(checkpointJSON)
	channelValues := getRecord(asRecord(parsed), "channel_values")
	if channelValues == nil {
		return nil
	}
	raw, _ := channelValues.Get("ui_chat_log")
	chatLog, ok := raw.([]any)
	if !ok {
		return nil
	}
	content := &checkpoint{contentLength: utf16Length(checkpointJSON), entries: []checkpointEntry{}}
	for index, item := range chatLog {
		entry := asRecord(item)
		if entry == nil {
			continue
		}
		messageType, _ := getRecordString(entry, "message_type")
		switch messageType {
		case "agent":
			text, ok := getRecordString(entry, "content")
			if !ok || text == "" {
				continue
			}
			subType, _ := getRecordString(entry, "message_sub_type")
			reasoning := subType == "reasoning"
			kind, key := kindText, fmt.Sprintf("agent:%d", index)
			if reasoning {
				kind, key = kindThinking, fmt.Sprintf("reasoning:%d", index)
			}
			if messageID, ok := getRecordString(entry, "message_id"); ok && messageID != "" {
				key = "agent:" + messageID
			}
			content.entries = append(content.entries, checkpointEntry{kind: kind, messageKey: key, content: text})
		case "request", "tool":
			content.entries = append(content.entries, checkpointEntry{kind: kindBoundary})
		}
	}
	return content
}

// action is GitLabDuoWorkflowActionDescriptor.
type action struct {
	requestID string
	name      string
	args      *cursorwire.JSONObject
}

// extractAction is extractGitLabDuoWorkflowAction: a wrapped action, or a
// root runMCPTool / run_mcp_tool. An action without a requestID is a
// protocol error: an answer to any other id would be discarded.
func extractAction(event *cursorwire.JSONObject) (*action, error) {
	wrapped := getRecord(event, "action")
	if wrapped == nil {
		wrapped = getRecord(event, "workflowAction")
	}
	if wrapped == nil {
		wrapped = getRecord(event, "toolCall")
	}
	if wrapped != nil {
		if getRecord(wrapped, "newCheckpoint") != nil {
			return nil, nil
		}
		name, ok := firstRecordString(
			recordString(wrapped, "name"),
			recordString(wrapped, "action"),
			recordString(wrapped, "type"),
			recordString(event, "actionName"),
		)
		if !ok || name == "" {
			return nil, nil
		}
		requestID, _ := firstRecordString(
			recordString(wrapped, "requestID"),
			recordString(wrapped, "requestId"),
			recordString(wrapped, "id"),
			recordString(event, "requestID"),
			recordString(event, "requestId"),
		)
		if requestID == "" {
			return nil, missingRequestID(name, wrapped)
		}
		args := getRecord(wrapped, "args")
		if args == nil {
			args = getRecord(wrapped, "arguments")
		}
		if args == nil {
			args = wrapped
		}
		return &action{requestID: requestID, name: name, args: withToolCallID(args, requestID)}, nil
	}
	for _, name := range actionNames {
		args := getRecord(event, name)
		if args == nil {
			continue
		}
		requestID, _ := firstRecordString(recordString(event, "requestID"), recordString(event, "requestId"))
		if requestID == "" {
			return nil, missingRequestID(name, event)
		}
		return &action{requestID: requestID, name: name, args: withToolCallID(args, requestID)}, nil
	}
	return nil, nil
}

func missingRequestID(name string, source *cursorwire.JSONObject) error {
	keys := source.Keys()
	if len(keys) > 20 {
		keys = keys[:20]
	}
	return &Error{Message: fmt.Sprintf("GitLab Duo Workflow action %q missing requestID (keys: %s)", name, strings.Join(keys, ", "))}
}

// withToolCallID is withGitLabDuoWorkflowToolCallId: the arguments with the
// request id as their tool call id unless they name one.
func withToolCallID(args *cursorwire.JSONObject, requestID string) *cursorwire.JSONObject {
	for _, key := range []string{"toolCallId", "tool_call_id"} {
		if value, _ := args.Get(key); value != nil {
			if _, ok := value.(string); ok {
				return args
			}
		}
	}
	out := copyObject(args)
	out.Set("toolCallId", requestID)
	out.Set("tool_call_id", requestID)
	return out
}

func copyObject(object *cursorwire.JSONObject) *cursorwire.JSONObject {
	out := &cursorwire.JSONObject{}
	for _, key := range object.Keys() {
		value, _ := object.Get(key)
		out.Set(key, value)
	}
	return out
}

// toolCall is buildGitLabDuoWorkflowActionToolCall: an MCP action as the
// client tool it names, under the request id.
func (a *action) toolCall() *cursorwire.OutBlock {
	name, arguments := a.name, copyObject(a.args)
	switch a.name {
	case "runMCPTool", "run_mcp_tool":
		name, arguments = mcpToolCall(a.args)
	}
	return &cursorwire.OutBlock{Type: "toolCall", ID: a.requestID, Name: name, Arguments: arguments}
}

// mcpToolCall is mapGitLabDuoWorkflowMcpToolCall.
func mcpToolCall(args *cursorwire.JSONObject) (string, *cursorwire.JSONObject) {
	rawName, ok := stringField(args, "toolName")
	if !ok {
		rawName, ok = stringField(args, "tool_name")
	}
	if !ok {
		rawName, _ = stringField(args, "name")
	}
	toolName := strings.TrimPrefix(rawName, "mcp__omp__")
	value, _ := args.Get("args")
	if value == nil {
		value, _ = args.Get("arguments")
	}
	parsed := mcpArguments(value)
	if toolName == "edit" {
		if input, _ := parsed.Get("input"); input != nil {
			if text, ok := input.(string); ok {
				out := &cursorwire.JSONObject{}
				out.Set("input", text)
				return toolName, out
			}
		}
	}
	return toolName, parsed
}

// mcpArguments is parseGitLabDuoWorkflowMcpArguments.
func mcpArguments(value any) *cursorwire.JSONObject {
	if text, ok := value.(string); ok {
		value = cursorwire.ParseToolCallArguments(text)
	}
	if object, ok := value.(*cursorwire.JSONObject); ok && object != nil {
		return object
	}
	return &cursorwire.JSONObject{}
}

// stepLimitMessage is isGitLabDuoWorkflowStepLimitMessage.
func stepLimitMessage(message string) bool {
	return strings.Contains(strings.ToLower(message), "reached its maximum step limit")
}

// genericProcessingError is isGitLabDuoWorkflowGenericProcessingError.
func genericProcessingError(message string) bool {
	return strings.Contains(strings.ToLower(message), "error processing your request in the duo agent platform")
}

func approvalStatus(status string) bool {
	return status == "PLAN_APPROVAL_REQUIRED" || status == "TOOL_CALL_APPROVAL_REQUIRED"
}

func completionStatus(status string) bool {
	return status == "INPUT_REQUIRED" || status == "FINISHED"
}
