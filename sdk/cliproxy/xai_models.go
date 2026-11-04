package cliproxy

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// xAI OAuth sign-ins publish the models their upstream lists at GET /v1/models:
// the official API for xai, the Grok CLI chat-proxy for grok-build. The two
// endpoints serve different model sets, so neither uses a hardcoded list once
// discovery has answered. Registration reads the cached listing and starts a
// background fetch when it is missing or stale; a successful fetch
// re-registers the credential.
const (
	xaiModelCatalogTTL        = 6 * time.Hour
	xaiModelCatalogRetry      = 5 * time.Minute
	xaiModelCatalogSweepEvery = 15 * time.Minute
	xaiModelCatalogTimeout    = 30 * time.Second
)

type xaiModelCatalogEntry struct {
	models    []executor.XAIListedModel
	fetchedAt time.Time
	failedAt  time.Time
	inFlight  bool
}

type xaiModelCatalog struct {
	mu      sync.Mutex
	entries map[string]*xaiModelCatalogEntry
}

var xaiCatalog = &xaiModelCatalog{entries: map[string]*xaiModelCatalogEntry{}}

// xaiFetchModels is replaced in tests.
var xaiFetchModels = executor.FetchXAIModels

func xaiCatalogKey(a *coreauth.Auth) string {
	return strings.ToLower(strings.TrimSpace(a.Provider)) + "|" + a.ID
}

// xaiUsesModelDiscovery reports whether a's models come from upstream listing:
// every grok-build credential and xai OAuth sign-ins. xai API keys keep their
// configured or curated models.
func xaiUsesModelDiscovery(a *coreauth.Auth) bool {
	if a == nil || a.ID == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(a.Provider)) {
	case executor.GrokBuildProvider:
		return true
	case "xai":
		return a.AuthKind() != coreauth.AuthKindAPIKey
	default:
		return false
	}
}

// xaiDiscoveredModels returns the models to register for an xAI OAuth
// credential and whether the cached listing needs a (re)fetch.
func xaiDiscoveredModels(a *coreauth.Auth, now time.Time) ([]*ModelInfo, bool) {
	xaiCatalog.mu.Lock()
	entry := xaiCatalog.entries[xaiCatalogKey(a)]
	var listed []executor.XAIListedModel
	stale := true
	if entry != nil {
		listed = entry.models
		fresh := !entry.fetchedAt.IsZero() && now.Sub(entry.fetchedAt) < xaiModelCatalogTTL
		backingOff := !entry.failedAt.IsZero() && now.Sub(entry.failedAt) < xaiModelCatalogRetry
		stale = !fresh && !backingOff && !entry.inFlight
	}
	xaiCatalog.mu.Unlock()
	grokBuild := strings.EqualFold(strings.TrimSpace(a.Provider), executor.GrokBuildProvider)
	if listed == nil {
		if grokBuild {
			// No curated fallback: the chat-proxy's set differs from the API's.
			return nil, stale
		}
		return registry.GetXAIModels(), stale
	}
	return xaiModelInfos(listed, !grokBuild), stale
}

// xaiModelInfos maps a listing onto model metadata. Known ids reuse the curated
// definition (thinking levels, modalities, limits); unknown ids still route.
// The official API also serves the image, video and speech built-ins.
func xaiModelInfos(listed []executor.XAIListedModel, withBuiltins bool) []*ModelInfo {
	known := map[string]*ModelInfo{}
	for _, m := range registry.GetXAIModels() {
		known[m.ID] = m
	}
	out := make([]*ModelInfo, 0, len(listed))
	for _, m := range listed {
		if info, ok := known[m.ID]; ok {
			out = append(out, info)
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, &ModelInfo{ID: m.ID, Object: "model", OwnedBy: "xai", Type: "xai", DisplayName: name, Name: m.ID, ContextLength: m.ContextWindow})
	}
	if withBuiltins {
		out = registry.WithXAIBuiltins(out)
	}
	return out
}

// queueXAIModelDiscovery fetches a's listing in the background unless a fetch
// is already running, then re-registers the credential with the result.
func (s *Service) queueXAIModelDiscovery(a *coreauth.Auth) {
	if s == nil || a == nil {
		return
	}
	cfg := s.currentConfig()
	if cfg != nil && cfg.Home.Enabled {
		return
	}
	key := xaiCatalogKey(a)
	xaiCatalog.mu.Lock()
	entry := xaiCatalog.entries[key]
	if entry == nil {
		entry = &xaiModelCatalogEntry{}
		xaiCatalog.entries[key] = entry
	}
	if entry.inFlight {
		xaiCatalog.mu.Unlock()
		return
	}
	entry.inFlight = true
	xaiCatalog.mu.Unlock()

	snapshot := a.Clone()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), xaiModelCatalogTimeout)
		defer cancel()
		listed, err := xaiFetchModels(ctx, cfg, snapshot)
		xaiCatalog.mu.Lock()
		entry.inFlight = false
		if err != nil {
			entry.failedAt = time.Now()
		} else {
			entry.models, entry.fetchedAt, entry.failedAt = listed, time.Now(), time.Time{}
		}
		xaiCatalog.mu.Unlock()
		if err != nil {
			log.WithError(err).Warnf("%s: model discovery failed for %s", snapshot.Provider, snapshot.ID)
			return
		}
		log.Infof("%s: discovered %d models for %s", snapshot.Provider, len(listed), snapshot.ID)
		if s.coreManager == nil {
			return
		}
		if latest, ok := s.coreManager.GetByID(snapshot.ID); ok && latest != nil && !latest.Disabled && xaiUsesModelDiscovery(latest) {
			s.refreshModelRegistrationForAuth(latest)
		}
	}()
}

// forgetXAIModelDiscovery drops the cached listing of a removed credential.
func forgetXAIModelDiscovery(provider, id string) {
	xaiCatalog.mu.Lock()
	delete(xaiCatalog.entries, strings.ToLower(strings.TrimSpace(provider))+"|"+id)
	xaiCatalog.mu.Unlock()
}

// runXAIModelDiscovery re-registers xAI OAuth credentials whose listing went
// stale, which refetches it; credentials that never re-register (imported,
// refresh-locked ones) still follow upstream model changes.
func (s *Service) runXAIModelDiscovery(ctx context.Context) {
	ticker := time.NewTicker(xaiModelCatalogSweepEvery)
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
			if a == nil || a.Disabled || !xaiUsesModelDiscovery(a) {
				continue
			}
			if _, stale := xaiDiscoveredModels(a, now); stale {
				s.queueXAIModelDiscovery(a)
			}
		}
	}
}
