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
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestAnthropicNPIToken owns an independent login session and auth-file type.
// A busy loopback port never replaces another provider's callback listener: the
// ordinary pasted-callback endpoint remains available in that case.
func (h *Handler) RequestAnthropicNPIToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	session, err := anthropicnpi.NewSession()
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to generate OAuth session"})
		return
	}
	RegisterOAuthSession(session.State, anthropicnpi.Provider)
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
			if _, err := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, anthropicnpi.Provider, session.State, query.Get("code"), query.Get("error")); err != nil {
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
		path := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-%s-%s.oauth", anthropicnpi.Provider, session.State))
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
				if !IsOAuthSessionPending(session.State, anthropicnpi.Provider) {
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
					log.WithError(err).Warn("anthropic-npi sign-in failed")
					SetOAuthSessionError(session.State, "Claude (NeoPi flow) token exchange failed")
					return
				}
				installPath := filepath.Join(h.cfg.AuthDir, ".anthropic-npi-install-id")
				installID, err := neoPiInstallID(installPath)
				if err != nil {
					SetOAuthSessionError(session.State, "Could not persist installation identity")
					return
				}
				credential.InstallID = installID
				metadataBytes, _ := json.Marshal(credential)
				var metadata map[string]any
				_ = json.Unmarshal(metadataBytes, &metadata)
				fileName := credential.FileName()
				record := &coreauth.Auth{ID: fileName, FileName: fileName, Provider: anthropicnpi.Provider, Prefix: anthropicnpi.Provider, Storage: credential, Metadata: metadata}
				if guardOAuthSessionPendingForSave(session.State, anthropicnpi.Provider) != nil {
					return
				}
				if _, err := h.saveTokenRecord(ctx, record); err != nil {
					SetOAuthSessionError(session.State, "Could not save Claude (NeoPi flow) credential")
					return
				}
				CompleteOAuthSession(session.State)
				return
			}
		}
	}()
	c.JSON(200, gin.H{"status": "ok", "url": session.AuthURL(), "state": session.State, "manual_callback": listenErr != nil,
		"instructions": "Complete login in your browser. If the browser cannot reach this machine, paste the final redirect URL or authorization code."})
}

func neoPiInstallID(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		return string(data), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		data, readErr := os.ReadFile(path)
		return string(data), readErr
	}
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	_, writeErr := file.WriteString(id)
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	return id, closeErr
}
