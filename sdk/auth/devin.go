package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// DevinAuthenticator runs NeoPi's Devin login: an authorization-code grant
// with PKCE and a uuid state through a loopback callback, with the callback
// URL or code accepted by paste as well.
type DevinAuthenticator struct {
	// CallbackPort is the preferred loopback port; another free port serves
	// when it is taken, unless the login requested a port explicitly.
	CallbackPort int
	AuthService  *devinauth.DevinAuthService
}

// NewDevinAuthenticator constructs a new Devin authenticator instance.
func NewDevinAuthenticator() *DevinAuthenticator {
	return &DevinAuthenticator{CallbackPort: devinauth.CallbackPort}
}

// Provider returns the unique provider identifier for Devin.
func (a *DevinAuthenticator) Provider() string {
	return "devin"
}

// RefreshLead returns nil: NeoPi's Devin credential has no refresh grant.
func (a *DevinAuthenticator) RefreshLead() *time.Duration {
	return nil
}

// Login executes the browser callback login, or the paste-only login when no
// browser is used.
func (a *DevinAuthenticator) Login(ctx context.Context, cfg *config.Config, opts *LoginOptions) (*coreauth.Auth, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}

	pkceCodes, errPKCE := devinauth.GeneratePKCECodes()
	if errPKCE != nil {
		return nil, fmt.Errorf("devin pkce generation failed: %w", errPKCE)
	}
	state := uuid.NewString()

	authSvc := a.AuthService
	if authSvc == nil {
		authSvc = devinauth.NewDevinAuthService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
	}
	port := a.CallbackPort
	if port <= 0 {
		port = devinauth.CallbackPort
	}
	if opts.CallbackPort > 0 {
		port = opts.CallbackPort
	}

	if opts.NoBrowser {
		if opts.Prompt == nil {
			return nil, fmt.Errorf("devin authentication in no-browser mode requires an interactive prompt")
		}
		redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", port)
		fmt.Printf("Visit the following URL to continue Devin authentication:\n%s\n\n", authSvc.BuildAuthorizationURL(redirectURI, pkceCodes.CodeChallenge, state))
		manualInputCh, manualInputErrCh := misc.AsyncPrompt(opts.Prompt, "Paste the Devin callback URL or authorization code: ")
		var authCode string
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case errInput := <-manualInputErrCh:
			return nil, fmt.Errorf("failed to read devin input: %w", errInput)
		case input := <-manualInputCh:
			code, errPaste := parseDevinManualPaste(input, state)
			if errPaste != nil {
				return nil, errPaste
			}
			if code == "" {
				return nil, fmt.Errorf("devin authentication canceled: empty input received")
			}
			authCode = code
		}
		return a.complete(ctx, authSvc, authCode, pkceCodes.CodeVerifier)
	}

	oauthServer := devinauth.NewOAuthServer(port)
	actualPort, errStart := oauthServer.Start()
	if errStart != nil && opts.CallbackPort <= 0 {
		// The rule allows another free port when the preferred one is taken.
		oauthServer = devinauth.NewOAuthServer(0)
		actualPort, errStart = oauthServer.Start()
	}
	if errStart != nil {
		return nil, fmt.Errorf("failed to start devin oauth callback server: %w", errStart)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = oauthServer.Stop(stopCtx)
	}()

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", actualPort)
	authURL := authSvc.BuildAuthorizationURL(redirectURI, pkceCodes.CodeChallenge, state)

	fmt.Println("Opening browser for Devin authentication...")
	if !browser.IsAvailable() {
		log.Warn("No browser available; please open the URL manually")
		util.PrintSSHTunnelInstructions(actualPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	} else if errOpen := browser.OpenURL(authURL); errOpen != nil {
		log.Warnf("Failed to open browser automatically: %v", errOpen)
		util.PrintSSHTunnelInstructions(actualPort)
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", authURL)
	}

	fmt.Println("Waiting for Devin authentication callback...")

	callbackCh := make(chan *devinauth.OAuthResult, 1)
	callbackErrCh := make(chan error, 1)
	go func() {
		result, errWait := oauthServer.WaitForCallbackWithContext(ctx, 5*time.Minute)
		if errWait != nil {
			select {
			case callbackErrCh <- errWait:
			case <-ctx.Done():
			}
			return
		}
		select {
		case callbackCh <- result:
		case <-ctx.Done():
		}
	}()

	var manualPromptC <-chan time.Time
	if opts.Prompt != nil {
		manualPromptTimer := time.NewTimer(5 * time.Second)
		manualPromptC = manualPromptTimer.C
		defer manualPromptTimer.Stop()
	}
	var manualInputCh <-chan string
	var manualInputErrCh <-chan error
	callbackCode := func(res *devinauth.OAuthResult) (string, error) {
		if res.Error != "" {
			return "", fmt.Errorf("devin oauth error: %s", res.Error)
		}
		if res.State != state {
			return "", fmt.Errorf("devin oauth state mismatch (possible CSRF)")
		}
		return res.Code, nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case res := <-callbackCh:
			code, errCallback := callbackCode(res)
			if errCallback != nil {
				return nil, errCallback
			}
			return a.complete(ctx, authSvc, code, pkceCodes.CodeVerifier)

		case errWait := <-callbackErrCh:
			return nil, fmt.Errorf("devin oauth callback failed: %w", errWait)

		case <-manualPromptC:
			manualPromptC = nil
			select {
			case res := <-callbackCh:
				code, errCallback := callbackCode(res)
				if errCallback != nil {
					return nil, errCallback
				}
				return a.complete(ctx, authSvc, code, pkceCodes.CodeVerifier)
			default:
			}
			manualInputCh, manualInputErrCh = misc.AsyncPrompt(
				opts.Prompt,
				"Paste the Devin callback URL or authorization code (or press Enter to keep waiting): ",
			)

		case input := <-manualInputCh:
			manualInputCh, manualInputErrCh = nil, nil
			code, errPaste := parseDevinManualPaste(input, state)
			if errPaste != nil {
				if errors.Is(errPaste, errDevinUnrecognizedPaste) {
					continue
				}
				return nil, errPaste
			}
			if code != "" {
				return a.complete(ctx, authSvc, code, pkceCodes.CodeVerifier)
			}

		case errInput := <-manualInputErrCh:
			manualInputCh, manualInputErrCh = nil, nil
			if errInput != nil {
				log.Debugf("manual input prompt error: %v", errInput)
			}
		}
	}
}

// complete exchanges the code and stores the credential.
func (a *DevinAuthenticator) complete(ctx context.Context, authSvc *devinauth.DevinAuthService, code, verifier string) (*coreauth.Auth, error) {
	credential, errExchange := authSvc.ExchangeCodeForToken(ctx, code, verifier)
	if errExchange != nil {
		return nil, fmt.Errorf("failed to exchange devin authorization code: %w", errExchange)
	}
	return authSvc.CreateAuthRecord(ctx, credential)
}

var errDevinUnrecognizedPaste = errors.New("unrecognized devin callback URL or authorization code")

// parseDevinManualPaste reads a pasted callback URL or bare authorization
// code. Empty input returns an empty code with a nil error; callers decide
// whether to abort or keep waiting.
func parseDevinManualPaste(input, expectedState string) (string, error) {
	trimmed := strings.TrimSpace(strings.Trim(strings.TrimSpace(input), "\"'"))
	if trimmed == "" {
		return "", nil
	}
	if parsed, errParse := misc.ParseOAuthCallback(trimmed); errParse == nil && parsed != nil {
		if errMsg := strings.TrimSpace(parsed.Error); errMsg != "" {
			if desc := strings.TrimSpace(parsed.ErrorDescription); desc != "" {
				errMsg = fmt.Sprintf("%s: %s", errMsg, desc)
			}
			return "", fmt.Errorf("devin oauth error: %s", errMsg)
		}
		if parsed.Code != "" {
			// A pasted URL's state must match; PKCE still protects a bare code.
			if parsed.State != "" && parsed.State != expectedState {
				return "", fmt.Errorf("devin oauth state mismatch (possible CSRF)")
			}
			return parsed.Code, nil
		}
	}
	if !strings.ContainsAny(trimmed, " \t\r\n/?=") {
		return trimmed, nil
	}
	return "", errDevinUnrecognizedPaste
}
