package auth

import (
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/snowflake"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Snowflake accounts sign in from the dashboard (management v8); only their
// refresh schedule registers here: NeoPi's auth broker refreshes a credential
// DEFAULT_REFRESH_SKEW_MS before it expires.
func init() {
	cliproxyauth.RegisterRefreshLeadProvider(snowflake.Provider, func() *time.Duration {
		lead := snowflake.RefreshLead
		return &lead
	})
}
