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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/gitlabduo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// RequestGitLabDuoToken is NeoPi's gitlab-duo sign-in: an authorization-code
// + PKCE login at gitlab.com whose callback listens on localhost:8080/callback
// (both loopback families; any free port when that one is taken). A
// GITLAB_REDIRECT_URI override is advertised verbatim and a taken pinned
// port fails the sign-in, as NeoPi does. The pasted-callback endpoint
// finishes a sign-in whose redirect does not reach this listener.
func (h *Handler) RequestGitLabDuoToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	callback, err := gitlabduo.ResolveCallback()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	listeners, redirectURI, err := callback.Listen()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	session, err := gitlabduo.Duo.NewSession(redirectURI)
	if err != nil {
		for _, listener := range listeners {
			_ = listener.Close()
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start GitLab sign-in"})
		return
	}
	const callbackProvider = gitlabduo.Provider
	RegisterOAuthSession(session.State, callbackProvider)
	bindPluginLoginOwner(ctx, session.State)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.EscapedPath() != callback.Path {
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
		if _, errWrite := WriteOAuthCallbackFileForPendingSession(h.cfg.AuthDir, callbackProvider, session.State, query.Get("code"), query.Get("error")); errWrite != nil {
			http.Error(w, "OAuth session is unavailable", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>GitLab sign-in</title><p>Authorization received. You can close this tab and return to NeoProxy.</p>"))
	})}
	for _, listener := range listeners {
		go func(listener net.Listener) { _ = server.Serve(listener) }(listener)
	}
	go h.awaitGitLabSignIn(ctx, gitlabduo.Duo, session, "GitLab Duo", func() { _ = server.Close() })
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": session.AuthURL(), "state": session.State})
}

// RequestGitLabDuoAgentToken is NeoPi's gitlab-duo-agent sign-in: an
// authorization-code + PKCE login with GitLab's VS Code application, whose
// registered redirect opens VS Code. Nothing listens (callback manual-only):
// the person pastes that vscode:// address back, and the pasted-callback
// endpoint finishes the sign-in.
func (h *Handler) RequestGitLabDuoAgentToken(c *gin.Context) {
	ctx := PopulateAuthContext(context.Background(), c)
	session, err := gitlabduo.Agent.NewSession(gitlabduo.AgentRedirectURI)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start GitLab sign-in"})
		return
	}
	RegisterOAuthSession(session.State, gitlabduo.AgentProvider)
	bindPluginLoginOwner(ctx, session.State)
	go h.awaitGitLabSignIn(ctx, gitlabduo.Agent, session, "GitLab Duo Agent", func() {})
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": session.AuthURL(), "state": session.State})
}

// awaitGitLabSignIn waits for a GitLab sign-in's callback, from its listener
// or a pasted address, then exchanges the code and saves the account under
// the rule. done runs when the wait ends.
func (h *Handler) awaitGitLabSignIn(ctx context.Context, rule gitlabduo.Rule, session *gitlabduo.Session, label string, done func()) {
	defer done()
	path := filepath.Join(h.cfg.AuthDir, fmt.Sprintf(".oauth-%s-%s.oauth", rule.Provider, session.State))
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
			if !IsOAuthSessionPending(session.State, rule.Provider) {
				return
			}
			data, errRead := os.ReadFile(path)
			if os.IsNotExist(errRead) {
				continue
			}
			var result map[string]string
			if errRead != nil || json.Unmarshal(data, &result) != nil {
				SetOAuthSessionError(session.State, "Invalid OAuth callback")
				return
			}
			if result["state"] != session.State || result["error"] != "" {
				SetOAuthSessionError(session.State, "OAuth authorization was rejected")
				return
			}
			client := util.SetProxy(&h.cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second})
			credential, errExchange := session.Exchange(ctx, client, result["code"])
			if errExchange != nil {
				log.WithError(errExchange).Warnf("%s sign-in failed", label)
				SetOAuthSessionError(session.State, errExchange.Error())
				return
			}
			if rule.Provider == gitlabduo.Provider {
				// NeoPi's after-exchange hook (gitlab-duo-clear-cache) drops
				// every minted direct-access token.
				gitlabduo.ClearDirectAccessCache()
			}
			fileName := rule.FileName(credential)
			record := &coreauth.Auth{ID: fileName, FileName: fileName, Provider: rule.Provider, Label: label, Metadata: rule.Metadata(credential)}
			if guardOAuthSessionPendingForSave(session.State, rule.Provider) != nil {
				return
			}
			if _, errSave := h.saveTokenRecord(ctx, record); errSave != nil {
				SetOAuthSessionError(session.State, "Could not save "+label+" credential")
				return
			}
			CompleteOAuthSession(session.State)
			return
		}
	}
}
