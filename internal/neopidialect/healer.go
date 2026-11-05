// Package neopidialect holds NeoPi's dialect machinery for the visible text
// channel: StreamMarkupHealing (the always-on leaked-thinking healer plus the
// Kimi, DeepSeek DSML and Qwen XML tool-call grammars), the thinking-fence
// stripper, and the demoted-thinking renderer. Each file is a port of the NeoPi
// source named in its comments, and the tests replay captures produced by
// running NeoPi's own code under Bun.
package neopidialect

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
)

// Event kinds emitted by Healer.
const (
	KindText     = "text"
	KindThinking = "thinking"
	KindToolCall = "toolCall"
)

// StreamMarkupHealingPattern values (StreamMarkupHealingPattern).
const (
	PatternThinking = "thinking"
	PatternKimi     = "kimi"
	PatternDSML     = "dsml"
	PatternQwen     = "qwen"
)

// HealEvent is one cleaned event in stream order: visible text, a thinking
// delta healed out of leaked reasoning markup, or a tool call reconstructed
// from leaked tool-call markup (ID, Name and its JSON Arguments).
type HealEvent struct {
	Kind      string
	Text      string
	ID        string
	Name      string
	Arguments string
}

// Healer consumes streamed visible text and emits cleaned text, thinking and
// tool-call events. It is NeoPi's StreamMarkupHealing: a ThinkingInbandScanner
// always heals leaked `<think>`, `<thinking>`, `<scratchpad>`, ```thinking
// fences and Gemma/Harmony channels out of the visible channel; for the kimi,
// dsml and qwen patterns that provider's tool-call grammar runs first and its
// visible text is piped through the thinking healer.
//
// Feed only one stream channel (the visible text). A Healer is not safe for
// concurrent use; use one per stream.
type Healer struct {
	pattern  string
	tool     inbandScanner
	thinking *thinkingScanner
	closed   bool
}

// NewThinkingHealer returns a "thinking"-pattern Healer for one stream.
func NewThinkingHealer() *Healer {
	return NewHealer(PatternThinking)
}

// NewHealer returns a Healer for one visible-text stream with the given
// StreamMarkupHealing pattern; an unknown pattern heals thinking only.
func NewHealer(pattern string) *Healer {
	h := &Healer{pattern: pattern, thinking: newThinkingScanner()}
	switch pattern {
	case PatternKimi:
		h.tool = &kimiScanner{}
	case PatternDSML:
		h.tool = newDeepseekScanner()
	case PatternQwen:
		h.tool = &qwenXMLScanner{}
	}
	return h
}

// Feed consumes a chunk and returns the cleaned events in stream order
// (StreamMarkupHealing.feedEvents).
func (h *Healer) Feed(text string) []HealEvent {
	if text == "" {
		return nil
	}
	h.markSectionClosed(text)
	if h.tool == nil {
		return convertScanEvents(h.thinking.feed(text))
	}
	return convertScanEvents(h.healThinking(h.tool.feed(text)))
}

// FeedWithoutCalls is feedEventsWithoutCalls: the chunk's events with every
// event from the first synthesized tool call on dropped except text and
// thinking, for a chunk whose structured tool_calls stay the source of truth.
func (h *Healer) FeedWithoutCalls(text string) []HealEvent {
	events := h.Feed(text)
	for i, event := range events {
		if event.Kind != KindToolCall {
			continue
		}
		out := events[:i:i]
		for _, rest := range events[i+1:] {
			if rest.Kind != KindToolCall {
				out = append(out, rest)
			}
		}
		return out
	}
	return events
}

// Flush returns held-back stream-end fragments as ordered events
// (StreamMarkupHealing.flushEvents): partial tool calls are dropped and an
// unterminated thinking block is emitted as thinking.
func (h *Healer) Flush() []HealEvent {
	var tail []scanEvent
	if h.tool != nil {
		tail = h.healThinking(h.tool.flush())
	}
	tail = append(tail, h.thinking.flush()...)
	return convertScanEvents(tail)
}

// SectionClosed reports whether a configured tool-call section or envelope
// has fully closed (StreamMarkupHealing.sectionClosed).
func (h *Healer) SectionClosed() bool { return h.closed }

func (h *Healer) markSectionClosed(text string) {
	if h.closed || h.tool == nil {
		return
	}
	switch h.pattern {
	case PatternKimi:
		h.closed = strings.Contains(text, kimiSectionEnd)
	case PatternQwen:
		h.closed = strings.Contains(text, qwenSectionClose)
	default:
		h.closed = strings.Contains(text, dsmlCallsCloseFull) || strings.Contains(text, dsmlCallsCloseASCII)
	}
}

// healThinking re-scans the tool grammar's visible text through the
// thinking healer; its thinking and tool-call events pass through in order.
func (h *Healer) healThinking(events []scanEvent) []scanEvent {
	var out []scanEvent
	for _, event := range events {
		if event.kind == scanText {
			out = append(out, h.thinking.feed(event.text)...)
		} else {
			out = append(out, event)
		}
	}
	return out
}

func convertScanEvents(events []scanEvent) []HealEvent {
	var out []HealEvent
	for _, event := range events {
		switch event.kind {
		case scanText:
			out = append(out, HealEvent{Kind: KindText, Text: event.text})
		case scanThinkingDelta:
			if event.text != "" {
				out = append(out, HealEvent{Kind: KindThinking, Text: event.text})
			}
		case scanToolEnd:
			args := event.args
			if args == nil {
				args = &cursorwire.JSONObject{}
			}
			out = append(out, HealEvent{Kind: KindToolCall, ID: mintToolCallID(), Name: event.name, Arguments: cursorwire.StringifyJSON(args)})
		}
	}
	return out
}
