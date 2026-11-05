package neopidialect

import (
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
)

// Qwen's XML tool-call section (dialect/qwen-xml.ts): self-closing elements
// whose attributes are the arguments, inside <tool_calls>…</tool_calls>.
const (
	qwenSectionOpen  = "<tool_calls>"
	qwenSectionClose = "</tool_calls>"
)

var (
	qwenToolElement = regexp.MustCompile(`(?s)^<([A-Za-z_][\w.-]*)(\s[^<>]*?)?\s*/>$`)
	qwenAttribute   = regexp.MustCompile(`([A-Za-z_][\w.-]*)\s*=\s*("([^"]*)"|'([^']*)')`)
	qwenElements    = regexp.MustCompile(`(?s)<[^<>]+/>`)
)

// qwenXMLScanner is NeoPi's QwenXmlInbandScanner.
type qwenXMLScanner struct {
	buffer        string
	insideSection bool
	closeWait     terminatorWait
}

func (s *qwenXMLScanner) feed(chunk string) []scanEvent {
	if s.closeWait.absorb(chunk) {
		return nil
	}
	s.buffer = s.closeWait.release(s.buffer) + chunk
	events := s.drain(false)
	if s.insideSection {
		s.closeWait.arm(qwenSectionClose, s.buffer)
	}
	return events
}

func (s *qwenXMLScanner) flush() []scanEvent {
	s.buffer = s.closeWait.release(s.buffer)
	return s.drain(true)
}

func (s *qwenXMLScanner) drain(final bool) []scanEvent {
	var events []scanEvent
	for s.buffer != "" {
		if !s.insideSection {
			if open := strings.Index(s.buffer, qwenSectionOpen); open >= 0 {
				if open > 0 {
					events = append(events, scanEvent{kind: scanText, text: s.buffer[:open]})
				}
				s.buffer = s.buffer[open+len(qwenSectionOpen):]
				s.insideSection = true
				continue
			}
			if final {
				events = append(events, scanEvent{kind: scanText, text: s.buffer})
				s.buffer = ""
				break
			}
			if visible := len(s.buffer) - partialSuffixOverlap(s.buffer, qwenSectionOpen); visible > 0 {
				events = append(events, scanEvent{kind: scanText, text: s.buffer[:visible]})
				s.buffer = s.buffer[visible:]
			}
			break
		}
		end := strings.Index(s.buffer, qwenSectionClose)
		if end < 0 {
			if final {
				s.buffer = ""
			}
			break
		}
		for _, element := range qwenElements.FindAllString(s.buffer[:end], -1) {
			name, args, ok := qwenToolCall(element)
			if !ok {
				continue
			}
			events = append(events, scanEvent{kind: scanToolStart, name: name}, scanEvent{kind: scanToolEnd, name: name, args: args})
		}
		s.buffer = s.buffer[end+len(qwenSectionClose):]
		s.insideSection = false
	}
	return events
}

// qwenToolCall is parseToolElement: the element's tag names the tool and its
// attributes are string arguments.
func qwenToolCall(element string) (string, *cursorwire.JSONObject, bool) {
	match := qwenToolElement.FindStringSubmatch(element)
	if match == nil {
		return "", nil, false
	}
	args := &cursorwire.JSONObject{}
	for _, attribute := range qwenAttribute.FindAllStringSubmatch(match[2], -1) {
		value := attribute[3]
		if strings.HasPrefix(attribute[2], "'") {
			value = attribute[4]
		}
		args.Set(attribute[1], value)
	}
	return match[1], args, true
}
