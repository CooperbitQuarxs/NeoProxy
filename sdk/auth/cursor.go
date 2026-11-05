package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// CursorAuthenticator runs NeoPi's Cursor browser sign-in, polled with a PKCE verifier.
type CursorAuthenticator struct{}

// NewCursorAuthenticator constructs a Cursor authenticator.
func NewCursorAuthenticator() Authenticator { return &CursorAuthenticator{} }

// Provider returns the provider key for Cursor.
func (CursorAuthenticator) Provider() string { return "cursor" }

// RefreshLead is NeoPi's refresh skew against the stored expiry.
func (CursorAuthenticator) RefreshLead() *time.Duration {
	lead := cursorauth.RefreshLead
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
	fmt.Println("Waiting for browser authentication...")
	pollCtx, cancel := context.WithTimeout(ctx, cursorauth.MaxPollDuration)
	defer cancel()
	credential, err := cursorauth.NewAuth(cfg).Login(pollCtx, session)
	if err != nil {
		return nil, err
	}
	return BuildCursorAuthRecord(credential), nil
}

// BuildCursorAuthRecord turns a finished sign-in into an engine auth record,
// named after the account email when Cursor's profile reports one.
func BuildCursorAuthRecord(credential *cursorauth.Credential) *coreauth.Auth {
	fileName := cursorauth.CredentialFileName(credential.Email, cursorauth.UserID(credential.Access))
	label := strings.TrimSpace(credential.Email)
	if label == "" {
		label = "Cursor"
	}
	return &coreauth.Auth{
		ID:         fileName,
		Provider:   "cursor",
		FileName:   fileName,
		Label:      label,
		Metadata:   cursorauth.Metadata(credential),
		Attributes: map[string]string{"auth_kind": "oauth", "base_url": cursorwire.DefaultBaseURL},
	}
}
