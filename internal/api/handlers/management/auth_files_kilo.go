package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	kiloauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kilo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// kiloCompatName is the openai-compatibility entry Kilo Gateway tokens live
// in; the dashboard catalogue maps it (and its -N variants) to "kilo".
const kiloCompatName = "kilo"

// kiloDeviceAuthBaseURL is where the sign-in starts and polls; tests replace it.
var kiloDeviceAuthBaseURL = kiloauth.DeviceAuthBaseURL

// RequestKiloToken starts NeoPi's Kilo Gateway device sign-in. The approved
// token is the gateway's bearer key, added to the Kilo openai-compatibility
// entry like a pasted key.
func (h *Handler) RequestKiloToken(c *gin.Context) {
	client := util.SetProxy(&h.cfg.SDKConfig, &http.Client{})
	// Do not inherit the HTTP request cancellation: the sign-in continues
	// after the code is shown.
	ctx := PopulateAuthContext(context.Background(), c)
	code, err := kiloauth.StartDeviceFlow(ctx, client, kiloDeviceAuthBaseURL)
	if err != nil {
		var loginErr *kiloauth.Error
		message := "failed to start Kilo sign-in"
		if errors.As(err, &loginErr) {
			message = loginErr.Message
		}
		log.WithError(err).Warn("Kilo sign-in: device authorization failed")
		c.JSON(http.StatusBadGateway, gin.H{"error": message})
		return
	}
	state := fmt.Sprintf("kilo-%d", time.Now().UnixNano())
	RegisterOAuthSession(state, kiloauth.Provider)
	go h.completeKiloSignIn(ctx, client, state, code)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": code.VerificationURL, "state": state, "flow": "device", "user_code": code.Code, "expires_in": code.ExpiresIn})
}

// completeKiloSignIn polls the device authorization and saves the token.
func (h *Handler) completeKiloSignIn(ctx context.Context, client *http.Client, state string, code *kiloauth.DeviceCode) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go watchOAuthSessionCancel(ctx, cancel, state, kiloauth.Provider)
	fail := func(message string) {
		if IsOAuthSessionPending(state, kiloauth.Provider) {
			SetOAuthSessionError(state, message)
		}
	}
	token, err := kiloauth.PollForToken(ctx, client, kiloDeviceAuthBaseURL, code, time.Now, kiloauth.Wait)
	if err != nil {
		var loginErr *kiloauth.Error
		if errors.As(err, &loginErr) {
			fail(loginErr.Message)
		} else {
			fail("Kilo sign-in failed")
		}
		return
	}
	if errGuard := guardOAuthSessionPendingForSave(state, kiloauth.Provider); errGuard != nil {
		return
	}
	h.mu.Lock()
	name := compatNameForBase(h.cfg, kiloCompatName, kiloauth.BaseURL)
	h.mu.Unlock()
	// The entry serves NeoPi's Kilo catalog, so no model list is stored.
	_, errCreate := h.CreateAccountAPIKey(ctx, coreauth.AccountKey{Provider: "openai-compatibility", APIKey: token, BaseURL: kiloauth.BaseURL, CompatName: name})
	switch {
	case errors.Is(errCreate, coreauth.ErrAccountKeyConflict):
		fail("This Kilo account is already in the pool.")
		return
	case errCreate != nil:
		log.WithError(errCreate).Error("Kilo sign-in: saving the token failed")
		fail("Failed to save the Kilo token")
		return
	}
	CompleteOAuthSession(state)
	log.Info("Kilo authentication successful")
}
