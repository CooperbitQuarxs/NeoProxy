package auth

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/githubcopilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// GitHubCopilotAuthenticator runs NeoPi's GitHub Copilot device-code
// sign-in.
type GitHubCopilotAuthenticator struct{}

// NewGitHubCopilotAuthenticator constructs the GitHub Copilot authenticator.
func NewGitHubCopilotAuthenticator() Authenticator {
	return &GitHubCopilotAuthenticator{}
}

// Provider returns the provider key for GitHub Copilot.
func (GitHubCopilotAuthenticator) Provider() string { return githubcopilot.Provider }

// RefreshLead is nil: the GitHub token is long-lived and NeoPi's refresh hook
// returns it unchanged.
func (GitHubCopilotAuthenticator) RefreshLead() *time.Duration { return nil }

// NewGitHubCopilotAuth builds the sign-in client on the configured proxy.
func NewGitHubCopilotAuth(cfg *config.Config) *githubcopilot.Auth {
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		sdkCfg.ProxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	return githubcopilot.NewAuth(util.SetProxy(&sdkCfg, &http.Client{}))
}

// GitHubCopilotEnterpriseMetadataKey is the login metadata key naming the
// GitHub Enterprise URL or domain (blank for github.com).
const GitHubCopilotEnterpriseMetadataKey = "enterprise_url"

// Login runs the device-code flow, enables the models a sign-in enables and
// returns the signed-in account.
func (a GitHubCopilotAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	enterprise, ok := opts.Metadata[GitHubCopilotEnterpriseMetadataKey]
	if !ok && opts.Prompt != nil {
		var err error
		if enterprise, err = opts.Prompt("GitHub Enterprise URL/domain (blank for github.com): "); err != nil {
			return nil, err
		}
	}
	signIn := NewGitHubCopilotAuth(cfg)
	fmt.Println("Starting GitHub Copilot authentication...")
	code, err := signIn.StartDeviceFlow(ctx, enterprise)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: failed to start device flow: %w", err)
	}
	fmt.Printf("\nTo authenticate, please visit:\n%s\n\nEnter code: %s\n\n", code.VerificationURI, code.UserCode)
	if !opts.NoBrowser && browser.IsAvailable() {
		if errOpen := browser.OpenURL(code.VerificationURI); errOpen != nil {
			log.Warnf("Failed to open browser automatically: %v", errOpen)
		}
	}
	fmt.Println("Waiting for authorization...")
	credential, err := signIn.PollForToken(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("github-copilot: %w", err)
	}
	fmt.Println("Enabling models...")
	EnableGitHubCopilotModels(ctx, signIn, credential)
	fmt.Println("\nGitHub Copilot authentication successful!")
	return GitHubCopilotAuthRecord(credential), nil
}

// EnableGitHubCopilotModels enables the policy of every model NeoPi enables
// after sign-in, as the chat surface unless COPILOT_INTEGRATION_ID pins
// another identity.
func EnableGitHubCopilotModels(ctx context.Context, signIn *githubcopilot.Auth, credential *githubcopilot.Credential) {
	signIn.EnableModels(ctx, credential, registry.GitHubCopilotPolicyModelIDs(), os.Getenv("COPILOT_INTEGRATION_ID"), func(model string, ok bool) {
		if !ok {
			log.Debugf("github-copilot: model policy for %s was not enabled", model)
		}
	})
}

// GitHubCopilotAuthRecord is the auth record of a signed-in Copilot account.
func GitHubCopilotAuthRecord(credential *githubcopilot.Credential) *coreauth.Auth {
	metadata := githubcopilot.Metadata(credential)
	metadata["timestamp"] = time.Now().UnixMilli()
	fileName := fmt.Sprintf("%s-%d.json", githubcopilot.Provider, time.Now().UnixMilli())
	label := "GitHub Copilot User"
	if credential.EnterpriseURL != "" {
		label = "GitHub Copilot (" + credential.EnterpriseURL + ")"
	}
	return &coreauth.Auth{
		ID:       fileName,
		Provider: githubcopilot.Provider,
		FileName: fileName,
		Label:    label,
		Storage:  githubcopilot.NewTokenStorage(credential),
		Metadata: metadata,
	}
}
