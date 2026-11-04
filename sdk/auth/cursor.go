package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// CursorAuthenticator implements Cursor's browser sign-in, polled with a PKCE verifier.
type CursorAuthenticator struct{}

// NewCursorAuthenticator constructs a Cursor authenticator.
func NewCursorAuthenticator() Authenticator { return &CursorAuthenticator{} }

// Provider returns the provider key for Cursor.
func (CursorAuthenticator) Provider() string { return "cursor" }

// RefreshLead instructs the manager to renew before the access token expires.
func (CursorAuthenticator) RefreshLead() *time.Duration {
	lead := cursorauth.RefreshLead()
	return &lead
}

// Login opens Cursor's sign-in page and waits for the browser step.
func (a CursorAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	session, err := cursorauth.StartLogin()
	if err != nil {
		return nil, err
	}
	fmt.Printf("\nTo sign in to Cursor, open:\n%s\n\n", session.URL)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(session.URL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		}
	}
	fmt.Println("Waiting for Cursor sign-in...")
	svc := cursorauth.NewAuth(cfg)
	pollCtx, cancel := context.WithTimeout(ctx, cursorauth.MaxPollDuration)
	defer cancel()
	tokens, err := svc.WaitForLogin(pollCtx, session)
	if err != nil {
		return nil, err
	}
	return BuildCursorAuthRecord(ctx, svc, tokens), nil
}

// BuildCursorAuthRecord turns a finished sign-in into an engine auth record,
// naming it after the account email when cursor.com reports one.
func BuildCursorAuthRecord(ctx context.Context, svc *cursorauth.Auth, tokens *cursorauth.Tokens) *coreauth.Auth {
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	email, _ := svc.FetchEmail(lookupCtx, tokens.AccessToken)
	cancel()
	userID := cursorauth.UserID(tokens.AccessToken)
	fileName := cursorauth.CredentialFileName(email, userID)
	label := strings.TrimSpace(email)
	if label == "" {
		label = "Cursor"
	}
	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "cursor",
		FileName:   fileName,
		Label:      label,
		Metadata:   cursorauth.Metadata(tokens, email),
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": cursorauth.APIBaseURL},
	}
}
