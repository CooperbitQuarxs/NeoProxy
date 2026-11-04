package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	zaiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/zai"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// zaiCompatName is the openai-compatibility entry Z.ai credentials live in;
// the dashboard catalogue maps it (and its -N variants) to "zai".
const zaiCompatName = "zai"

type zaiOAuthService interface {
	AuthorizationURL(state string) string
	ExchangeCode(ctx context.Context, code, state string) (string, zaiauth.Account, error)
	MintAPIKey(ctx context.Context, oauthToken string) (string, error)
	ListModels(ctx context.Context, apiKey string) ([]string, error)
}

var newZaiOAuthService = func(cfg *config.Config) zaiOAuthService {
	return zaiauth.NewService(util.SetProxy(&cfg.SDKConfig, &http.Client{Timeout: 30 * time.Second}))
}

// RequestZaiToken starts Z.ai's GLM Coding Plan sign-in. Z.ai only redirects
// to ZCode's zcode:// scheme, so the code always arrives through the pasted
// callback (POST oauth callback), never a loopback listener.
func (h *Handler) RequestZaiToken(c *gin.Context) {
	state, errState := misc.GenerateRandomState()
	if errState != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate state parameter"})
		return
	}
	authSvc := newZaiOAuthService(h.cfg)
	RegisterOAuthSession(state, "zai")
	// Do not inherit the HTTP request cancellation: login continues after returning the URL.
	ctx := PopulateAuthContext(context.Background(), c)
	go h.completeZaiOAuth(ctx, h.cfg.AuthDir, state, authSvc)
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": authSvc.AuthorizationURL(state), "state": state})
}

// completeZaiOAuth waits for the pasted code, provisions the durable API key
// and adds it to the Coding Plan openai-compatibility entry like a pasted key.
func (h *Handler) completeZaiOAuth(ctx context.Context, authDir, state string, authSvc zaiOAuthService) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	go watchOAuthSessionCancel(ctx, cancel, state, "zai")

	waitFile := filepath.Join(authDir, fmt.Sprintf(".oauth-zai-%s.oauth", state))
	defer func() {
		if errRemove := os.Remove(waitFile); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warn("failed to remove Z.ai OAuth callback file")
		}
	}()
	result, errWait := waitOAuthCallbackFile(ctx, waitFile, state, "zai")
	if errWait != nil {
		if IsOAuthSessionPending(state, "zai") {
			SetOAuthSessionError(state, errWait.Error())
		}
		return
	}
	fail := func(message string) {
		if IsOAuthSessionPending(state, "zai") {
			SetOAuthSessionError(state, message)
		}
	}
	switch {
	case !IsOAuthSessionPending(state, "zai"):
		return
	case result.State != state:
		fail("State code error")
		return
	case result.Error != "":
		fail("Z.ai authorization denied")
		return
	case strings.TrimSpace(result.Code) == "":
		fail("Missing authorization code")
		return
	}
	// Upstream errors can carry tokens or keys; never expose their text.
	token, _, errExchange := authSvc.ExchangeCode(ctx, strings.TrimSpace(result.Code), state)
	if errExchange != nil {
		log.WithError(errExchange).Warn("Z.ai sign-in: code exchange failed")
		fail("Failed to exchange authorization code for tokens")
		return
	}
	key, errMint := authSvc.MintAPIKey(ctx, token)
	if errMint != nil {
		log.WithError(errMint).Warn("Z.ai sign-in: API key provisioning failed")
		fail("Z.ai signed you in but did not provide an API key. Check that the account has a GLM Coding Plan.")
		return
	}
	models, errModels := authSvc.ListModels(ctx, key)
	if errModels != nil {
		log.WithError(errModels).Warn("Z.ai sign-in: model listing failed")
		fail("Z.ai did not list models for the new key")
		return
	}
	if errGuard := guardOAuthSessionPendingForSave(state, "zai"); errGuard != nil {
		return
	}
	h.mu.Lock()
	name := compatNameForBase(h.cfg, zaiCompatName, zaiauth.CodingPlanBaseURL)
	h.mu.Unlock()
	_, errCreate := h.CreateAccountAPIKey(ctx, coreauth.AccountKey{Provider: "openai-compatibility", APIKey: key, BaseURL: zaiauth.CodingPlanBaseURL, CompatName: name, Models: models})
	switch {
	case errors.Is(errCreate, coreauth.ErrAccountKeyConflict):
		fail("This Z.ai account is already in the pool.")
		return
	case errCreate != nil:
		log.WithError(errCreate).Error("Z.ai sign-in: saving the key failed")
		fail("Failed to save the Z.ai key")
		return
	}
	CompleteOAuthSession(state)
	log.Info("Z.ai authentication successful")
}

// compatNameForBase returns name, or name-N, so an openai-compatibility entry
// with that name either does not exist yet or already points at base. One name
// maps to exactly one upstream.
func compatNameForBase(cfg *config.Config, name, base string) string {
	bases := map[string]string{}
	if cfg != nil {
		for _, entry := range cfg.OpenAICompatibility {
			bases[strings.ToLower(strings.TrimSpace(entry.Name))] = strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/")
		}
	}
	candidate := name
	for n := 2; ; n++ {
		existing, taken := bases[candidate]
		if !taken || existing == base {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", name, n)
	}
}
