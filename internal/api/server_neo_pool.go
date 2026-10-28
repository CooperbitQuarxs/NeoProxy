package api

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func (s *Server) initNeoPool(manager *coreauth.Manager) {
	if s.neoStore == nil || s.neoErr != nil {
		return
	}
	pool, err := neopool.New(s.neoStore.DB(), manager)
	if err != nil {
		s.neoErr = fmt.Errorf("initialize account pools: %w", err)
		return
	}
	s.neoPool = pool
	s.neoStore.SetAccountScopeValidator(pool.ScopeOwned)
	s.neoPoolHTTP = neopool.NewHTTP(pool, s.mgmt)
	s.neoPoolHTTP.Register(s.engine.Group("/v8/neo", s.neoHTTP.SessionMiddleware()))
}
