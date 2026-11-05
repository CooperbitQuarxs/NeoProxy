package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
)

// FactoryDroidSystemPrefix is droid-identity.md: the Droid identity sentence
// NeoPi puts ahead of the caller's system prompt on every Factory wire.
const FactoryDroidSystemPrefix = "You are Droid, an AI software engineering agent built by Factory."

// factoryDroidRuntimeVersion is the Node build droid's packaged runtime
// reports in its Stainless fingerprint.
const factoryDroidRuntimeVersion = "v26.3.0"

// FactoryDroidCompletionsTemperature is the temperature droid's
// chat-completions builder pins on every body; a caller temperature
// displaces it.
const FactoryDroidCompletionsTemperature = 1.0

// FactoryDroidDeterministicUUID is deterministicUuid: the leading 128 bits of
// seed's SHA-256 in the 8-4-4-4-12 layout, stable per seed.
func FactoryDroidDeterministicUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// FactoryDroidIdentity is what buildIdentityHeaders reads for one attempt.
type FactoryDroidIdentity struct {
	Wire     factorydroid.Wire
	Upstream string
	// RoutingSource is session_lock, configured_order or registry_default.
	RoutingSource  string
	SessionUUID    string
	RequestID      string
	OrgID          string
	OpenAIPlatform string
}

// FactoryDroidIdentityHeaders is buildIdentityHeaders: droid's client
// identity on every wire, plus each SDK's own extras (Accept and the
// OpenAI-Platform hint on the OpenAI wires, the Stainless fingerprint on
// every SDK-backed wire, the 600s timeout and x-api-key placeholder of the
// Anthropic client).
func FactoryDroidIdentityHeaders(id FactoryDroidIdentity) http.Header {
	headers := factorydroid.ClientHeaders(id.OrgID)
	headers.Set("User-Agent", "factory-cli/"+factorydroid.ClientVersion)
	headers.Set("x-api-provider", id.Upstream)
	headers.Set("x-provider-routing-source", id.RoutingSource)
	headers.Set("x-session-id", id.SessionUUID)
	headers.Set("x-assistant-message-id", id.RequestID)
	if id.Wire == factorydroid.WireCompletions || id.Wire == factorydroid.WireResponses {
		headers.Set("Accept", "application/json")
		if id.OpenAIPlatform != "" {
			headers.Set("OpenAI-Platform", id.OpenAIPlatform)
		}
	}
	if id.Wire != factorydroid.WireGoogle {
		headers.Set("X-Stainless-Lang", "js")
		if id.Wire == factorydroid.WireMessages {
			headers.Set("X-Stainless-Package-Version", "0.70.1")
		} else {
			headers.Set("X-Stainless-Package-Version", "6.25.0")
		}
		headers.Set("X-Stainless-Runtime", "node")
		headers.Set("X-Stainless-Runtime-Version", factoryDroidRuntimeVersion)
		headers.Set("X-Stainless-Arch", StainlessArch())
		headers.Set("X-Stainless-OS", StainlessOS())
		headers.Set("X-Stainless-Retry-Count", "0")
		if id.Wire == factorydroid.WireMessages {
			headers.Set("X-Stainless-Timeout", "600")
			headers.Set("x-api-key", "placeholder")
		}
	}
	return headers
}

var factoryDroidToolNameInvalid = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// FactoryDroidSanitizeToolName is sanitizeFactoryDroidToolName: the CLI's
// [a-zA-Z0-9_-] form, and a name over maxLength keeps its first maxLength-9
// characters and ends "_" plus 8 hex characters of its SHA-256.
func FactoryDroidSanitizeToolName(name string, maxLength int) string {
	sanitized := factoryDroidToolNameInvalid.ReplaceAllString(name, "_")
	if len(sanitized) <= maxLength {
		return sanitized
	}
	sum := sha256.Sum256([]byte(sanitized))
	return sanitized[:maxLength-9] + "_" + hex.EncodeToString(sum[:])[:8]
}

// FactoryDroidCompletionsToolName is encodeFactoryDroidCompletionsToolName:
// chat-completions names go out verbatim unless they exceed maxLength.
func FactoryDroidCompletionsToolName(name string, maxLength int) string {
	if len(name) > maxLength {
		return FactoryDroidSanitizeToolName(name, maxLength)
	}
	return name
}

// FactoryDroidToolNamesByWire is factoryDroidToolNamesByWire: wire name to
// the declared name, so returned calls dispatch to the advertised tool. Two
// tools encoding to one wire name cannot be told apart.
func FactoryDroidToolNamesByWire(names []string, encode func(string) string) (map[string]string, error) {
	byWire := make(map[string]string, len(names))
	for _, name := range names {
		wire := encode(name)
		if existing, ok := byWire[wire]; ok && existing != name {
			return nil, fmt.Errorf("Factory tool names %q and %q both encode as %q", existing, name, wire)
		}
		byWire[wire] = name
	}
	return byWire, nil
}

// FactoryDroidCompletionsReasoning is buildCompletionsReasoningBody: the
// route dialect's reasoning fields, which own every reasoning field of the
// body. Fields are ordered like NeoPi's spread: reasoning_effort, then
// reasoning_history, then chat_template_args. effort is empty when reasoning
// is off.
func FactoryDroidCompletionsReasoning(mode, history, effort string, disabled bool) json.RawMessage {
	withHistory := effort != "" && history != "" && history != "omit"
	effortField := func(value string) []jsjson.Field {
		if value == "" {
			return nil
		}
		fields := []jsjson.Field{{Name: "reasoning_effort", Value: jsjson.Marshal(value)}}
		if withHistory {
			fields = append(fields, jsjson.Field{Name: "reasoning_history", Value: jsjson.Marshal(history)})
		}
		return fields
	}
	effortValue := func() string {
		if disabled {
			return "none"
		}
		return effort
	}
	switch mode {
	case "none":
		return nil
	case "forced-on":
		// The route cannot turn thinking off; off maps to its lowest rung.
		if disabled {
			return jsjson.Object(effortField("low")...)
		}
		return jsjson.Object(effortField(effort)...)
	case "opt-in":
		// Toggle-only dialect: the template flag is the sole control.
		fields := []jsjson.Field{{
			Name:  "chat_template_args",
			Value: jsjson.Object(jsjson.Field{Name: "enable_thinking", Value: jsjson.Marshal(effort != "")}),
		}}
		if withHistory {
			fields = append(fields, jsjson.Field{Name: "reasoning_history", Value: jsjson.Marshal(history)})
		}
		return jsjson.Object(fields...)
	default:
		return jsjson.Object(effortField(effortValue())...)
	}
}

var factoryDroidRegionUnavailable = regexp.MustCompile(`(?i)not available in this region`)

// FactoryDroidRegionMessage is asRegionUnavailableError: a region rejection
// explained without excluding the model.
func FactoryDroidRegionMessage(modelID, message string) (string, bool) {
	if !factoryDroidRegionUnavailable.MatchString(message) {
		return "", false
	}
	return modelID + " is not served from your network's region. Choose another model.", true
}
