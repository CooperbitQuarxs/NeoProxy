package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
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
	s.neoStore.SetAccountScopeValidator(func(ctx context.Context, userID string, ids []string) error {
		err := pool.ScopeOwned(ctx, userID, ids)
		if errors.Is(err, neopool.ErrForbidden) {
			return neo.ErrForbidden
		}
		return err
	})
	s.neoPoolHTTP = neopool.NewHTTP(pool, s.mgmt)
	s.neoPoolHTTP.SetAccessProbe(s.probeAccess)
	s.neoPoolHTTP.Register(s.engine.Group("/v8/neo", s.neoHTTP.SessionMiddleware()))
}

// probeAccess runs one Chat Completions request through the engine pinned to a
// single credential. The dashboard principal is dropped from the context so the
// request is not routed through virtual models and never touches the pool
// ledger: a test is budget-free, like a direct attempt.
func (s *Server) probeAccess(ctx context.Context, authID, provider string, body []byte) ([]byte, int, error) {
	model := gjson.GetBytes(body, "model").String()
	resp, errMsg := s.handlers.ExecuteModel(neo.WithPrincipal(ctx, neo.Principal{}), handlers.ModelExecutionRequest{
		EntryProtocol:  "openai",
		ExitProtocol:   "openai",
		Model:          model,
		Body:           body,
		ForcedProvider: provider,
		AuthID:         authID,
	})
	if errMsg != nil {
		err := errMsg.Error
		if err == nil {
			err = errors.New("upstream request failed")
		}
		return nil, errMsg.StatusCode, err
	}
	return resp.Body, resp.StatusCode, nil
}
