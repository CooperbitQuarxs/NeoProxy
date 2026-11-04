package auth

import (
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func init() {
	registerRefreshLead("codex", func() Authenticator { return NewCodexAuthenticator() })
	registerRefreshLead("claude", func() Authenticator { return NewClaudeAuthenticator() })
	cliproxyauth.RegisterRefreshLeadProvider("anthropic-npi", func() *time.Duration {
		lead := time.Minute
		return &lead
	})
	registerRefreshLead("antigravity", func() Authenticator { return NewAntigravityAuthenticator() })
	registerRefreshLead("kimi", func() Authenticator { return NewKimiAuthenticator() })
	registerRefreshLead("kimi-ai", func() Authenticator { return NewKimiAIAuthenticator() })
	registerRefreshLead("kimi.ai", func() Authenticator { return NewKimiAIDotAuthenticator() })
	registerRefreshLead("xai", func() Authenticator { return NewXAIAuthenticator() })
	registerRefreshLead("grok-build", func() Authenticator { return NewXAIAuthenticator() })
	registerRefreshLead("devin", func() Authenticator { return NewDevinAuthenticator() })
	registerRefreshLead("meta", func() Authenticator { return NewMetaAuthenticator() })
	registerRefreshLead("cursor", func() Authenticator { return NewCursorAuthenticator() })
	registerRefreshLead("nous-portal", func() Authenticator { return NewNousAuthenticator() })
}

func registerRefreshLead(provider string, factory func() Authenticator) {
	cliproxyauth.RegisterRefreshLeadProvider(provider, func() *time.Duration {
		if factory == nil {
			return nil
		}
		auth := factory()
		if auth == nil {
			return nil
		}
		return auth.RefreshLead()
	})
}
