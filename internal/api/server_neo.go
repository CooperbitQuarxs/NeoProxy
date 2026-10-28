package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// WithNeoStore injects an explicitly isolated identity database. Its lifecycle
// remains the caller's responsibility. Empty-config SDK callers otherwise do not
// create a database, unless NEOPROXY_DATA_DIR explicitly selects one.
func WithNeoStore(store *neo.Store) ServerOption {
	return func(options *serverOptionConfig) { options.neoStore = store }
}
func (s *Server) NeoStore() *neo.Store { return s.neoStore }

// EnableNeoInference is called only after the scoped catalogue and resolver have
// both been attached. Legacy inference remains available before that cutover.
func (s *Server) EnableNeoInference() { s.neoInferenceReady.Store(true) }
func (s *Server) initNeo(injected *neo.Store) {
	s.neoStore = injected
	if injected == nil {
		dir := strings.TrimSpace(os.Getenv("NEOPROXY_DATA_DIR"))
		if dir == "" && strings.TrimSpace(s.configFilePath) != "" {
			path, err := filepath.Abs(s.configFilePath)
			if err != nil {
				s.neoErr = err
			} else {
				dir = filepath.Join(filepath.Dir(path), "data")
			}
		}
		if dir != "" {
			s.neoStore, s.neoErr = neo.Open(filepath.Join(dir, "neoproxy.sqlite"))
			s.neoOwnStore = s.neoStore != nil
		}
	}
	s.neoHTTP = neo.NewHTTP(s.neoStore)
}
func (s *Server) neoManagementMiddleware() gin.HandlerFunc {
	if s.neoHTTP == nil {
		return s.mgmt.Middleware()
	}
	return s.neoHTTP.ManagementMiddleware(s.mgmt.Middleware())
}

func neoCredential(c *gin.Context) string {
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	parts := strings.SplitN(authorization, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		authorization = strings.TrimSpace(parts[1])
	}
	for _, candidate := range []string{authorization, c.GetHeader("X-Api-Key"), c.GetHeader("X-Goog-Api-Key"), c.GetHeader("X-Management-Key"), c.Query("key"), c.Query("auth_token")} {
		candidate = strings.TrimSpace(candidate)
		if strings.HasPrefix(candidate, "nk_") {
			return candidate
		}
	}
	return ""
}

// neoInferenceMiddleware runs before any dynamic route, including worker routes
// that deliberately disable legacy websocket authentication. New user keys are
// consumer-only: only day-one inference surfaces may receive these principals.
func (s *Server) neoInferenceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		secret := neoCredential(c)
		if secret == "" {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		allowed := c.Request.Method == http.MethodGet && path == "/v1/models"
		if c.Request.Method == http.MethodPost {
			switch path {
			case "/v1/chat/completions", "/v1/responses", "/v1/messages":
				allowed = true
			}
		}
		if !allowed {
			neo.Error(c, neo.ErrForbidden)
			return
		}
		if s.neoHTTP == nil || !s.neoHTTP.Available(c) {
			return
		}
		p, err := s.neoStore.AuthenticateKey(c.Request.Context(), secret)
		if err != nil {
			neo.Error(c, err)
			return
		}
		if !s.neoInferenceReady.Load() {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"message": "scoped inference is not attached", "code": "neoproxy_unavailable"}})
			return
		}
		ctx := neo.WithPrincipal(c.Request.Context(), p)
		// Neither raw key nor cookie is propagated to usage/executor identity.
		ctx = coreusage.WithAPIKey(ctx, p.KeyID)
		ctx = coreusage.WithAccessProvider(ctx, "neoproxy-user")
		ctx = coreusage.WithIsNativeKey(ctx, false)
		c.Request = c.Request.WithContext(ctx)
		c.Set("userApiKey", p.KeyID)
		c.Set("accessProvider", "neoproxy-user")
		c.Next()
	}
}
func (s *Server) neoStartupError() error {
	if s.neoErr != nil {
		return fmt.Errorf("initialize NeoProxy identity database: %w", s.neoErr)
	}
	return nil
}
