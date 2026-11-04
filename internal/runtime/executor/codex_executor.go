package executor

import (
	"context"
	"sync"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/sync/singleflight"
)

// CodexExecutor serves Codex Responses and owns per-credential refresh flights.
type CodexExecutor struct {
	cfg *config.Config
	// refresh is shared by the per-scope copies ForAPIKey makes, so every
	// copy joins the same per-credential flight.
	refresh      *codexRefreshState
	refreshClock func() time.Time
	refreshToken func(context.Context, *cliproxyauth.Auth) (*codexauth.CodexTokenData, error)
}

type codexRefreshState struct {
	flight singleflight.Group
	mu     sync.Mutex
	mints  map[string]codexMint
}

type codexMint struct {
	access string
	at     time.Time
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor {
	return &CodexExecutor{cfg: cfg, refresh: &codexRefreshState{}}
}

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) modelLevelCooling() bool {
	return e != nil && e.cfg != nil && e.cfg.Codex.ModelLevelCooling
}

// SupportsApplyPatch reports the actual executor contract, independent of its provider name.
func (e *CodexExecutor) SupportsApplyPatch() bool { return e != nil }
