package management

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/githubcopilot"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// newGitHubCopilotAuth builds the sign-in client; tests replace it.
var newGitHubCopilotAuth = sdkAuth.NewGitHubCopilotAuth

// RequestGitHubCopilotToken starts NeoPi's GitHub Copilot device-code
// sign-in on github.com, or on the GitHub Enterprise instance the
// enterprise_url query names.
func (h *Handler) RequestGitHubCopilotToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	const providerName = githubcopilot.Provider

	state := fmt.Sprintf("ghc-%d", time.Now().UnixNano())
	signIn := newGitHubCopilotAuth(h.cfg)
	code, errStart := signIn.StartDeviceFlow(ctx, c.Query(sdkAuth.GitHubCopilotEnterpriseMetadataKey))
	if errStart != nil {
		log.Errorf("Failed to start GitHub Copilot sign-in: %v", errStart)
		c.JSON(http.StatusBadRequest, gin.H{"error": errStart.Error()})
		return
	}

	RegisterOAuthSession(state, providerName)

	go func() {
		pollCtx, cancelPoll := context.WithCancel(ctx)
		defer cancelPoll()
		go watchOAuthSessionCancel(pollCtx, cancelPoll, state, providerName)

		credential, errPoll := signIn.PollForToken(pollCtx, code)
		if errPoll != nil {
			if !IsOAuthSessionPending(state, providerName) {
				return
			}
			SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errPoll))
			log.Warnf("GitHub Copilot authentication failed: %v", errPoll)
			return
		}
		if !IsOAuthSessionPending(state, providerName) {
			return
		}
		// NeoPi enables every model's policy before the sign-in completes.
		sdkAuth.EnableGitHubCopilotModels(pollCtx, signIn, credential)
		record := sdkAuth.GitHubCopilotAuthRecord(credential)
		if errGuard := guardOAuthSessionPendingForSave(state, providerName); errGuard != nil {
			return
		}
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			log.Errorf("Failed to save GitHub Copilot tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		CompleteOAuthSession(state)
	}()

	response := gin.H{"status": "ok", "url": code.VerificationURI, "state": state, "flow": "device", "user_code": code.UserCode}
	if code.ExpiresIn > 0 {
		response["expires_in"] = code.ExpiresIn
	}
	c.JSON(http.StatusOK, response)
}
