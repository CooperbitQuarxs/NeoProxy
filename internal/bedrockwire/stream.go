package bedrockwire

import (
	"errors"
	"io"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

// streamExceptionStatus is BEDROCK_STREAM_EXCEPTION_STATUS, keyed by the
// lowercased exception shape name.
var streamExceptionStatus = map[string]int{
	"accessdeniedexception":         403,
	"conflictexception":             400,
	"internalserverexception":       500,
	"modelerrorexception":           424,
	"modelnotreadyexception":        429,
	"modelstreamerrorexception":     424,
	"modeltimeoutexception":         408,
	"resourcenotfoundexception":     404,
	"servicequotaexceededexception": 400,
	"serviceunavailableexception":   503,
	"throttlingexception":           429,
	"validationexception":           400,
}

// StreamExceptionStatus is bedrockStreamExceptionStatus: the service-model
// status of an in-stream exception or error code, 400 when unknown.
func StreamExceptionStatus(code string) int {
	if status, ok := streamExceptionStatus[strings.ToLower(jsTrim(code))]; ok {
		return status
	}
	return 400
}

// APIError is a BedrockApiError: an in-stream exception, error frame or
// failed turn. Status 0 is NeoPi's status-less failure.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// Result is the assistant message one stream produced.
type Result struct {
	Output *cursorwire.Output
}

// decoder is streamBedrock's event loop over one Converse stream.
type decoder struct {
	sink     cursorwire.Sink
	output   *cursorwire.Output
	sentinel bool
	byIndex  map[int64]*cursorwire.OutBlock
	partial  map[*cursorwire.OutBlock]*string
}

// ReadStream reads a ConverseStream eventstream body into sink events and the
// final assistant message, as streamBedrock does after HTTP 200. sentinel is
// the request's __no_tools__ placeholder, whose calls are dropped. A failed
// turn is an *APIError; an unreadable stream is a *FrameError or the read's
// own error.
func ReadStream(body io.Reader, sentinel bool, sink cursorwire.Sink) (*Result, error) {
	d := &decoder{sink: sink, output: &cursorwire.Output{StopReason: "stop"}, sentinel: sentinel,
		byIndex: map[int64]*cursorwire.OutBlock{}, partial: map[*cursorwire.OutBlock]*string{}}
	frames := NewDecoder(body)
	errorMessage := ""
	for {
		message, err := frames.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return d.result(), err
		}
		switch message.Headers[":message-type"] {
		case "exception":
			code := message.Headers[":exception-type"]
			if code == "" {
				code = "Exception"
			}
			text := payloadMessage(message.Payload)
			return d.result(), &APIError{Status: StreamExceptionStatus(code), Message: code + ": " + text}
		case "error":
			code := message.Headers[":error-code"]
			if code == "" {
				code = "UnknownError"
			}
			text := message.Headers[":error-message"]
			if text == "" {
				text = decodeUTF8(message.Payload)
			}
			return d.result(), &APIError{Status: StreamExceptionStatus(code), Message: code + ": " + text}
		case "event":
		default:
			continue
		}
		payload, ok := safeParsePayload(message.Payload)
		if !ok {
			continue
		}
		switch message.Headers[":event-type"] {
		case "messageStart":
			if role := payload.Get("role"); role.Type != gjson.String || role.Str != "assistant" {
				return d.result(), &APIError{Message: "Unexpected assistant message start but got user message start instead"}
			}
		case "contentBlockStart":
			d.blockStart(payload)
		case "contentBlockDelta":
			d.blockDelta(payload)
		case "contentBlockStop":
			d.blockStop(payload.Get("contentBlockIndex").Int())
		case "messageStop":
			reason := payload.Get("stopReason")
			if d.sentinel && reason.Type == gjson.String && reason.Str == "tool_use" {
				d.output.StopReason = "stop"
			} else {
				d.output.StopReason = mapStopReason(reason)
			}
			if d.output.StopReason == "error" {
				// A guardrail block often ends the turn with no content; it must
				// never read as an empty completion.
				switch {
				case reason.Type == gjson.String && reason.Str == "guardrail_intervened":
					errorMessage = "Response blocked by Amazon Bedrock guardrail (stop reason: guardrail_intervened)."
				case reason.Type == gjson.String && reason.Str == "content_filtered":
					errorMessage = "Response filtered by Amazon Bedrock content filters (stop reason: content_filtered)."
				default:
					name := "unknown"
					if reason.Exists() && reason.Type != gjson.Null {
						name = jsString(reason)
					}
					errorMessage = "Generation failed with stop reason: " + name
				}
			}
		case "metadata":
			if usage := payload.Get("usage"); truthy(usage) {
				u := &d.output.Usage
				u.Input, u.Output = usage.Get("inputTokens").Int(), usage.Get("outputTokens").Int()
				u.CacheRead, u.CacheWrite = usage.Get("cacheReadInputTokens").Int(), usage.Get("cacheWriteInputTokens").Int()
				u.Total = usage.Get("totalTokens").Int()
				if u.Total == 0 {
					u.Total = u.Input + u.Output
				}
			}
		}
	}
	if d.output.StopReason == "error" {
		if errorMessage == "" {
			errorMessage = "An unknown error occurred"
		}
		return d.result(), &APIError{Message: errorMessage}
	}
	// Tool calls the stream never stopped are closed with what arrived.
	for _, block := range d.output.Content {
		if _, open := d.partial[block]; open {
			d.endTool(block)
		}
	}
	return d.result(), nil
}

func (d *decoder) result() *Result { return &Result{Output: d.output} }

// register is pushStreamBlock: the first block an index names keeps it.
func (d *decoder) register(block *cursorwire.OutBlock, index int64) {
	d.output.Content = append(d.output.Content, block)
	if _, taken := d.byIndex[index]; !taken {
		d.byIndex[index] = block
	}
}

// blockStart is handleContentBlockStart: only tool calls announce a block.
func (d *decoder) blockStart(payload gjson.Result) {
	toolUse := payload.Get("start.toolUse")
	if !truthy(toolUse) {
		return
	}
	name := toolUse.Get("name")
	if d.sentinel && name.Type == gjson.String && name.Str == noToolsSentinelName {
		return
	}
	block := &cursorwire.OutBlock{Type: "toolCall", ID: cursorwire.NormalizeToolCallID(orEmpty(toolUse.Get("toolUseId"))),
		Name: orEmpty(name), Arguments: &cursorwire.JSONObject{}}
	empty := ""
	d.partial[block] = &empty
	d.register(block, payload.Get("contentBlockIndex").Int())
	d.sink.ToolCallStart(block)
}

// blockDelta is handleContentBlockDelta: text and reasoning open their block
// on first delta; tool input accumulates as partial JSON.
func (d *decoder) blockDelta(payload gjson.Result) {
	index := payload.Get("contentBlockIndex").Int()
	block := d.byIndex[index]
	delta := payload.Get("delta")
	switch text := delta.Get("text"); {
	case text.Exists():
		if block == nil {
			block = &cursorwire.OutBlock{Type: "text"}
			d.register(block, index)
		}
		if block.Type == "text" {
			value := jsString(text)
			block.Text += value
			d.sink.TextDelta(value)
		}
	case truthy(delta.Get("toolUse")) && block != nil && block.Type == "toolCall":
		input := orEmpty(delta.Get("toolUse.input"))
		if partial, ok := d.partial[block]; ok {
			*partial += input
		} else {
			d.partial[block] = &input
		}
		d.sink.ToolCallDelta(block, input)
	case truthy(delta.Get("reasoningContent")):
		if block == nil {
			block = &cursorwire.OutBlock{Type: "thinking"}
			d.register(block, index)
		}
		if block.Type == "thinking" {
			if text := delta.Get("reasoningContent.text"); truthy(text) {
				value := jsString(text)
				block.Text += value
				d.sink.ThinkingDelta(value)
			}
		}
	}
}

// blockStop is handleContentBlockStop: a tool call's arguments are parsed
// from what arrived.
func (d *decoder) blockStop(index int64) {
	if block := d.byIndex[index]; block != nil && block.Type == "toolCall" {
		d.endTool(block)
	}
}

func (d *decoder) endTool(block *cursorwire.OutBlock) {
	partial := ""
	if value, ok := d.partial[block]; ok {
		partial = *value
		delete(d.partial, block)
	}
	block.Arguments = cursorwire.ParseToolCallArguments(partial)
	d.sink.ToolCallEnd(block)
}

// mapStopReason is mapStopReason.
func mapStopReason(reason gjson.Result) string {
	if reason.Type != gjson.String {
		return "error"
	}
	switch reason.Str {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "toolUse"
	}
	return "error"
}

// safeParsePayload is safeParsePayload plus the loop's falsy check: an empty
// payload is {}, invalid JSON and falsy values are skipped.
func safeParsePayload(payload []byte) (gjson.Result, bool) {
	if len(payload) == 0 {
		return gjson.Parse("{}"), true
	}
	text := decodeUTF8(payload)
	if !gjson.Valid(text) {
		return gjson.Result{}, false
	}
	value := gjson.Parse(text)
	return value, truthy(value)
}

// payloadMessage is an exception frame's `payload?.message || text`.
func payloadMessage(payload []byte) string {
	if value, ok := safeParsePayload(payload); ok {
		if message := value.Get("message"); truthy(message) {
			return jsString(message)
		}
	} else if len(payload) == 0 {
		return ""
	}
	return decodeUTF8(payload)
}

// truthy is JavaScript truthiness of a JSON value; absent is undefined.
func truthy(value gjson.Result) bool {
	switch value.Type {
	case gjson.Null, gjson.False:
		return false
	case gjson.Number:
		return value.Num != 0
	case gjson.String:
		return value.Str != ""
	case gjson.True, gjson.JSON:
		return true
	}
	return false
}

// orEmpty is `value || ""` for a value read as a string.
func orEmpty(value gjson.Result) string {
	if !truthy(value) {
		return ""
	}
	return jsString(value)
}

// jsString is String(value) for a JSON value.
func jsString(value gjson.Result) string {
	switch value.Type {
	case gjson.String:
		return value.Str
	case gjson.Null:
		return "null"
	case gjson.True:
		return "true"
	case gjson.False:
		return "false"
	case gjson.Number:
		return jsjson.Number(value.Num)
	case gjson.JSON:
		if value.IsArray() {
			items := value.Array()
			parts := make([]string, len(items))
			for i, item := range items {
				if item.Type != gjson.Null {
					parts[i] = jsString(item)
				}
			}
			return strings.Join(parts, ",")
		}
		return "[object Object]"
	}
	return "undefined"
}
