package cliproxy

import (
	"context"
	"crypto/sha256"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const codexModelTTL = 2 * time.Hour
const codexModelRetry = 5 * time.Minute

type codexModelCatalogEntry struct {
	key       [32]byte
	scope     [32]byte
	models    []*ModelInfo // nil means no successful discovery; empty is authoritative for this cycle
	etag      string
	nextFetch time.Time
	cancel    context.CancelFunc
}

func codexDiscoveryCredential(cfg *config.Config, a *coreauth.Auth) (token, account, base string, key, scope [32]byte) {
	if a == nil {
		return
	}
	token, _ = a.Metadata["access_token"].(string)
	account, _ = a.Metadata["account_id"].(string)
	if storage, ok := a.Storage.(*codexauth.CodexTokenStorage); ok && storage != nil {
		if token == "" {
			token = storage.AccessToken
		}
		if account == "" {
			account = storage.AccountID
		}
	}
	base = strings.TrimSpace(a.Attributes["base_url"])
	if base == "" {
		base, _ = a.Metadata["base_url"].(string)
	}
	proxy := a.ProxyURL
	if proxy == "" && cfg != nil {
		proxy = cfg.ProxyURL
	}
	scope = sha256.Sum256([]byte(account + "\x00" + base + "\x00" + proxy))
	key = sha256.Sum256([]byte(token + "\x00" + account + "\x00" + base + "\x00" + proxy))
	return
}

// Each credential publishes only its own live roster. A transient failure keeps
// the last successful roster; the NeoPi bundle is only the cold-start fallback.
// Background work uses the same generation and auth/config publication fences
// as the other source-owned provider catalogs.
func (s *Service) codexModelsForAuth(cfg *config.Config, a *coreauth.Auth, now time.Time) []*ModelInfo {
	token, account, base, key, scope := codexDiscoveryCredential(cfg, a)
	if s == nil || token == "" {
		return registry.GetCodexModels()
	}
	s.cfgMu.RLock()
	lifetime := s.codexDiscoveryContext
	s.cfgMu.RUnlock()
	s.codexDiscoveryMu.Lock()
	if s.codexCatalog == nil {
		s.codexCatalog = make(map[string]*codexModelCatalogEntry)
	}
	entry := s.codexCatalog[a.ID]
	if entry == nil || entry.key != key {
		previous := entry
		if previous != nil && previous.cancel != nil {
			previous.cancel()
		}
		entry = &codexModelCatalogEntry{key: key, scope: scope}
		// A rotated token for the same account must not erase its live roster
		// while the new generation is being fetched.
		if previous != nil && account != "" && previous.scope == scope {
			entry.models, entry.etag = previous.models, previous.etag
		}
		s.codexCatalog[a.ID] = entry
	}
	models := entry.models
	if lifetime != nil && lifetime.Err() == nil && !(cfg != nil && cfg.Home.Enabled) && entry.cancel == nil && !now.Before(entry.nextFetch) {
		ctx, cancel := context.WithCancel(lifetime)
		entry.cancel = cancel
		go s.fetchCodexModels(ctx, cfg, a.Clone(), token, account, base, entry)
	}
	s.codexDiscoveryMu.Unlock()
	if models == nil {
		return registry.GetCodexModels()
	}
	// Registration applies user aliases and exclusions to its own snapshot.
	result := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		result = append(result, model.Codex.ModelInfo())
	}
	return result
}

func (s *Service) fetchCodexModels(ctx context.Context, cfg *config.Config, a *coreauth.Auth, token, account, base string, entry *codexModelCatalogEntry) {
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, a, 0)
	catalog, err := codexauth.FetchModels(ctx, client, codexauth.ModelDiscoveryOptions{AccessToken: token, AccountID: account, BaseURL: base})
	s.codexDiscoveryMu.Lock()
	if s.codexCatalog[a.ID] != entry {
		s.codexDiscoveryMu.Unlock()
		return
	}
	entry.cancel()
	entry.cancel = nil
	entry.nextFetch = time.Now().Add(codexModelRetry)
	if err == nil {
		entry.models = make([]*ModelInfo, 0, len(catalog.Models))
		entry.etag = catalog.ETag
		for _, model := range catalog.Models {
			model.ModelsETag = catalog.ETag
			entry.models = append(entry.models, model.ModelInfo())
		}
		// NeoPi retries successful empty catalogs after five minutes rather
		// than freezing an empty discovery cache for the full two-hour TTL.
		if len(catalog.Models) > 0 {
			entry.nextFetch = time.Now().Add(codexModelTTL)
		}
	}
	s.codexDiscoveryMu.Unlock()
	if err != nil || s.coreManager == nil {
		return
	}
	if latest, ok := s.coreManager.GetByID(a.ID); ok && latest != nil && !latest.Disabled && latest.Provider == "codex" {
		s.refreshModelRegistrationForAuth(latest)
	}
}

func (s *Service) forgetCodexModelDiscovery(id string) {
	s.codexDiscoveryMu.Lock()
	if entry := s.codexCatalog[id]; entry != nil && entry.cancel != nil {
		entry.cancel()
	}
	delete(s.codexCatalog, id)
	s.codexDiscoveryMu.Unlock()
}

func (s *Service) runCodexModelDiscovery(ctx context.Context) {
	ticker := time.NewTicker(codexModelRetry)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if s.coreManager == nil {
			continue
		}
		cfg := s.currentConfig()
		for _, a := range s.coreManager.List() {
			if a == nil || a.Disabled || a.Provider != "codex" || a.AuthKind() == "apikey" {
				continue
			}
			s.codexModelsForAuth(cfg, a, time.Now())
		}
	}
}
