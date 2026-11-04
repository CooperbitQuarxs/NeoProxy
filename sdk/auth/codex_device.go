package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	codexLoginModeMetadataKey = "codex_login_mode"
	codexLoginModeDevice      = "device"
)

func shouldUseCodexDeviceFlow(opts *LoginOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(opts.Metadata[codexLoginModeMetadataKey]), codexLoginModeDevice)
}

func (a *CodexAuthenticator) loginWithDeviceFlow(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	authSvc := codex.NewCodexAuth(cfg)
	session, err := authSvc.StartDevice(ctx)
	if err != nil {
		return nil, err
	}
	fmt.Println("Starting Codex device authentication...")
	fmt.Printf("Codex device URL: %s\n", session.URL)
	fmt.Printf("Codex device code: %s\n", session.UserCode)

	if !opts.NoBrowser {
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the device URL manually")
		} else if errOpen := browser.OpenURL(session.URL); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		}
	}

	authBundle, err := authSvc.WaitForDevice(ctx, session)
	if err != nil {
		return nil, err
	}

	return a.buildAuthRecord(authSvc, authBundle)
}

func (a *CodexAuthenticator) buildAuthRecord(authSvc *codex.CodexAuth, authBundle *codex.CodexAuthBundle) (*coreauth.Auth, error) {
	tokenStorage := authSvc.CreateTokenStorage(authBundle)

	if tokenStorage == nil || (tokenStorage.Email == "" && tokenStorage.AccountID == "") {
		return nil, fmt.Errorf("codex token storage missing account information")
	}

	planType := tokenStorage.PlanType
	hashAccountID := ""
	if tokenStorage.AccountID != "" {
		digest := sha256.Sum256([]byte(tokenStorage.AccountID))
		hashAccountID = hex.EncodeToString(digest[:])[:8]
	}

	fileName := codex.CredentialFileName(tokenStorage.Email, planType, hashAccountID, true)
	metadata := map[string]any{
		"email":      tokenStorage.Email,
		"account_id": tokenStorage.AccountID,
		"plan_type":  planType,
	}

	fmt.Println("Codex authentication successful")
	if authBundle.APIKey != "" {
		fmt.Println("Codex API key obtained and stored")
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
		Attributes: map[string]string{
			"plan_type": planType,
		},
	}, nil
}
