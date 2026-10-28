package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
)

func (s *Server) initNeoRouting() {
	if s.neoStore == nil || s.neoPool == nil || s.neoErr != nil {
		return
	}
	route, err := neoroute.New(s.neoStore, s.neoPool)
	if err != nil {
		s.neoErr = fmt.Errorf("initialize virtual-model routing: %w", err)
		return
	}
	s.neoRoute = route
	s.handlers.SetNeoResolver(route)
	route.RegisterRoutes(s.engine.Group("/v8/neo", s.neoHTTP.SessionMiddleware()))
	s.neoPoolHTTP.SetAccountVisibility(route.VisibleAccountIDs)
	s.EnableNeoInference()
}

func (s *Server) neoModelsHandler(legacy gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := neo.PrincipalFromContext(c.Request.Context()); !ok {
			legacy(c)
			return
		}
		if s.neoRoute == nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "scoped inference is not attached", "code": "neoproxy_unavailable"}})
			return
		}
		s.neoRoute.ModelsHTTP(c)
	}
}
