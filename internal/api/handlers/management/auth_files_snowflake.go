package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/snowflake"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestSnowflakeToken is loginSnowflake: the account named by the account
// query parameter, then an authorization-code + PKCE sign-in whose callback
// listens on 127.0.0.1:54551 (any free loopback port when that is taken, as
// NeoPi falls back). The pasted-callback endpoint finishes it otherwise.
func (h *Handler) RequestSnowflakeToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	listener, listenErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", snowflake.CallbackPort))
	if listenErr != nil {
		listener, listenErr = net.Listen("tcp", "127.0.0.1:0")
	}
	port := snowflake.CallbackPort
	if listenErr == nil {
		port = listener.Addr().(*net.TCPAddr).Port
	}
	session, err := snowflake.NewSession(c.Query("account"), port)
	if err != nil {
		if listener != nil {
			_ = listener.Close()
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	const callbackProvider = snowflake.Provider
	RegisterOAuthSession(session.State, callbackProvider)
	bindPluginLoginOwner(ctx, session.State)
	var callback *http.Server
	if listenErr == nil {
		callback = &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			query := r.URL.Query()
			if query.Get("state") != session.State {
				http.Error(w, "Invalid OAuth state", http.StatusBadRequest)
				return
			}
			if query.Get("code") == "" && query.Get("error") == "" {
				http.Error(w, "Missing OAuth code", http.StatusBadRequest)
				return
			}
			if _, err := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, callbackProvider, session.State, query.Get("code"), query.Get("error")); err != nil {
				http.Error(w, "OAuth session is unavailable", http.StatusConflict)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte("<!doctype html><title>Snowflake sign-in</title><p>Authorization received. You can close this tab and return to NeoProxy.</p>"))
		})}
		go func() { _ = callback.Serve(listener) }()
	}
	go func() {
		if callback != nil {
			defer func() { _ = callback.Close() }()
		}
		path := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-%s-%s.oauth", callbackProvider, session.State))
		defer func() { _ = os.Remove(path) }()
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
				client := util.SetProxy(&h.cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
				tokens, err := session.Exchange(ctx, client, result["code"])
				if err != nil {
					log.WithError(err).Warn("Snowflake sign-in failed")
					SetOAuthSessionError(session.State, err.Error())
					return
				}
				host := session.AccountURL
				if parsed, parseErr := url.Parse(session.AccountURL); parseErr == nil {
					host = parsed.Hostname()
				}
				// The session state is random per sign-in, so records of users
				// signing in to one account at once never share a name.
				fileName := fmt.Sprintf("snowflake-%s-%s.json", host, session.State)
				record := &coreauth.Auth{ID: fileName, FileName: fileName, Provider: snowflake.Provider, Label: host, Metadata: snowflake.Metadata(tokens, session.AccountURL)}
				if guardOAuthSessionPendingForSave(session.State, callbackProvider) != nil {
					return
				}
				if _, err := h.saveTokenRecord(ctx, record); err != nil {
					SetOAuthSessionError(session.State, "Could not save Snowflake credential")
					return
				}
				CompleteOAuthSession(session.State)
				return
			}
		}
	}()
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": session.AuthURL(), "state": session.State})
}
