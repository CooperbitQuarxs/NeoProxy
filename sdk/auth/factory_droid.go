package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// factoryDroidRefreshLead is OAUTH_REFRESH_SKEW_MS: NeoPi refreshes a
// Factory credential within a minute of its expiry.
var factoryDroidRefreshLead = factorydroid.RefreshSkew

// FactoryDroidAuthenticator runs NeoPi's Factory Droid WorkOS device-code
// sign-in.
type FactoryDroidAuthenticator struct{}

// NewFactoryDroidAuthenticator constructs the Factory Droid authenticator.
func NewFactoryDroidAuthenticator() Authenticator {
	return &FactoryDroidAuthenticator{}
}

// Provider returns the provider key for Factory Droid.
func (FactoryDroidAuthenticator) Provider() string { return factorydroid.Provider }

// RefreshLead returns the duration before expiry to refresh.
func (FactoryDroidAuthenticator) RefreshLead() *time.Duration {
	return &factoryDroidRefreshLead
}

// NewFactoryDroidAuth builds the sign-in client on the configured proxy.
func NewFactoryDroidAuth(cfg *config.Config) *factorydroid.Auth {
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		sdkCfg.ProxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	return factorydroid.NewAuth(util.SetProxy(&sdkCfg, &http.Client{}))
}

// Login runs the device-code flow and returns the signed-in account.
func (a FactoryDroidAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	authSvc := NewFactoryDroidAuth(cfg)
	fmt.Println("Starting Factory Droid authentication...")
	code, err := authSvc.StartDeviceFlow(ctx)
	if err != nil {
		return nil, fmt.Errorf("factory-droid: failed to start device flow: %w", err)
	}
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\nEnter code: %s\n\n", code.URL(), code.UserCode)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(code.URL()); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		} else {
			fmt.Println("Browser opened automatically.")
		}
	}
	fmt.Println("Waiting for authorization...")
	credential, err := authSvc.PollForToken(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("factory-droid: %w", err)
	}
	fmt.Println("\nFactory Droid authentication successful!")
	return FactoryDroidAuthRecord(credential), nil
}

// FactoryDroidAuthRecord is the auth record of a signed-in Factory account.
func FactoryDroidAuthRecord(credential *factorydroid.Credential) *coreauth.Auth {
	metadata := factorydroid.Metadata(credential)
	metadata["timestamp"] = time.Now().UnixMilli()
	fileName := fmt.Sprintf("%s-%d.json", factorydroid.Provider, time.Now().UnixMilli())
	label := credential.Email
	if label == "" {
		label = "Factory Droid User"
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: factorydroid.Provider,
		FileName: fileName,
		Label:    label,
		Storage:  factorydroid.NewTokenStorage(credential),
		Metadata: metadata,
	}
}
