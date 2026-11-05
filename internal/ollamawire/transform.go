// Package ollamawire is NeoPi's Ollama chat transport (pi-ai
// providers/ollama.ts, api "ollama-chat") for the Ollama Cloud provider:
// the /api/chat request body built from a gateway Context and the NDJSON
// stream read back into assistant events.
package ollamawire

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopidialect"
)

const (
	// maxToolCallIDLength is MAX_TOOL_CALL_ID_LENGTH, which bounds only the
	// segments of a duplicate id's replacement.
	maxToolCallIDLength = 64
	// maxToolCallNameLength is MAX_TOOL_CALL_NAME_LENGTH; no catalog rule
	// narrows tool names for ollama-chat.
	maxToolCallNameLength = 128
	noResultProvided      = "No result provided"
)

// strictToolCallName is STRICT_TOOL_CALL_NAME_RE.
var strictToolCallName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// transformMessages is transformMessages(messages, model) for an ollama-chat
// target over history the auth gateway parsed. Every gateway assistant turn
// is stamped openai-completions/openai, so it is never the target's own and
// carries no stop reason: its thinking is demoted to text in the target's
// dialect (blank thinking dropped), tool-call ids stay as sent (no
// normalizer, not an Anthropic target) but later duplicates get _dupN
// suffixes, every call is answered by exactly one result ("No result
// provided" when none arrived), and orphan results become a stale-tool-result
// note or are dropped. Credential redaction is off by default and not ported.
func transformMessages(messages []cursorwire.Message, modelID string) []cursorwire.Message {
	messages = sanitizeMalformedToolCalls(messages)
	normalized := make([]cursorwire.Message, len(messages))
	for index, msg := range messages {
		if msg.Role == "assistant" {
			msg = demoteForeignAssistant(msg, modelID)
		}
		normalized[index] = msg
	}
	return pairToolResults(deduplicateToolCallIDs(normalized))
}

func isMalformedToolCall(block cursorwire.Block) bool {
	if jsTrim(block.ID) == "" {
		return true
	}
	return len(utf16.Encode([]rune(block.Name))) > maxToolCallNameLength || !strictToolCallName.MatchString(block.Name)
}

// sanitizeMalformedToolCalls drops tool calls whose id or name the target
// cannot replay, the results that answer them (per id, first in first out
// within each result window), and assistant turns left empty.
func sanitizeMalformedToolCalls(messages []cursorwire.Message) []cursorwire.Message {
	hasMalformed := false
	for _, msg := range messages {
		for _, block := range msg.Blocks {
			if msg.Role == "assistant" && block.Type == "toolCall" && isMalformedToolCall(block) {
				hasMalformed = true
			}
		}
	}
	if !hasMalformed {
		return messages
	}
	dropQueues := map[string][]bool{}
	result := make([]cursorwire.Message, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "assistant":
			dropQueues = map[string][]bool{}
			var kept []cursorwire.Block
			for _, block := range msg.Blocks {
				if block.Type == "toolCall" {
					malformed := isMalformedToolCall(block)
					dropQueues[block.ID] = append(dropQueues[block.ID], malformed)
					if malformed {
						continue
					}
				}
				kept = append(kept, block)
			}
			if len(kept) == 0 {
				continue
			}
			msg.Blocks = kept
			result = append(result, msg)
		case "toolResult":
			if queue := dropQueues[msg.ToolCallID]; len(queue) > 0 {
				dropQueues[msg.ToolCallID] = queue[1:]
				if queue[0] {
					continue
				}
			}
			result = append(result, msg)
		default:
			dropQueues = map[string][]bool{}
			result = append(result, msg)
		}
	}
	return result
}

// demoteForeignAssistant is the first pass over a foreign assistant turn:
// non-blank thinking becomes demoted text, text is copied, tool calls keep
// their ids, and a trailing demoted block loses its trailing whitespace.
func demoteForeignAssistant(msg cursorwire.Message, modelID string) cursorwire.Message {
	blocks := make([]cursorwire.Block, 0, len(msg.Blocks))
	trailingDemoted := false
	for _, block := range msg.Blocks {
		switch block.Type {
		case "thinking":
			if jsTrim(block.Thinking) == "" {
				continue
			}
			blocks = append(blocks, cursorwire.Block{Type: "text", Text: neopidialect.RenderDemotedThinking(modelID, block.Thinking)})
			trailingDemoted = true
			continue
		case "text":
			blocks = append(blocks, cursorwire.Block{Type: "text", Text: block.Text})
		case "toolCall":
			blocks = append(blocks, block)
		default:
			continue
		}
		trailingDemoted = false
	}
	if n := len(blocks); n > 0 && trailingDemoted {
		blocks[n-1].Text = jsTrimEnd(blocks[n-1].Text)
	}
	msg.Blocks = blocks
	return msg
}

// deduplicateToolCallIds: a repeated call id gets the first free _dupN
// suffix (each pipe-separated segment fitted to 64 UTF-16 units), and later
// results answering it are rewritten first in first out. Opaque Chat
// Completions ids pair by the id itself.
func deduplicateToolCallIDs(messages []cursorwire.Message) []cursorwire.Message {
	seen := map[string]int{}
	pendingRewrites := map[string][]*string{}
	out := make([]cursorwire.Message, len(messages))
	for index, msg := range messages {
		switch msg.Role {
		case "toolResult":
			if rewrites := pendingRewrites[msg.ToolCallID]; len(rewrites) > 0 {
				pendingRewrites[msg.ToolCallID] = rewrites[1:]
				if rewrites[0] != nil {
					msg.ToolCallID = *rewrites[0]
				}
			}
		case "assistant":
			touched := map[string]bool{}
			blocks := append([]cursorwire.Block(nil), msg.Blocks...)
			for i, block := range blocks {
				if block.Type != "toolCall" {
					continue
				}
				key := block.ID
				if !touched[key] {
					// A turn reusing an id discards rewrites still pending
					// from older turns.
					touched[key] = true
					delete(pendingRewrites, key)
				}
				previous := seen[key]
				if previous == 0 {
					seen[key] = 1
					pendingRewrites[key] = append(pendingRewrites[key], nil)
					continue
				}
				dup := previous
				replacement := appendDuplicateSuffix(key, "_dup"+strconv.Itoa(dup))
				for seen[replacement] > 0 {
					dup++
					replacement = appendDuplicateSuffix(key, "_dup"+strconv.Itoa(dup))
				}
				seen[key] = dup + 1
				seen[replacement] = 1
				pendingRewrites[key] = append(pendingRewrites[key], &replacement)
				blocks[i].ID = replacement
			}
			msg.Blocks = blocks
		}
		out[index] = msg
	}
	return out
}

func appendDuplicateSuffix(id, suffix string) string {
	parts := strings.Split(id, "|")
	for i, part := range parts {
		parts[i] = appendSegmentDuplicateSuffix(part, suffix)
	}
	return strings.Join(parts, "|")
}

func appendSegmentDuplicateSuffix(segment, suffix string) string {
	units := utf16.Encode([]rune(segment))
	if len(units)+len(suffix) <= maxToolCallIDLength {
		return segment + suffix
	}
	budget := min(max(0, maxToolCallIDLength-len(suffix)), len(units))
	return string(utf16.Decode(units[:budget])) + suffix
}

type indexedResult struct {
	index    int
	msg      cursorwire.Message
	consumed bool
}

// pairToolResults is the second pass: each assistant's calls open a result
// window closed by the next assistant, user or developer turn (or the end),
// where every unanswered call takes its earliest later real result or a
// synthetic "No result provided" error. Results for known calls answered
// already or outside their window are dropped; a result for no known call
// is dropped while a window is open, else kept as a stale-tool-result note
// of its non-blank text.
func pairToolResults(messages []cursorwire.Message) []cursorwire.Message {
	realResults := map[string][]*indexedResult{}
	knownCalls := map[string]bool{}
	for index, msg := range messages {
		switch msg.Role {
		case "toolResult":
			realResults[msg.ToolCallID] = append(realResults[msg.ToolCallID], &indexedResult{index: index, msg: msg})
		case "assistant":
			for _, block := range msg.Blocks {
				if block.Type == "toolCall" {
					knownCalls[block.ID] = true
				}
			}
		}
	}
	takeResult := func(id string, after int) (cursorwire.Message, bool) {
		for _, entry := range realResults[id] {
			if entry.consumed || entry.index <= after {
				continue
			}
			entry.consumed = true
			return entry.msg, true
		}
		return cursorwire.Message{}, false
	}

	result := make([]cursorwire.Message, 0, len(messages))
	resolved := map[string]bool{}
	var pendingCalls []cursorwire.Block
	pendingStart := -1
	flushPending := func() {
		for _, call := range pendingCalls {
			if resolved[call.ID] {
				continue
			}
			if real, ok := takeResult(call.ID, pendingStart); ok {
				result = append(result, real)
			} else {
				result = append(result, cursorwire.Message{Role: "toolResult", ToolCallID: call.ID, ToolName: call.Name, IsError: true,
					Parts: []cursorwire.Part{{Text: noResultProvided}}})
			}
			resolved[call.ID] = true
		}
		pendingCalls = nil
	}
	hasOpenPending := func() bool {
		for _, call := range pendingCalls {
			if !resolved[call.ID] {
				return true
			}
		}
		return false
	}

	for index, msg := range messages {
		switch msg.Role {
		case "assistant":
			flushPending()
			for _, block := range msg.Blocks {
				if block.Type == "toolCall" {
					pendingCalls = append(pendingCalls, block)
				}
			}
			pendingStart = index
			result = append(result, msg)
		case "toolResult":
			key := msg.ToolCallID
			if resolved[key] {
				continue
			}
			inWindow := false
			for _, call := range pendingCalls {
				inWindow = inWindow || call.ID == key
			}
			if inWindow {
				resolved[key] = true
				result = append(result, msg)
				continue
			}
			if knownCalls[key] || hasOpenPending() {
				continue
			}
			var texts []string
			for _, part := range msg.Parts {
				if !part.Image && jsTrim(part.Text) != "" {
					texts = append(texts, part.Text)
				}
			}
			if len(texts) > 0 {
				attr := ""
				if msg.IsError {
					attr = ` is-error="true"`
				}
				result = append(result, cursorwire.Message{Role: "user", Plain: true,
					Text: "<stale-tool-result tool=\"" + msg.ToolName + "\" id=\"" + msg.ToolCallID + "\"" + attr + ">\n" + strings.Join(texts, "\n") + "\n</stale-tool-result>"})
			}
		default:
			flushPending()
			result = append(result, msg)
		}
	}
	flushPending()
	return result
}

// isJSSpace is the WhiteSpace and LineTerminator set String.prototype.trim
// removes.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func jsTrim(s string) string    { return strings.TrimFunc(s, isJSSpace) }
func jsTrimEnd(s string) string { return strings.TrimRightFunc(s, isJSSpace) }
