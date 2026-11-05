package neopidialect

import "strings"

// Kimi's chat-template tool-call tokens (dialect/kimi.ts).
const (
	kimiSectionBegin = "<|tool_calls_section_begin|>"
	kimiSectionEnd   = "<|tool_calls_section_end|>"
	kimiCallBegin    = "<|tool_call_begin|>"
	kimiCallEnd      = "<|tool_call_end|>"
	kimiArgBegin     = "<|tool_call_argument_begin|>"
	kimiThinkOpen    = "<think>"
	kimiThinkClose   = "</think>"
)

var (
	kimiTokens      = []string{kimiSectionBegin, kimiSectionEnd, kimiCallBegin, kimiCallEnd, kimiArgBegin}
	kimiTokensThink = []string{kimiSectionBegin, kimiSectionEnd, kimiCallBegin, kimiCallEnd, kimiArgBegin, kimiThinkOpen}
)

type kimiState int

const (
	kimiOutside kimiState = iota
	kimiSection
	kimiHeader
	kimiArgs
	kimiThinking
)

// kimiScanner is NeoPi's KimiInbandScanner (parseThinking on): Kimi's token
// sections become tool calls and <think> blocks thinking.
type kimiScanner struct {
	buffer    string
	state     kimiState
	name      string
	thinking  string
	closeWait terminatorWait
}

func (s *kimiScanner) feed(text string) []scanEvent {
	if text == "" {
		return nil
	}
	if s.closeWait.absorb(text) {
		return nil
	}
	s.buffer = s.closeWait.release(s.buffer) + text
	events := s.consume(false)
	if s.state == kimiArgs {
		s.closeWait.arm(kimiCallEnd, s.buffer)
	}
	return events
}

func (s *kimiScanner) flush() []scanEvent {
	s.buffer = s.closeWait.release(s.buffer)
	return s.consume(true)
}

func (s *kimiScanner) consume(final bool) []scanEvent {
	var events []scanEvent
	for s.buffer != "" {
		var progressed bool
		switch s.state {
		case kimiOutside:
			events, progressed = s.consumeOutside(final, events)
		case kimiThinking:
			events, progressed = s.consumeThinking(final, events)
		case kimiSection:
			progressed = s.consumeSection(final)
		case kimiHeader:
			events, progressed = s.consumeHeader(final, events)
		default:
			events, progressed = s.consumeArgs(final, events)
		}
		if !progressed {
			break
		}
	}
	if final && s.state == kimiThinking {
		events = s.endThinking(events)
	}
	return events
}

func (s *kimiScanner) consumeOutside(final bool, events []scanEvent) ([]scanEvent, bool) {
	start := s.nextTokenIndex()
	if think := strings.Index(s.buffer, kimiThinkOpen); think != -1 && (start == -1 || think < start) {
		start = think
	}
	if start == -1 {
		hold := 0
		if !final {
			hold = partialSuffixOverlapAny(s.buffer, kimiTokensThink)
		}
		if end := len(s.buffer) - hold; end > 0 {
			events = append(events, scanEvent{kind: scanText, text: s.buffer[:end]})
			s.buffer = s.buffer[end:]
		}
		return events, false
	}
	if start > 0 {
		events = append(events, scanEvent{kind: scanText, text: s.buffer[:start]})
	}
	s.buffer = s.buffer[start:]
	if strings.HasPrefix(s.buffer, kimiThinkOpen) {
		s.buffer = s.buffer[len(kimiThinkOpen):]
		s.thinking = ""
		s.state = kimiThinking
		return append(events, scanEvent{kind: scanThinkingStart}), true
	}
	token := s.tokenAtStart()
	if token == "" {
		return events, false
	}
	s.buffer = s.buffer[len(token):]
	if token == kimiSectionBegin {
		s.state = kimiSection
	} else {
		events = append(events, scanEvent{kind: scanText, text: token})
	}
	return events, true
}

func (s *kimiScanner) consumeThinking(final bool, events []scanEvent) ([]scanEvent, bool) {
	end := strings.Index(s.buffer, kimiThinkClose)
	if end == -1 {
		hold := 0
		if !final {
			hold = partialSuffixOverlap(s.buffer, kimiThinkClose)
		}
		events = s.emitThinking(s.buffer[:len(s.buffer)-hold], events)
		s.buffer = s.buffer[len(s.buffer)-hold:]
		if final {
			events = s.endThinking(events)
		}
		return events, false
	}
	events = s.emitThinking(s.buffer[:end], events)
	s.buffer = s.buffer[end+len(kimiThinkClose):]
	return s.endThinking(events), true
}

func (s *kimiScanner) emitThinking(delta string, events []scanEvent) []scanEvent {
	if delta == "" {
		return events
	}
	s.thinking += delta
	return append(events, scanEvent{kind: scanThinkingDelta, text: delta})
}

func (s *kimiScanner) endThinking(events []scanEvent) []scanEvent {
	events = append(events, scanEvent{kind: scanThinkingEnd, text: s.thinking})
	s.thinking = ""
	s.state = kimiOutside
	return events
}

func (s *kimiScanner) consumeSection(final bool) bool {
	i := 0
	for i < len(s.buffer) && isASCIISpace(s.buffer[i]) {
		i++
	}
	s.buffer = s.buffer[i:]
	if s.buffer == "" {
		return false
	}
	switch token := s.tokenAtStart(); token {
	case kimiSectionEnd:
		s.buffer = s.buffer[len(kimiSectionEnd):]
		s.state = kimiOutside
		return true
	case kimiCallBegin:
		s.buffer = s.buffer[len(kimiCallBegin):]
		s.state = kimiHeader
		return true
	case "":
	default:
		s.buffer = s.buffer[len(token):]
		return true
	}
	if !final && partialSuffixOverlapAny(s.buffer, kimiTokens) == len(s.buffer) {
		return false
	}
	s.buffer = s.buffer[1:]
	return true
}

func (s *kimiScanner) consumeHeader(final bool, events []scanEvent) ([]scanEvent, bool) {
	sep := strings.Index(s.buffer, kimiArgBegin)
	if sep == -1 {
		if final {
			s.dropBufferedCall()
		}
		return events, false
	}
	s.name = kimiFunctionName(jsTrim(s.buffer[:sep]))
	events = append(events, scanEvent{kind: scanToolStart, name: s.name})
	s.buffer = s.buffer[sep+len(kimiArgBegin):]
	s.state = kimiArgs
	return events, true
}

func (s *kimiScanner) consumeArgs(final bool, events []scanEvent) ([]scanEvent, bool) {
	end := strings.Index(s.buffer, kimiCallEnd)
	if end == -1 {
		if final {
			s.dropBufferedCall()
		}
		return events, false
	}
	raw := jsTrim(s.buffer[:end])
	args := parseToolArgs(raw)
	events = append(events, scanEvent{kind: scanToolEnd, name: s.name, args: args})
	s.buffer = s.buffer[end+len(kimiCallEnd):]
	s.name = ""
	s.state = kimiSection
	return events, true
}

func (s *kimiScanner) nextTokenIndex() int {
	best := -1
	for _, token := range kimiTokens {
		if i := strings.Index(s.buffer, token); i != -1 && (best == -1 || i < best) {
			best = i
		}
	}
	return best
}

func (s *kimiScanner) tokenAtStart() string {
	for _, token := range kimiTokens {
		if strings.HasPrefix(s.buffer, token) {
			return token
		}
	}
	return ""
}

func (s *kimiScanner) dropBufferedCall() {
	s.buffer, s.name = "", ""
	s.state = kimiOutside
}

// kimiFunctionName is normalizeKimiFunctionName: "functions.read:0" names
// the tool "read".
func kimiFunctionName(rawID string) string {
	before, _, _ := strings.Cut(rawID, ":")
	parts := strings.Split(before, ".")
	return jsTrim(parts[len(parts)-1])
}
