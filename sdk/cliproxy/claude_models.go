package cliproxy

import (
	"context"
	"crypto/sha256"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const claudeModelTTL = 2 * time.Hour
const claudeModelRetry = 5 * time.Minute

type claudeModelCatalogEntry struct {
	key       [32]byte
	models    []anthropicnpi.DiscoveredModel
	nextFetch time.Time
	cancel    context.CancelFunc
}

func claudeDiscoveryCredential(cfg *config.Config, a *coreauth.Auth) (token, base string, key [32]byte) {
	credential, err := anthropicnpi.CredentialFromAuth(a)
	if err != nil {
		return
	}
	token = credential.APIKey
	if token == "" {
		token = credential.AccessToken
	}
	base = strings.TrimSpace(a.Attributes["base_url"])
	if base == "" {
		base, _ = a.Metadata["base_url"].(string)
	}
	proxy := a.ProxyURL
	if proxy == "" && cfg != nil {
		proxy = cfg.ProxyURL
	}
	key = sha256.Sum256([]byte(token + "\x00" + base + "\x00" + proxy))
	return
}

// NeoPi merges Anthropic's live roster with its bundled models; a successful
// empty response is not an authoritative removal of the bundled catalog.
func claudeModelInfos(listed []anthropicnpi.DiscoveredModel) []*ModelInfo {
	models := registry.GetClaudeModels()
	known := make(map[string]*ModelInfo, len(models)+len(listed))
	for _, model := range models {
		known[model.ID] = model
	}
	for _, model := range listed {
		if info := known[model.ID]; info != nil {
			info.DisplayName = model.Name
			continue
		}
		info := &ModelInfo{ID: model.ID, Object: "model", OwnedBy: "anthropic", Type: "claude", Name: model.ID, DisplayName: model.Name, SupportedInputModalities: []string{"text"}}
		known[model.ID] = info
		models = append(models, info)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}

// Registration never blocks on an upstream socket. Each credential generation
// has one cancellable probe; only that generation can update its cached roster.
// Publication itself still passes through the service's auth/config fence.
func (s *Service) claudeModelsForAuth(cfg *config.Config, a *coreauth.Auth, now time.Time) []anthropicnpi.DiscoveredModel {
	token, base, key := claudeDiscoveryCredential(cfg, a)
	if s == nil || token == "" {
		return nil
	}
	s.cfgMu.RLock()
	lifetime := s.claudeDiscoveryContext
	s.cfgMu.RUnlock()
	s.claudeDiscoveryMu.Lock()
	if s.claudeCatalog == nil {
		s.claudeCatalog = make(map[string]*claudeModelCatalogEntry)
	}
	entry := s.claudeCatalog[a.ID]
	if entry == nil || entry.key != key {
		if entry != nil && entry.cancel != nil {
			entry.cancel()
		}
		entry = &claudeModelCatalogEntry{key: key}
		s.claudeCatalog[a.ID] = entry
	}
	listed := entry.models
	if lifetime != nil && lifetime.Err() == nil && !(cfg != nil && cfg.Home.Enabled) && entry.cancel == nil && !now.Before(entry.nextFetch) {
		ctx, cancel := context.WithCancel(lifetime)
		entry.cancel = cancel
		go s.fetchClaudeModels(ctx, cfg, a.Clone(), token, base, entry)
	}
	s.claudeDiscoveryMu.Unlock()
	return listed
}

func (s *Service) fetchClaudeModels(ctx context.Context, cfg *config.Config, a *coreauth.Auth, token, base string, entry *claudeModelCatalogEntry) {
	client := &anthropicnpi.Client{HTTP: helps.NewProxyAwareHTTPClient(ctx, cfg, a, 0)}
	listed, err := client.Models(ctx, token, base)
	s.claudeDiscoveryMu.Lock()
	if s.claudeCatalog[a.ID] != entry {
		s.claudeDiscoveryMu.Unlock()
		return
	}
	entry.cancel()
	entry.cancel = nil
	entry.nextFetch = time.Now().Add(claudeModelRetry)
	if err == nil {
		entry.models = listed
		entry.nextFetch = time.Now().Add(claudeModelTTL)
	}
	s.claudeDiscoveryMu.Unlock()
	if err != nil || s.coreManager == nil {
		return
	}
	if latest, ok := s.coreManager.GetByID(a.ID); ok && latest != nil && !latest.Disabled && latest.Provider == "claude" {
		s.refreshModelRegistrationForAuth(latest)
	}
}

func (s *Service) forgetClaudeModelDiscovery(id string) {
	s.claudeDiscoveryMu.Lock()
	if entry := s.claudeCatalog[id]; entry != nil && entry.cancel != nil {
		entry.cancel()
	}
	delete(s.claudeCatalog, id)
	s.claudeDiscoveryMu.Unlock()
}

func (s *Service) runClaudeModelDiscovery(ctx context.Context) {
	ticker := time.NewTicker(claudeModelRetry)
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
			if a == nil || a.Disabled || a.Provider != "claude" {
				continue
			}
			if entry := resolveConfigClaudeKeyForConfig(cfg, a); entry != nil && len(entry.Models) > 0 {
				continue
			}
			s.claudeModelsForAuth(cfg, a, time.Now())
		}
	}
}
