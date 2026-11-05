package ollamawire

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopidialect"
	"github.com/tidwall/gjson"
)

const (
	// EmptyLengthMessage is EMPTY_OLLAMA_LENGTH_COMPLETION_MESSAGE.
	EmptyLengthMessage = "Model returned no content: prompt filled the context window; raise Ollama num_ctx or shorten the prompt."
	// EmptyLoadMessage is EMPTY_OLLAMA_LOAD_COMPLETION_MESSAGE.
	EmptyLoadMessage = "Ollama loaded the model but generated nothing (done_reason: load): the request contained no user-role message."
	// jsonlEndedMessage is readJsonl's error for a truncated final line.
	jsonlEndedMessage = "JSONL stream ended unexpectedly"
)

// llamaCppToolCallParse is LLAMA_CPP_TOOL_CALL_PARSE_PATTERN: a deterministic
// tool-call argument parse failure that retrying cannot fix.
var llamaCppToolCallParse = regexp.MustCompile(`(?i)failed to parse tool call arguments as json|\[json\.exception\.parse_error\.101\]`)

// RetryResponse is shouldRetryOllamaResponse for a retryable status: any
// status below 500, or a 5xx that is not a llama.cpp tool-call parse failure.
func RetryResponse(status int, body []byte) bool {
	return status < 500 || !llamaCppToolCallParse.Match(body)
}

// healingPattern is getStreamMarkupHealingPattern by identity class.
func healingPattern(class string) string {
	switch class {
	case "kimi":
		return neopidialect.PatternKimi
	case "deepseek":
		return neopidialect.PatternDSML
	}
	return neopidialect.PatternThinking
}

// StreamError is a failure the stream reported after HTTP 200: an NDJSON
// error record or an unreadable line.
type StreamError struct{ Message string }

func (e *StreamError) Error() string { return e.Message }

// Result is the assistant message one stream produced. ErrorMessage is set
// with StopReason "error".
type Result struct {
	Output       *cursorwire.Output
	ErrorMessage string
	// Committed reports that a non-empty content delta reached the sink,
	// which makes the attempt no longer replay-safe.
	Committed bool
}

// decoder is streamOllamaOnce's response loop over one NDJSON body.
type decoder struct {
	sink                cursorwire.Sink
	output              *cursorwire.Output
	healer              *neopidialect.Healer
	activeText          *cursorwire.OutBlock
	activeThinking      *cursorwire.OutBlock
	activeTools         []*cursorwire.OutBlock
	partial             map[*cursorwire.OutBlock]string
	healedCall          bool
	suppressHealedThink bool
	committed           bool
	errorMessage        string
}

// ReadStream reads an /api/chat NDJSON body into sink events and the final
// assistant message, as streamOllamaOnce does after HTTP 200. identityClass
// is the row's identity class, which picks the markup-healing grammar.
func ReadStream(body io.Reader, identityClass string, sink cursorwire.Sink) (*Result, error) {
	d := &decoder{sink: sink, output: &cursorwire.Output{StopReason: "stop"}, healer: neopidialect.NewHealer(healingPattern(identityClass)),
		partial: map[*cursorwire.OutBlock]string{}}
	reader := bufio.NewReaderSize(body, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		atEOF := errors.Is(err, io.EOF)
		if err != nil && !atEOF {
			return d.result(), err
		}
		trimmed := bytes.TrimFunc(line, isJSONWhitespace)
		if len(trimmed) > 0 {
			if !gjson.ValidBytes(trimmed) {
				if atEOF {
					return d.result(), &StreamError{Message: jsonlEndedMessage}
				}
				return d.result(), &StreamError{Message: "JSON Parse error: Unable to parse JSON string"}
			}
			if errChunk := d.chunk(gjson.ParseBytes(trimmed)); errChunk != nil {
				return d.result(), errChunk
			}
		}
		if atEOF {
			break
		}
	}
	d.finish()
	return d.result(), nil
}

func isJSONWhitespace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func (d *decoder) result() *Result {
	return &Result{Output: d.output, ErrorMessage: d.errorMessage, Committed: d.committed}
}

func (d *decoder) chunk(chunk gjson.Result) error {
	// Once HTTP 200 has started, Ollama reports failures as error records.
	if message := chunk.Get("error"); truthy(message) {
		return &StreamError{Message: "Ollama stream error: " + jsString(message)}
	}
	if thinking := chunk.Get("message.thinking"); truthy(thinking) {
		d.suppressHealedThink = true
		d.endText()
		d.appendThinking(jsString(thinking), false)
	}
	calls := chunk.Get("message.tool_calls")
	structured := calls.IsArray() && len(calls.Array()) > 0
	if content := chunk.Get("message.content"); truthy(content) {
		text := jsString(content)
		feed := d.healer.Feed
		if structured {
			feed = d.healer.FeedWithoutCalls
		}
		d.heal(feed(text))
	}
	if structured {
		d.endThinking()
		d.endText()
		for _, call := range calls.Array() {
			name := "unknown_tool"
			if value := call.Get("function.name"); value.Exists() && value.Type != gjson.Null {
				name = jsString(value)
			}
			partial := "{}"
			if raw := call.Get("function.arguments"); raw.Type == gjson.String {
				partial = raw.Str
			} else if raw.Exists() && raw.Type != gjson.Null {
				partial = cursorwire.StringifyJSON(cursorwire.FromGJSON(raw))
			}
			block := &cursorwire.OutBlock{Type: "toolCall", ID: "ollama:" + strconv.Itoa(len(d.output.Content)) + ":" + name, Name: name}
			d.output.Content = append(d.output.Content, block)
			d.partial[block] = partial
			d.activeTools = append(d.activeTools, block)
			d.sink.ToolCallStart(block)
			d.toolDelta(block, partial)
		}
	}
	if truthy(chunk.Get("done")) {
		d.heal(d.healer.Flush())
		d.endThinking()
		d.endText()
		d.endTools()
		d.output.StopReason = mapDoneReason(chunk.Get("done_reason"), d.output)
		if d.healedCall && d.output.StopReason == "stop" {
			d.output.StopReason = "toolUse"
		}
		cacheRead := chunk.Get("prompt_eval_cached_count").Int()
		d.output.Usage.CacheRead = cacheRead
		d.output.Usage.Input = chunk.Get("prompt_eval_count").Int() - cacheRead
		d.output.Usage.Output = chunk.Get("eval_count").Int()
		d.output.Usage.Total = d.output.Usage.Input + d.output.Usage.Output + d.output.Usage.CacheRead
	}
	return nil
}

// finish is the post-loop settlement: a final healing flush, open blocks
// closed, empty length/load turns turned into errors, and calls under a
// natural stop promoted to toolUse.
func (d *decoder) finish() {
	d.heal(d.healer.Flush())
	if d.healedCall && d.output.StopReason == "stop" {
		d.output.StopReason = "toolUse"
	}
	d.endThinking()
	d.endText()
	d.endTools()
	if d.output.StopReason == "length" && !hasVisibleContent(d.output) {
		d.output.StopReason = "error"
		d.errorMessage = EmptyLengthMessage
	}
	if d.output.StopReason == "error" && d.errorMessage == "" {
		d.errorMessage = EmptyLoadMessage
	}
	if d.output.StopReason == "stop" && hasToolCall(d.output) {
		d.output.StopReason = "toolUse"
	}
}

func (d *decoder) heal(events []neopidialect.HealEvent) {
	for _, event := range events {
		switch event.Kind {
		case neopidialect.KindText:
			d.appendText(event.Text)
		case neopidialect.KindThinking:
			if !d.suppressHealedThink {
				d.appendThinking(event.Text, true)
			}
		case neopidialect.KindToolCall:
			d.endThinking()
			d.endText()
			block := &cursorwire.OutBlock{Type: "toolCall", ID: event.ID, Name: event.Name}
			d.output.Content = append(d.output.Content, block)
			d.partial[block] = event.Arguments
			d.sink.ToolCallStart(block)
			d.toolDelta(block, event.Arguments)
			d.endTool(block)
			d.healedCall = true
		}
	}
}

func (d *decoder) appendText(text string) {
	if text == "" {
		return
	}
	d.endThinking()
	if d.activeText == nil {
		d.activeText = &cursorwire.OutBlock{Type: "text"}
		d.output.Content = append(d.output.Content, d.activeText)
	}
	d.activeText.Text += text
	d.committed = true
	d.sink.TextDelta(text)
}

// appendThinking adds to the open thinking block; healed thinking closes an
// open text block first, native thinking already has.
func (d *decoder) appendThinking(thinking string, healed bool) {
	if thinking == "" {
		return
	}
	if healed {
		d.endText()
	}
	if d.activeThinking == nil {
		d.activeThinking = &cursorwire.OutBlock{Type: "thinking"}
		d.output.Content = append(d.output.Content, d.activeThinking)
	}
	d.activeThinking.Text += thinking
	d.committed = true
	d.sink.ThinkingDelta(thinking)
}

func (d *decoder) toolDelta(block *cursorwire.OutBlock, delta string) {
	if delta != "" {
		d.committed = true
	}
	d.sink.ToolCallDelta(block, delta)
}

func (d *decoder) endText()     { d.activeText = nil }
func (d *decoder) endThinking() { d.activeThinking = nil }

func (d *decoder) endTools() {
	for _, block := range d.activeTools {
		d.endTool(block)
	}
	d.activeTools = nil
}

// endTool is endToolCallBlock: the accumulated arguments parsed with repair.
func (d *decoder) endTool(block *cursorwire.OutBlock) {
	if partial, ok := d.partial[block]; ok {
		block.Arguments = cursorwire.ParseToolCallArguments(partial)
		delete(d.partial, block)
	}
	d.sink.ToolCallEnd(block)
}

// mapDoneReason is mapDoneReason.
func mapDoneReason(reason gjson.Result, output *cursorwire.Output) string {
	switch {
	case reason.Type == gjson.String && reason.Str == "length":
		return "length"
	case reason.Type == gjson.String && reason.Str == "tool_calls":
		return "toolUse"
	case reason.Type == gjson.String && reason.Str == "load":
		return "error"
	case !reason.Exists() && hasToolCall(output):
		return "toolUse"
	}
	return "stop"
}

func hasToolCall(output *cursorwire.Output) bool {
	for _, block := range output.Content {
		if block.Type == "toolCall" {
			return true
		}
	}
	return false
}

// hasVisibleContent is ollama.ts hasVisibleAssistantContent: non-blank text
// or thinking, or any tool call.
func hasVisibleContent(output *cursorwire.Output) bool {
	for _, block := range output.Content {
		switch block.Type {
		case "toolCall":
			return true
		case "text", "thinking":
			if jsTrim(block.Text) != "" {
				return true
			}
		}
	}
	return false
}

// truthy is JavaScript truthiness of a JSON value.
func truthy(value gjson.Result) bool {
	switch value.Type {
	case gjson.String:
		return value.Str != ""
	case gjson.Number:
		return value.Num != 0
	case gjson.True, gjson.JSON:
		return true
	}
	return false
}

// jsString is String(value) for a JSON value: strings as is, others as
// JavaScript would print them.
func jsString(value gjson.Result) string {
	switch value.Type {
	case gjson.String:
		return value.Str
	case gjson.JSON:
		if value.IsArray() {
			parts := make([]string, 0)
			for _, item := range value.Array() {
				if item.Type == gjson.Null {
					parts = append(parts, "")
					continue
				}
				parts = append(parts, jsString(item))
			}
			return strings.Join(parts, ",")
		}
		return "[object Object]"
	case gjson.Number:
		return cursorwire.StringifyJSON(value.Num)
	case gjson.True:
		return "true"
	case gjson.False:
		return "false"
	}
	return fmt.Sprint(value.Value())
}
