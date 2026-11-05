package googlewire

import "strings"

// isJSSpace is ECMAScript WhiteSpace plus LineTerminator, the set
// String.prototype.trim removes (U+0085 is not in it; U+FEFF is).
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

// jsTrim is String.prototype.trim.
func jsTrim(value string) string { return strings.TrimFunc(value, isJSSpace) }

// jsTrimEnd is String.prototype.trimEnd.
func jsTrimEnd(value string) string { return strings.TrimRightFunc(value, isJSSpace) }
