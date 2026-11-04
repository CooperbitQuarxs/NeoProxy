package config

import "strings"

// Upstream APIs an openai-compatibility entry can route a model to. The names
// follow the gateway vocabulary (OpenCode's /models "api" keys).
const (
	DialectChatCompletions = "chat-completions"
	DialectResponses       = "responses"
	DialectMessages        = "messages"
	DialectGenerateContent = "generate-content"
)

// NormalizeDialect canonicalizes a configured dialect name, accepting NeoPi's
// api ids as synonyms. Empty means chat completions; unknown values return "".
func NormalizeDialect(dialect string) string {
	switch strings.ToLower(strings.TrimSpace(dialect)) {
	case "", "chat", DialectChatCompletions, "openai-completions":
		return DialectChatCompletions
	case DialectResponses, "openai-responses":
		return DialectResponses
	case DialectMessages, "anthropic-messages":
		return DialectMessages
	case DialectGenerateContent, "google-generative-ai":
		return DialectGenerateContent
	default:
		return ""
	}
}

// ResolveModelDialect returns the upstream dialect and base-URL override for one
// model of this entry, matched by upstream name first and then by alias. The
// model's own settings win over the entry's. An unknown configured dialect is
// returned verbatim so callers can refuse it instead of silently misrouting.
func (c *OpenAICompatibility) ResolveModelDialect(upstreamModel, requestedModel string) (dialect, baseURL string) {
	if c == nil {
		return DialectChatCompletions, ""
	}
	dialect = c.Dialect
	if m := c.findModel(upstreamModel, requestedModel); m != nil {
		if strings.TrimSpace(m.Dialect) != "" {
			dialect = m.Dialect
		}
		baseURL = strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	}
	if normalized := NormalizeDialect(dialect); normalized != "" {
		return normalized, baseURL
	}
	return strings.TrimSpace(dialect), baseURL
}

func (c *OpenAICompatibility) findModel(upstreamModel, requestedModel string) *OpenAICompatibilityModel {
	for _, candidate := range []string{upstreamModel, requestedModel} {
		if candidate = strings.TrimSpace(candidate); candidate == "" {
			continue
		}
		for i := range c.Models {
			if strings.EqualFold(strings.TrimSpace(c.Models[i].Name), candidate) {
				return &c.Models[i]
			}
		}
		for i := range c.Models {
			if strings.EqualFold(strings.TrimSpace(c.Models[i].Alias), candidate) {
				return &c.Models[i]
			}
		}
	}
	return nil
}
