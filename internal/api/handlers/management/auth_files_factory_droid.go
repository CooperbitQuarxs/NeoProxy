package management

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// newFactoryDroidAuth builds the sign-in client; tests replace it.
var newFactoryDroidAuth = sdkAuth.NewFactoryDroidAuth

// RequestFactoryDroidToken starts NeoPi's Factory Droid WorkOS device-code
// sign-in.
func (h *Handler) RequestFactoryDroidToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	const providerName = factorydroid.Provider

	state := fmt.Sprintf("fdr-%d", time.Now().UnixNano())
	signIn := newFactoryDroidAuth(h.cfg)
	code, errStart := signIn.StartDeviceFlow(ctx)
	if errStart != nil {
		log.Errorf("Failed to start Factory Droid sign-in: %v", errStart)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate authorization url"})
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
			log.Warnf("Factory Droid authentication failed: %v", errPoll)
			return
		}
		if !IsOAuthSessionPending(state, providerName) {
			return
		}
		record := sdkAuth.FactoryDroidAuthRecord(credential)
		if errGuard := guardOAuthSessionPendingForSave(state, providerName); errGuard != nil {
			return
		}
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			log.Errorf("Failed to save Factory Droid tokens: %v", errSave)
			SetOAuthSessionError(state, "Failed to save authentication tokens")
			return
		}
		CompleteOAuthSession(state)
	}()

	response := gin.H{"status": "ok", "url": code.URL(), "state": state, "flow": "device", "user_code": code.UserCode}
	if code.ExpiresIn > 0 {
		response["expires_in"] = code.ExpiresIn
	}
	c.JSON(http.StatusOK, response)
}
