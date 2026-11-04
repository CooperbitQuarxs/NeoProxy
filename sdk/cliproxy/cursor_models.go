package cliproxy

import (
	"context"
	"strings"
	"sync"
	"time"

	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	cursorModelTTL          = 6 * time.Hour
	cursorModelFetchTimeout = 20 * time.Second
)

// cursorModelRefreshing holds the auth IDs whose model discovery is in flight.
var cursorModelRefreshing sync.Map

// cursorFallbackSlugs are Cursor's own models, available on every plan; they
// stand in until the account's GetUsableModels answer arrives.
var cursorFallbackSlugs = []cursorauth.UsableModel{
	{ID: "default", DisplayName: "Auto"},
	{ID: "composer-2.5", DisplayName: "Composer 2.5"},
	{ID: "composer-2.5-fast", DisplayName: "Composer 2.5 Fast"},
}

// cursorModelsForAuth returns the account's discovered models (or Cursor's own
// models until discovery answers) and refreshes discovery in the background:
// registration must not wait for network I/O.
func (s *Service) cursorModelsForAuth(a *coreauth.Auth) []*ModelInfo {
	cached, fetched, ok := cursorauth.CachedModels(a.ID)
	if !ok || time.Since(fetched) > cursorModelTTL {
		s.refreshCursorModels(a)
	}
	if !ok || len(cached) == 0 {
		cached = cursorFallbackSlugs
	}
	return cursorModelInfos(cached)
}

func cursorModelInfos(models []cursorauth.UsableModel) []*ModelInfo {
	out := make([]*ModelInfo, 0, len(models))
	for _, m := range models {
		name := strings.TrimSpace(m.DisplayName)
		if name == "" {
			name = m.ID
		}
		out = append(out, &ModelInfo{
			ID:          executor.CursorModelPrefix + m.ID,
			Object:      "model",
			OwnedBy:     "cursor",
			Type:        "cursor",
			DisplayName: "Cursor " + name,
		})
	}
	return out
}

func (s *Service) refreshCursorModels(a *coreauth.Auth) {
	token, _ := a.Metadata["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return
	}
	if _, running := cursorModelRefreshing.LoadOrStore(a.ID, true); running {
		return
	}
	authID, proxyURL, cfg := a.ID, a.ProxyURL, s.currentConfig()
	go func() {
		defer cursorModelRefreshing.Delete(authID)
		ctx, cancel := context.WithTimeout(context.Background(), cursorModelFetchTimeout)
		defer cancel()
		models, err := cursorauth.NewAuthWithProxyURL(cfg, proxyURL).FetchUsableModels(ctx, strings.TrimSpace(token))
		if err != nil || len(models) == 0 {
			log.Debugf("cursor: model discovery for %s failed: %v", authID, err)
			return
		}
		cursorauth.RememberModels(authID, models)
		if s.coreManager == nil {
			return
		}
		if current, exists := s.coreManager.GetByID(authID); exists && current != nil && !current.Disabled {
			s.registerModelsForAuth(context.Background(), current)
		}
	}()
}
