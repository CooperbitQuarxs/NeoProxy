package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestAnthropicToken uses NeoPi's OAuth engine for the official Claude login.
// A busy loopback port leaves the ordinary pasted-callback endpoint available.
func (h *Handler) RequestAnthropicToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	session, err := anthropicnpi.NewSession()
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to generate OAuth session"})
		return
	}
	// Callback filenames retain the established internal anthropic name.
	const callbackProvider = "anthropic"
	RegisterOAuthSession(session.State, callbackProvider)
	bindPluginLoginOwner(ctx, session.State)
	listener, listenErr := net.Listen("tcp", "127.0.0.1:54545")
	var callback *http.Server
	if listenErr == nil {
		callback = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			query := r.URL.Query()
			if query.Get("state") != session.State {
				http.Error(w, "Invalid OAuth state", 400)
				return
			}
			if query.Get("code") == "" && query.Get("error") == "" {
				http.Error(w, "Missing OAuth code", 400)
				return
			}
			if _, err := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, callbackProvider, session.State, query.Get("code"), query.Get("error")); err != nil {
				http.Error(w, "OAuth session is unavailable", 409)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!doctype html><title>Claude sign-in</title><p>Authorization received. You can close this tab and return to NeoProxy.</p>"))
		})}
		go func() { _ = callback.Serve(listener) }()
	}
	go func() {
		if callback != nil {
			defer callback.Close()
		}
		path := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-%s-%s.oauth", callbackProvider, session.State))
		defer os.Remove(path)
		timer := time.NewTimer(5 * time.Minute)
		defer timer.Stop()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-timer.C:
				SetOAuthSessionError(session.State, "Timeout waiting for OAuth callback")
				return
			case <-ticker.C:
				if !IsOAuthSessionPending(session.State, callbackProvider) {
					return
				}
				data, err := os.ReadFile(path)
				if os.IsNotExist(err) {
					continue
				}
				var result map[string]string
				if err != nil || json.Unmarshal(data, &result) != nil {
					SetOAuthSessionError(session.State, "Invalid OAuth callback")
					return
				}
				if result["state"] != session.State || result["error"] != "" {
					SetOAuthSessionError(session.State, "OAuth authorization was rejected")
					return
				}
				credential, err := anthropicnpi.NewClient(h.cfg, "").Exchange(ctx, session, result["code"])
				if err != nil {
					log.WithError(err).Warn("Claude sign-in failed")
					SetOAuthSessionError(session.State, "Claude token exchange failed")
					return
				}
				if err = anthropicnpi.EnsureInstallID(h.cfg.AuthDir, credential); err != nil {
					SetOAuthSessionError(session.State, "Could not persist installation identity")
					return
				}
				metadata, err := anthropicnpi.CredentialMetadata(credential)
				if err != nil {
					SetOAuthSessionError(session.State, "Could not encode Claude credential")
					return
				}
				fileName := credential.FileName()
				record := &coreauth.Auth{ID: fileName, FileName: fileName, Provider: anthropicnpi.Provider, Storage: credential, Metadata: metadata}
				if guardOAuthSessionPendingForSave(session.State, callbackProvider) != nil {
					return
				}
				if _, err := h.saveTokenRecord(ctx, record); err != nil {
					SetOAuthSessionError(session.State, "Could not save Claude credential")
					return
				}
				CompleteOAuthSession(session.State)
				return
			}
		}
	}()
	c.JSON(200, gin.H{"status": "ok", "url": session.AuthURL(), "state": session.State})
}
