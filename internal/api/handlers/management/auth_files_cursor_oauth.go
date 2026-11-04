package management

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// RequestCursorToken starts Cursor's browser sign-in. The person opens the
// returned cursor.com URL; NeoProxy polls Cursor with the PKCE verifier and
// saves the session once the browser step finishes.
func (h *Handler) RequestCursorToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	session, err := cursorauth.StartLogin()
	if err != nil {
		log.Errorf("Failed to start Cursor sign-in: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start Cursor sign-in"})
		return
	}
	state := fmt.Sprintf("cursor-%d", time.Now().UnixNano())
	RegisterOAuthSession(state, "cursor")

	go func() {
		pollCtx, cancel := context.WithTimeout(ctx, cursorauth.MaxPollDuration)
		defer cancel()
		go watchOAuthSessionCancel(pollCtx, cancel, state, "cursor")

		svc := cursorauth.NewAuth(h.cfg)
		tokens, errWait := svc.WaitForLogin(pollCtx, session)
		if errWait != nil {
			if IsOAuthSessionPending(state, "cursor") {
				log.Errorf("Cursor sign-in failed: %v", errWait)
				SetOAuthSessionError(state, oauthSessionErrorWithCause("Authentication failed", errWait))
			}
			return
		}
		if !IsOAuthSessionPending(state, "cursor") {
			return
		}
		record := sdkAuth.BuildCursorAuthRecord(ctx, svc, tokens)
		if errGuard := guardOAuthSessionPendingForSave(state, "cursor"); errGuard != nil {
			return
		}
		if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
			log.Errorf("Failed to save Cursor credential: %v", errSave)
			SetOAuthSessionError(state, "Failed to save token to file")
			return
		}
		CompleteOAuthSession(state)
	}()

	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"url":        session.URL,
		"state":      state,
		"flow":       "browser",
		"expires_in": int(cursorauth.MaxPollDuration / time.Second),
	})
}
