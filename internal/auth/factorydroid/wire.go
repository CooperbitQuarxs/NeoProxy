// Package factorydroid ports NeoPi's Factory Droid provider: the WorkOS
// device-code sign-in and refresh, the account scope a credential resolves
// to, and the wire constants shared by discovery, inference and usage
// (pi-catalog wire/factory-droid.ts, pi-ai registry/oauth/factory-droid.ts).
package factorydroid

import "net/http"

// Provider is NeoPi's provider id for Factory Droid.
const Provider = "factory-droid"

// ClientVersion is FACTORY_DROID_CLIENT_VERSION, the CLI release the proxy
// sees.
const ClientVersion = "0.237.0"

// Wire is a protocol the Factory proxy multiplexes (FACTORY_DROID_WIRES).
type Wire string

// Factory proxy wires.
const (
	WireCompletions Wire = "openai-completions"
	WireResponses   Wire = "openai-responses"
	WireMessages    Wire = "anthropic-messages"
	WireGoogle      Wire = "google-generate"
)

// InferenceRegion resolves an account's inference region
// (resolveFactoryDroidInferenceRegion): an explicit scope wins, else EU
// residency infers EU, else global.
func InferenceRegion(region, inferenceRegion string) string {
	if inferenceRegion != "" {
		return inferenceRegion
	}
	if region == "eu" {
		return "eu"
	}
	return "global"
}

// APIBaseURL is factoryDroidApiBaseUrl: EU accounts are served from the EU
// region.
func APIBaseURL(region string) string {
	if region == "eu" {
		return "https://api.eu.factory.ai"
	}
	return "https://api.factory.ai"
}

// WireBaseURL is factoryDroidWireBaseUrl: the per-wire base for an account
// region; the stream layer appends the path suffix.
func WireBaseURL(wire Wire, region string) string {
	host := APIBaseURL(region)
	switch wire {
	case WireMessages:
		return host + "/api/llm/a"
	case WireGoogle:
		return host + "/api/llm/g/v1"
	default:
		return host + "/api/llm/o/v1"
	}
}

// ClientHeaders is factoryDroidClientHeaders: the identity discovery, usage
// and inference requests share.
func ClientHeaders(orgID string) http.Header {
	headers := http.Header{}
	headers.Set("X-Client-Version", ClientVersion)
	headers.Set("X-Factory-Client", "cli")
	if orgID != "" {
		headers.Set("X-Factory-Org-Id", orgID)
	}
	return headers
}
