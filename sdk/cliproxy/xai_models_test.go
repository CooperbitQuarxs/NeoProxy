package cliproxy

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func setXAICatalog(t *testing.T, a *coreauth.Auth, entry *xaiModelCatalogEntry) {
	t.Helper()
	xaiCatalog.mu.Lock()
	xaiCatalog.entries[xaiCatalogKey(a)] = entry
	xaiCatalog.mu.Unlock()
	t.Cleanup(func() { forgetXAIModelDiscovery(a.Provider, a.ID) })
}

func modelIDs(models []*ModelInfo) map[string]*ModelInfo {
	out := map[string]*ModelInfo{}
	for _, m := range models {
		out[m.ID] = m
	}
	return out
}

func TestXAIModelDiscoveryScope(t *testing.T) {
	for _, tc := range []struct {
		auth *coreauth.Auth
		want bool
	}{
		{&coreauth.Auth{ID: "gb", Provider: "grok-build", Attributes: map[string]string{"auth_kind": "oauth"}}, true},
		{&coreauth.Auth{ID: "x-oauth", Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}}, true},
		{&coreauth.Auth{ID: "x-key", Provider: "xai", Attributes: map[string]string{"auth_kind": "apikey", "api_key": "k"}}, false},
		{&coreauth.Auth{ID: "c", Provider: "codex"}, false},
	} {
		if got := xaiUsesModelDiscovery(tc.auth); got != tc.want {
			t.Errorf("%s/%s discovery = %v, want %v", tc.auth.Provider, tc.auth.ID, got, tc.want)
		}
	}
}

func TestXAIDiscoveredModelsBeforeAndAfterListing(t *testing.T) {
	now := time.Now()
	grokBuild := &coreauth.Auth{ID: "gb-test", Provider: executor.GrokBuildProvider}
	xai := &coreauth.Auth{ID: "xai-test", Provider: "xai", Attributes: map[string]string{"auth_kind": "oauth"}}

	if models, stale := xaiDiscoveredModels(grokBuild, now); models != nil || !stale {
		t.Fatalf("unlisted grok-build = %d models, stale %v; want none and a fetch", len(models), stale)
	}
	if models, stale := xaiDiscoveredModels(xai, now); len(models) == 0 || !stale {
		t.Fatalf("unlisted xai = %d models, stale %v; want curated fallback and a fetch", len(models), stale)
	}

	setXAICatalog(t, grokBuild, &xaiModelCatalogEntry{fetchedAt: now, models: []executor.XAIListedModel{
		{ID: "grok-4.7-build-fast", Name: "Grok 4.7 Fast", ContextWindow: 256000},
		{ID: "grok-9-preview", Name: "Grok 9 Preview", ContextWindow: 1000000},
	}})
	models, stale := xaiDiscoveredModels(grokBuild, now)
	byID := modelIDs(models)
	if stale || len(models) != 2 {
		t.Fatalf("listed grok-build = %v, stale %v", byID, stale)
	}
	if known := byID["grok-4.7-build-fast"]; known == nil || known.Thinking == nil {
		t.Fatalf("known model lost curated metadata: %+v", known)
	}
	if unknown := byID["grok-9-preview"]; unknown == nil || unknown.DisplayName != "Grok 9 Preview" || unknown.ContextLength != 1000000 || unknown.Type != "xai" {
		t.Fatalf("new upstream model not published: %+v", unknown)
	}

	setXAICatalog(t, xai, &xaiModelCatalogEntry{fetchedAt: now, models: []executor.XAIListedModel{{ID: "grok-4.3"}}})
	byID = modelIDs(mustModels(xaiDiscoveredModels(xai, now)))
	if byID["grok-4.3"] == nil || byID["grok-4.7-build-fast"] != nil || byID["grok-tts"] == nil {
		t.Fatalf("listed xai models = %v; want the listing plus API built-ins only", byID)
	}

	if _, stale = xaiDiscoveredModels(xai, now.Add(xaiModelCatalogTTL+time.Minute)); !stale {
		t.Fatal("expired listing not refetched")
	}
	setXAICatalog(t, grokBuild, &xaiModelCatalogEntry{failedAt: now})
	if _, stale = xaiDiscoveredModels(grokBuild, now.Add(time.Minute)); stale {
		t.Fatal("failed listing retried before its backoff")
	}
}

func mustModels(models []*ModelInfo, _ bool) []*ModelInfo { return models }
