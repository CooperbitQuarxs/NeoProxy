package codex

import "testing"

// Account endpoints live only on the canonical ChatGPT origins; a streaming
// proxy base URL must not receive the bearer (normalizeCodexBaseUrl, #3679).
func TestUsageURLKeepsAccountRequestsOnChatGPTOrigin(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"", "https://chatgpt.com/backend-api/wham/usage"},
		{"https://chatgpt.com/backend-api/codex/responses/", "https://chatgpt.com/backend-api/wham/usage"},
		{"https://chat.openai.com", "https://chat.openai.com/backend-api/wham/usage"},
		{"https://proxy.example/backend-api", "https://chatgpt.com/backend-api/wham/usage"},
		{"chatgpt.com/backend-api", "https://chatgpt.com/backend-api/wham/usage"},
	} {
		if got := UsageURL(tc.base); got != tc.want {
			t.Errorf("UsageURL(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}
