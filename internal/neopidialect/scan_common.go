package neopidialect

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
)

// inbandScanner is NeoPi's InbandScanner: streamed visible text in, ordered
// scan events out.
type inbandScanner interface {
	feed(text string) []scanEvent
	flush() []scanEvent
}

// terminatorWait is NeoPi's TerminatorWait (dialect/terminator-wait.ts): while
// a scanner waits inside a body for one fixed terminator, deltas are set aside
// and only the new text plus the terminator-length overlap is searched; the
// buffer is rebuilt once the terminator shows up or the stream ends.
type terminatorWait struct {
	terminator string
	tail       string
	parts      []string
}

// arm starts deferring; buffered is the scanner's pending body, known not to
// contain terminator.
func (w *terminatorWait) arm(terminator, buffered string) {
	w.terminator = terminator
	w.tail = buffered[max(0, len(buffered)-(len(terminator)-1)):]
	w.parts = w.parts[:0]
}

// absorb takes text while the terminator is still absent; false means the
// caller must release and scan.
func (w *terminatorWait) absorb(text string) bool {
	if w.terminator == "" {
		return false
	}
	window := w.tail + text
	if strings.Contains(window, w.terminator) {
		return false
	}
	w.parts = append(w.parts, text)
	w.tail = window[max(0, len(window)-(len(w.terminator)-1)):]
	return true
}

// release stops deferring and returns buffered with every absorbed delta
// appended.
func (w *terminatorWait) release(buffered string) string {
	w.terminator, w.tail = "", ""
	if len(w.parts) == 0 {
		return buffered
	}
	joined := buffered + strings.Join(w.parts, "")
	w.parts = w.parts[:0]
	return joined
}

// partialSuffixOverlapAny is the longest partialSuffixOverlap over tags.
func partialSuffixOverlapAny(text string, tags []string) int {
	best := 0
	for _, tag := range tags {
		best = max(best, partialSuffixOverlap(text, tag))
	}
	return best
}

// mintToolCallID is NeoPi's healed tool-call id: "call_" and 24 hex digits.
func mintToolCallID() string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return "call_" + hex.EncodeToString(raw[:])
}

// parseToolArgs is parseToolCallArguments then asRecord: the repaired
// object, an empty one for a non-object, or NeoPi's bounded diagnostic.
func parseToolArgs(raw string) *cursorwire.JSONObject {
	if object, ok := cursorwire.ParseToolCallArguments(raw).(*cursorwire.JSONObject); ok {
		return object
	}
	return &cursorwire.JSONObject{}
}

// isASCIISpace is the whitespace set NeoPi's Kimi scanner skips.
func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
