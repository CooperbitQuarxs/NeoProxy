package cliproxy

import (
	"context"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Kimi Code accounts publish the roster their plan lists at GET
// /coding/v1/models; NeoPi treats it as authoritative and serves its bundled
// kimi-code catalog until it answers. Registration reads the cached listing
// and starts a background fetch when it is missing or stale; a successful
// fetch re-registers the account.
const (
	kimiModelCatalogTTL        = 6 * time.Hour
	kimiModelCatalogRetry      = 5 * time.Minute
	kimiModelCatalogSweepEvery = 15 * time.Minute
	kimiModelCatalogTimeout    = 30 * time.Second
)

type kimiModelCatalogEntry struct {
	models    []*ModelInfo
	fetchedAt time.Time
	failedAt  time.Time
	inFlight  bool
}

var kimiCatalog = struct {
	mu      sync.Mutex
	entries map[string]*kimiModelCatalogEntry
}{entries: map[string]*kimiModelCatalogEntry{}}

// kimiFetchModels is replaced in tests.
var kimiFetchModels = executor.FetchKimiModels

// kimiDiscoveredModels returns the models to register for a Kimi account and
// whether its cached listing needs a (re)fetch.
func kimiDiscoveredModels(a *coreauth.Auth, now time.Time) ([]*ModelInfo, bool) {
	kimiCatalog.mu.Lock()
	defer kimiCatalog.mu.Unlock()
	entry := kimiCatalog.entries[a.ID]
	if entry == nil {
		return registry.KimiBundledModels(), true
	}
	fresh := !entry.fetchedAt.IsZero() && now.Sub(entry.fetchedAt) < kimiModelCatalogTTL
	backingOff := !entry.failedAt.IsZero() && now.Sub(entry.failedAt) < kimiModelCatalogRetry
	stale := !fresh && !backingOff && !entry.inFlight
	if entry.models == nil {
		return registry.KimiBundledModels(), stale
	}
	out := make([]*ModelInfo, 0, len(entry.models))
	for _, model := range entry.models {
		clone := *model
		clone.NeoPi = model.NeoPi.Clone()
		out = append(out, &clone)
	}
	return out, stale
}

// queueKimiModelDiscovery fetches a's listing in the background unless a
// fetch is already running, then re-registers the account with the result.
func (s *Service) queueKimiModelDiscovery(a *coreauth.Auth) {
	if s == nil || a == nil || a.ID == "" {
		return
	}
	cfg := s.currentConfig()
	if cfg != nil && cfg.Home.Enabled {
		return
	}
	kimiCatalog.mu.Lock()
	entry := kimiCatalog.entries[a.ID]
	if entry == nil {
		entry = &kimiModelCatalogEntry{}
		kimiCatalog.entries[a.ID] = entry
	}
	if entry.inFlight {
		kimiCatalog.mu.Unlock()
		return
	}
	entry.inFlight = true
	kimiCatalog.mu.Unlock()

	snapshot := a.Clone()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), kimiModelCatalogTimeout)
		defer cancel()
		models, err := kimiFetchModels(ctx, cfg, snapshot)
		kimiCatalog.mu.Lock()
		entry.inFlight = false
		if err != nil {
			entry.failedAt = time.Now()
		} else {
			entry.models, entry.fetchedAt, entry.failedAt = models, time.Now(), time.Time{}
		}
		kimiCatalog.mu.Unlock()
		if err != nil {
			log.WithError(err).Warnf("kimi: model discovery failed for %s", snapshot.ID)
			return
		}
		log.Infof("kimi: discovered %d models for %s", len(models), snapshot.ID)
		if s.coreManager == nil {
			return
		}
		if latest, ok := s.coreManager.GetByID(snapshot.ID); ok && latest != nil && !latest.Disabled && latest.Provider == "kimi" {
			s.refreshModelRegistrationForAuth(latest)
		}
	}()
}

// forgetKimiModelDiscovery drops the cached listing of a removed account.
func forgetKimiModelDiscovery(id string) {
	kimiCatalog.mu.Lock()
	delete(kimiCatalog.entries, id)
	kimiCatalog.mu.Unlock()
}

// runKimiModelDiscovery re-registers Kimi accounts whose listing went stale,
// which refetches it; accounts that never re-register (imported,
// refresh-locked ones) still follow plan changes.
func (s *Service) runKimiModelDiscovery(ctx context.Context) {
	ticker := time.NewTicker(kimiModelCatalogSweepEvery)
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
		now := time.Now()
		for _, a := range s.coreManager.List() {
			if a == nil || a.Disabled || a.Provider != "kimi" {
				continue
			}
			if _, stale := kimiDiscoveredModels(a, now); stale {
				s.queueKimiModelDiscovery(a)
			}
		}
	}
}
