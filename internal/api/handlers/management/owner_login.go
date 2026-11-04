package management

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func inheritAccountLoginOwner(ctx, source context.Context) context.Context {
	if owner := coreauth.AccountLoginOwner(source); owner != "" {
		return coreauth.WithAccountLoginOwner(ctx, owner)
	}
	if p, ok := neo.PrincipalFromContext(source); ok && p.Session && (p.Role == "admin" || p.Role == "owner") {
		return coreauth.WithAccountLoginOwner(ctx, p.UserID)
	}
	return ctx
}

func bindPluginLoginOwner(ctx context.Context, state string) {
	oauthSessions.mu.Lock()
	defer oauthSessions.mu.Unlock()
	session, ok := oauthSessions.sessions[state]
	if !ok {
		return
	}
	session.AccountOwnerID = coreauth.AccountLoginOwner(ctx)
	oauthSessions.sessions[state] = session
}

func oauthInitiatingOwner(ctx context.Context, state string) context.Context {
	session, ok := oauthSessions.Get(state)
	if !ok {
		return coreauth.WithAccountLoginOwner(ctx, "")
	}
	return coreauth.WithAccountLoginOwner(ctx, session.AccountOwnerID)
}

// SetAccountOwnershipHook installs the SQLite ownership check independently of
// application post-auth hooks. Configure once before accepting login requests.
func (h *Handler) SetAccountOwnershipHook(hook coreauth.PostAuthHook) { h.accountOwnershipHook = hook }
