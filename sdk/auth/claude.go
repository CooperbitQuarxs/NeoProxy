package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// ClaudeAuthenticator implements the OAuth login flow for Anthropic Claude accounts.
type ClaudeAuthenticator struct {
	CallbackPort int
}

// NewClaudeAuthenticator constructs a Claude authenticator with default settings.
func NewClaudeAuthenticator() *ClaudeAuthenticator {
	return &ClaudeAuthenticator{CallbackPort: 54545}
}

func (a *ClaudeAuthenticator) Provider() string {
	return "claude"
}

func (a *ClaudeAuthenticator) RefreshLead() *time.Duration {
	lead := anthropicnpi.RefreshLead
	return &lead
}

func (a *ClaudeAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	callbackPort := a.CallbackPort
	if opts.CallbackPort > 0 {
		callbackPort = opts.CallbackPort
	}

	session, err := anthropicnpi.NewSession()
	if err != nil {
		return nil, fmt.Errorf("claude OAuth session generation failed: %w", err)
	}
	session.Redirect = fmt.Sprintf("http://localhost:%d/callback", callbackPort)
	state := session.State

	oauthServer := claude.NewOAuthServer(callbackPort)
	started := oauthServer.Start() == nil
	if !started && opts.Prompt == nil {
		return nil, fmt.Errorf("claude callback listener unavailable; use a prompt-enabled login for pasted authorization codes")
	}
	defer func() {
		if !started {
			return
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if stopErr := oauthServer.Stop(stopCtx); stopErr != nil {
			log.Warnf("claude oauth server stop error: %v", stopErr)
		}
	}()

	authURL := session.AuthURL()

	if !opts.NoBrowser {
		fmt.Println("Opening browser for Claude authentication")
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		} else if err = browser.OpenURL(authURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			util.PrintSSHTunnelInstructions(callbackPort)
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
		}
	} else {
		util.PrintSSHTunnelInstructions(callbackPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Claude authentication callback...")

	callbackCh := make(chan *claude.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)
	manualDescription := ""

	go func() {
		if !started {
			return
		}
		result, errWait := oauthServer.WaitForCallback(5 * time.Minute)
		if errWait != nil {
			callbackErrCh <- errWait
			return
		}
		callbackCh <- result
	}()

	var result *claude.OAuthResult
	var manualPromptTimer *time.Timer
	var manualPromptC <-chan time.Time
	if opts.Prompt != nil {
		manualPromptTimer = time.NewTimer(15 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}

	// A pasted-code-only flow has the same bounded lifetime as loopback.
	loginTimeout := time.NewTimer(5 * time.Minute)
	defer loginTimeout.Stop()

	var manualInputCh <-chan string
	var manualInputErrCh <-chan error

waitForCallback:
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-loginTimeout.C:
			return nil, claude.NewAuthenticationError(claude.ErrCallbackTimeout, fmt.Errorf("timeout waiting for Claude authorization"))
		case result = <-callbackCh:
			break waitForCallback
		case err = <-callbackErrCh:
			if strings.Contains(err.Error(), "timeout") {
				return nil, claude.NewAuthenticationError(claude.ErrCallbackTimeout, err)
			}
			return nil, err
		case <-manualPromptC:
			manualPromptC = nil
			if manualPromptTimer != nil {
				manualPromptTimer.Stop()
			}
			select {
			case result = <-callbackCh:
				break waitForCallback
			case err = <-callbackErrCh:
				if strings.Contains(err.Error(), "timeout") {
					return nil, claude.NewAuthenticationError(claude.ErrCallbackTimeout, err)
				}
				return nil, err
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(opts.Prompt, "Paste the Claude callback URL or authorization code (or press Enter to keep waiting): ")
			continue
		case input := <-manualInputCh:
			manualInputCh = nil
			manualInputErrCh = nil
			input = strings.TrimSpace(input)
			var parsed *misc.OAuthCallback
			var errParse error
			if input != "" && !strings.Contains(input, "://") && !strings.Contains(input, "?") {
				code, pastedState, _ := strings.Cut(input, "#")
				if pastedState == "" {
					pastedState = state
				}
				parsed = &misc.OAuthCallback{Code: code, State: pastedState}
			} else {
				parsed, errParse = misc.ParseOAuthCallback(input)
			}
			if errParse != nil {
				return nil, errParse
			}
			if parsed == nil {
				manualPromptTimer.Reset(15 * time.Second)
				manualPromptC = manualPromptTimer.C
				continue
			}
			manualDescription = parsed.ErrorDescription
			result = &claude.OAuthResult{
				Code:  parsed.Code,
				State: parsed.State,
				Error: parsed.Error,
			}
			break waitForCallback
		case errManual := <-manualInputErrCh:
			return nil, errManual
		}
	}

	if result.Error != "" {
		return nil, claude.NewOAuthError(result.Error, manualDescription, http.StatusBadRequest)
	}

	if result.State != state {
		return nil, claude.NewAuthenticationError(claude.ErrInvalidState, fmt.Errorf("state mismatch"))
	}

	tokenStorage, err := anthropicnpi.NewClient(cfg, "").Exchange(ctx, session, result.Code)
	if err != nil {
		log.Errorf("Token exchange failed: %v", err)
		return nil, claude.NewAuthenticationError(claude.ErrCodeExchangeFailed, err)
	}

	if err = anthropicnpi.EnsureInstallID(cfg.AuthDir, tokenStorage); err != nil {
		return nil, fmt.Errorf("persist Claude installation identity: %w", err)
	}

	if tokenStorage == nil || tokenStorage.Email == "" {
		return nil, fmt.Errorf("claude token storage missing account information")
	}

	fileName := tokenStorage.FileName()
	metadata, err := anthropicnpi.CredentialMetadata(tokenStorage)
	if err != nil {
		return nil, err
	}

	fmt.Println("Claude authentication successful")

	return &coreauth.Auth{
		ID:       fileName,
		Provider: a.Provider(),
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: metadata,
	}, nil
}
