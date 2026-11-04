package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// kimiRefreshLead keeps the proactive refresh loop ahead of the stored expiry
// (already five minutes early, as NeoPi stores it); NeoPi itself refreshes
// once that expiry passes, just before the next request.
var kimiRefreshLead = 5 * time.Minute

// KimiAuthenticator runs NeoPi's Kimi Code device-code sign-in.
type KimiAuthenticator struct{}

// NewKimiAuthenticator constructs the Kimi Code authenticator.
func NewKimiAuthenticator() Authenticator {
	return &KimiAuthenticator{}
}

// Provider returns the provider key for Kimi Code.
func (KimiAuthenticator) Provider() string { return kimi.Type }

// RefreshLead returns the duration before the stored expiry to refresh.
func (KimiAuthenticator) RefreshLead() *time.Duration {
	return &kimiRefreshLead
}

// Login runs the device-code flow and returns the signed-in account.
func (a KimiAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	authSvc := kimi.NewAuth(cfg, "")
	fmt.Println("Starting Kimi authentication...")
	deviceCode, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("kimi: failed to start device flow: %w", err)
	}
	verificationURL := deviceCode.VerificationURIComplete
	if verificationURL == "" {
		verificationURL = deviceCode.VerificationURI
	}
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\n", verificationURL)
	if deviceCode.UserCode != "" {
		fmt.Printf("User code: %s\n\n", deviceCode.UserCode)
	}
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(verificationURL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		} else {
			fmt.Println("Browser opened automatically.")
		}
	}
	fmt.Println("Waiting for authorization...")
	credential, err := authSvc.PollForToken(ctx, deviceCode)
	if err != nil {
		return nil, fmt.Errorf("kimi: %w", err)
	}
	fmt.Println("\nKimi authentication successful!")
	return KimiAuthRecord(credential), nil
}

// KimiAuthRecord is the auth record of a signed-in Kimi Code account.
func KimiAuthRecord(credential *kimi.Credential) *coreauth.Auth {
	storage := kimi.NewTokenStorage(credential)
	metadata := map[string]any{
		"type":          kimi.Type,
		"access_token":  storage.AccessToken,
		"refresh_token": storage.RefreshToken,
		"expired":       storage.Expired,
		"timestamp":     time.Now().UnixMilli(),
	}
	if storage.AccountID != "" {
		metadata["account_id"] = storage.AccountID
	}
	fileName := fmt.Sprintf("%s-%d.json", kimi.Type, time.Now().UnixMilli())
	return &coreauth.Auth{
		ID:       fileName,
		Provider: kimi.Type,
		FileName: fileName,
		Label:    "Kimi User",
		Storage:  storage,
		Metadata: metadata,
	}
}
