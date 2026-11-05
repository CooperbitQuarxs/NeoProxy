package handlers

import (
	"regexp"
	"strings"
)

// UpstreamErrorMessageLimit bounds each client-visible upstream error string.
const UpstreamErrorMessageLimit = 2048

const upstreamErrorFieldLimit = 256

var (
	upstreamErrorSensitiveValuePattern = regexp.MustCompile(`(?i)((?:"?(?:api[_-]?key|access[_-]?token|token|authorization|secret)"?)\s*[=:]\s*"?)([^\s"&,;}]+)`)
	upstreamErrorBearerPattern         = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
)

func truncateUpstreamErrorText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func redactUpstreamErrorText(text string) string {
	text = upstreamErrorSensitiveValuePattern.ReplaceAllString(text, `${1}[REDACTED]`)
	return upstreamErrorBearerPattern.ReplaceAllString(text, "Bearer [REDACTED]")
}

// SanitizeUpstreamErrorText applies the shared secret-redaction and text bounds.
func SanitizeUpstreamErrorText(text string) string {
	return truncateUpstreamErrorText(redactUpstreamErrorText(text), UpstreamErrorMessageLimit)
}

// SanitizeUpstreamErrorField also trims and bounds short error/event identifiers.
func SanitizeUpstreamErrorField(text string) string {
	return truncateUpstreamErrorText(redactUpstreamErrorText(strings.TrimSpace(text)), upstreamErrorFieldLimit)
}

func isUpstreamErrorSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	if strings.Contains(k, "tokens") || strings.Contains(k, "token_count") || strings.Contains(k, "token_limit") || strings.Contains(k, "token_usage") {
		return false
	}
	switch k {
	case "authorization", "secret", "password", "passwd", "api_key", "apikey", "token", "access_token", "refresh_token", "id_token", "auth_token", "session_token", "api_token", "client_secret", "client_key":
		return true
	}
	return strings.HasSuffix(k, "_secret") || strings.HasSuffix(k, "_password") || strings.HasSuffix(k, "_api_key") || strings.HasSuffix(k, "_token")
}

// SanitizeUpstreamErrorNode returns a client-safe copy of a decoded JSON value.
// It preserves numeric values; decode raw JSON with UseNumber for exact fidelity.
func SanitizeUpstreamErrorNode(val any) any {
	switch v := val.(type) {
	case string:
		return SanitizeUpstreamErrorText(v)
	case map[string]any:
		cleaned := make(map[string]any, len(v))
		for k, item := range v {
			if isUpstreamErrorSensitiveKey(k) {
				cleaned[k] = "[REDACTED]"
				continue
			}
			cleaned[k] = SanitizeUpstreamErrorNode(item)
		}
		return cleaned
	case []any:
		cleaned := make([]any, len(v))
		for i, item := range v {
			cleaned[i] = SanitizeUpstreamErrorNode(item)
		}
		return cleaned
	default:
		return val
	}
}
