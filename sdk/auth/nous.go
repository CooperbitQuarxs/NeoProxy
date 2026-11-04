package auth

import (
	"context"
	"errors"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/nous"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var nousRefreshLead = 5 * time.Minute

// NousAuthenticator supplies Nous Portal's refresh lead time. Sign-in runs
// through the management device flow (dashboard), not the CLI.
type NousAuthenticator struct{}

func NewNousAuthenticator() Authenticator { return NousAuthenticator{} }

func (NousAuthenticator) Provider() string { return nous.Provider }

func (NousAuthenticator) RefreshLead() *time.Duration { return &nousRefreshLead }

func (NousAuthenticator) Login(context.Context, *config.Config, *LoginOptions) (*coreauth.Auth, error) {
	return nil, errors.New("nous portal: sign in from the NeoProxy dashboard (Add provider access → Nous Portal)")
}
