// Package ollamawire is NeoPi's Ollama chat transport (pi-ai
// providers/ollama.ts, api "ollama-chat") for the Ollama Cloud provider:
// the /api/chat request body built from a gateway Context and the NDJSON
// stream read back into assistant events.
package ollamawire

import "strings"

// isJSSpace is the WhiteSpace and LineTerminator set String.prototype.trim
// removes.
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff':
		return true
	}
	return r >= '\u2000' && r <= '\u200a'
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }
