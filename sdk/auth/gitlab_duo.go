package auth

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/gitlabduo"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// GitLab Duo accounts sign in from the dashboard (management v8); only their
// refresh schedule registers here: NeoPi's auth broker refreshes a credential
// DEFAULT_REFRESH_SKEW_MS before it expires.
func init() {
	cliproxyauth.RegisterRefreshLeadProvider(gitlabduo.Provider, func() *time.Duration {
		lead := 5 * time.Minute
		return &lead
	})
}
