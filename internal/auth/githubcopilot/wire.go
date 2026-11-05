// Package githubcopilot ports NeoPi's GitHub Copilot provider: the GitHub
// device-code sign-in, the Copilot API endpoint a token is served from, the
// client identity Copilot requests carry and the account's premium-request
// usage (pi-catalog wire/github-copilot.ts, pi-ai registry/oauth/github-copilot.ts,
// providers/github-copilot-headers.ts, usage/github-copilot.ts).
package githubcopilot

import (
	"net/http"
	"net/url"
	"strings"
)

// Provider is NeoPi's provider id for GitHub Copilot.
const Provider = "github-copilot"

// CLIVersion is COPILOT_CLI_VERSION, the Copilot CLI release the identity
// headers name.
const CLIVersion = "1.0.82"

// CLIUserAgent is COPILOT_CLI_USER_AGENT.
const CLIUserAgent = "copilot/" + CLIVersion

// CLIIntegrationID is the Copilot CLI's Copilot-Integration-Id.
const CLIIntegrationID = "copilot-developer-cli"

// ChatIntegrationID is COPILOT_CHAT_INTEGRATION_ID: chat and model-policy
// requests default to the chat surface, which Business organizations that
// gate premium models per surface commonly allow.
const ChatIntegrationID = "copilot-chat"

// APIVersion is COPILOT_API_VERSION, sent on Copilot API requests only.
const APIVersion = "2026-08-01"

// PersonalBaseURL is PERSONAL_GITHUB_COPILOT_BASE_URL.
const PersonalBaseURL = "https://api.githubcopilot.com"

// GitHubHeaders are COPILOT_GITHUB_HEADERS, sent to GitHub's own API.
func GitHubHeaders() http.Header {
	return http.Header{"User-Agent": {CLIUserAgent}}
}

// IdentityHeaders are COPILOT_CAPI_IDENTITY_HEADERS: the Copilot CLI identity
// sent to the Copilot API.
func IdentityHeaders() http.Header {
	headers := GitHubHeaders()
	headers.Set("Editor-Version", CLIUserAgent)
	headers.Set("Copilot-Integration-Id", CLIIntegrationID)
	headers.Set("Copilot-Harness-Id", "copilot-sdk")
	headers.Set("Openai-Intent", "conversation-agent")
	return headers
}

// APIHeaders are COPILOT_API_HEADERS: the identity plus the API version.
func APIHeaders() http.Header {
	headers := IdentityHeaders()
	headers.Set("X-GitHub-Api-Version", APIVersion)
	return headers
}

// DiscoveryHeaders are COPILOT_DISCOVERY_HEADERS, for user-initiated model
// listing.
func DiscoveryHeaders() http.Header {
	headers := APIHeaders()
	headers.Set("X-Initiator", "user")
	return headers
}

// NormalizeIntegrationID is normalizeCopilotIntegrationId: a trimmed value,
// empty when blank or carrying CR/LF.
func NormalizeIntegrationID(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n") {
		return ""
	}
	return trimmed
}

// IsPublicGitHubHost reports github.com and its public API hosts.
func IsPublicGitHubHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "api.github.com", "github.com", "www.github.com":
		return true
	}
	return false
}

// NormalizeDomain is normalizeDomain: the host of a URL or bare domain,
// empty when it does not parse.
func NormalizeDomain(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// NormalizeEnterpriseDomain is normalizeGitHubCopilotEnterpriseDomain: a
// GitHub Enterprise host, empty for public GitHub.
func NormalizeEnterpriseDomain(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	normalized := NormalizeDomain(trimmed)
	if normalized == "" {
		normalized = strings.ToLower(trimmed)
	}
	if IsPublicGitHubHost(normalized) {
		return ""
	}
	return normalized
}

// NormalizeAPIEndpoint is normalizeGitHubCopilotApiEndpoint: an https URL
// without trailing slashes, empty otherwise.
func NormalizeAPIEndpoint(input string) string {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "https://") {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return ""
	}
	return strings.TrimRight(trimmed, "/")
}

// BaseURL is getGitHubCopilotBaseUrl: the personal host, or the Enterprise
// instance's copilot-api host.
func BaseURL(enterpriseDomain string) string {
	domain := NormalizeEnterpriseDomain(enterpriseDomain)
	if domain == "" {
		return PersonalBaseURL
	}
	if !strings.HasPrefix(domain, "copilot-api.") {
		domain = "copilot-api." + domain
	}
	return "https://" + domain
}

// ResolveBaseURL is resolveGitHubCopilotBaseUrl: the plan endpoint the token
// was issued, else the Enterprise host, unless a custom (non-Copilot) base
// URL is configured.
func ResolveBaseURL(baseURL string, credential *Credential) string {
	if credential == nil {
		return baseURL
	}
	if credential.APIEndpoint != "" && (baseURL == "" || strings.Contains(baseURL, "githubcopilot.com")) {
		return credential.APIEndpoint
	}
	if credential.EnterpriseURL == "" {
		return baseURL
	}
	if baseURL != "" && !strings.Contains(baseURL, "githubcopilot.com") {
		return baseURL
	}
	return BaseURL(credential.EnterpriseURL)
}
