package neopidialect

import "github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"

// Dialect is NeoPi's in-band dialect id (catalog identity/dialect.ts).
type Dialect string

const (
	DialectGLM       Dialect = "glm"
	DialectHermes    Dialect = "hermes"
	DialectKimi      Dialect = "kimi"
	DialectXML       Dialect = "xml"
	DialectAnthropic Dialect = "anthropic"
	DialectDeepSeek  Dialect = "deepseek"
	DialectHarmony   Dialect = "harmony"
	DialectQwen3     Dialect = "qwen3"
	DialectGemini    Dialect = "gemini"
	DialectGemma     Dialect = "gemma"
	DialectMiniMax   Dialect = "minimax"
)

// fallbackDialect is NeoPi's FALLBACK_DIALECT.
const fallbackDialect = DialectXML

// PreferredDialect mirrors NeoPi's preferredDialect(modelId): classify the model
// id (empty provider, lenient) and map its class to a dialect.
func PreferredDialect(modelID string) Dialect {
	switch neopitaxonomy.Classify("", modelID).Class {
	case "anthropic":
		return DialectAnthropic
	case "glm":
		return DialectGLM
	case "gemini":
		return DialectGemini
	case "gemma":
		return DialectGemma
	case "kimi":
		return DialectKimi
	case "qwen":
		return DialectQwen3
	case "deepseek":
		return DialectDeepSeek
	case "minimax":
		return DialectMiniMax
	case "openai", "gpt-oss":
		return DialectHarmony
	default:
		return fallbackDialect
	}
}
