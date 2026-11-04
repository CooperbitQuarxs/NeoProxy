package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type claudeModelsTransport func(*http.Request) (*http.Response, error)

func (f claudeModelsTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClaudeDiscoveredRosterMergesWithoutInventingCapabilities(t *testing.T) {
	bundled := registry.GetClaudeModels()
	known := bundled[0]
	listed := []anthropicnpi.DiscoveredModel{{ID: known.ID, Name: "Live display"}, {ID: "claude-future-unknown", Name: "Future"}}
	models := claudeModelInfos(listed)
	byID := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	for _, model := range bundled {
		if byID[model.ID] == nil {
			t.Errorf("live listing removed bundled %s", model.ID)
		}
	}
	if got := byID[known.ID]; got.DisplayName != "Live display" || got.ContextLength != known.ContextLength || got.MaxCompletionTokens != known.MaxCompletionTokens {
		t.Fatalf("known model lost bundled metadata or live name: %#v", got)
	}
	if got := byID["claude-future-unknown"]; got == nil || got.Thinking != nil || got.ContextLength != 0 || len(got.SupportedInputModalities) != 1 || got.SupportedInputModalities[0] != "text" {
		t.Fatalf("unknown model invented capabilities: %#v", got)
	}
	if registry.GetClaudeModels()[0].DisplayName != known.DisplayName {
		t.Error("discovery mutated bundled catalog")
	}
}

func TestClaudeDiscoveryRetainsLastGoodButNotAnotherCredential(t *testing.T) {
	a := &coreauth.Auth{ID: "claude-cache-boundary", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat-cache-one"}}
	cfg := &config.Config{}
	token, base, key := claudeDiscoveryCredential(cfg, a)
	entry := &claudeModelCatalogEntry{key: key, cancel: func() {}}
	s := &Service{cfg: cfg, claudeCatalog: map[string]*claudeModelCatalogEntry{a.ID: entry}}
	status, response := http.StatusOK, `{"data":[{"id":"live-account-model"}]}`
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", claudeModelsTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("discovery used another credential")
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	}))
	before := time.Now()
	s.fetchClaudeModels(ctx, cfg, a, token, base, entry)
	if len(entry.models) != 1 || entry.models[0].ID != "live-account-model" || entry.nextFetch.Before(before.Add(claudeModelTTL)) {
		t.Fatalf("successful listing not cached: %#v", entry)
	}
	status = http.StatusServiceUnavailable
	entry.cancel = func() {}
	before = time.Now()
	s.fetchClaudeModels(ctx, cfg, a, token, base, entry)
	if len(entry.models) != 1 || entry.models[0].ID != "live-account-model" || entry.nextFetch.Before(before.Add(claudeModelRetry)) || entry.nextFetch.After(before.Add(claudeModelRetry+time.Minute)) {
		t.Fatalf("failed refresh lost fallback or retry policy: %#v", entry)
	}
	changed := a.Clone()
	changed.Metadata = map[string]any{"access_token": "sk-ant-oat-cache-two"}
	if listed := s.claudeModelsForAuth(cfg, changed, time.Now()); len(listed) != 0 {
		t.Fatalf("new token inherited another credential's roster: %#v", listed)
	}
	s.forgetClaudeModelDiscovery(a.ID)
	if len(s.claudeCatalog) != 0 {
		t.Error("removed credential retained its model cache")
	}
}

func TestClaudeDiscoveryDiscardsLateCredentialResult(t *testing.T) {
	a := &coreauth.Auth{ID: "claude-late-result", Provider: "claude", Metadata: map[string]any{"access_token": "sk-ant-oat-old"}}
	cfg := &config.Config{}
	token, base, key := claudeDiscoveryCredential(cfg, a)
	old := &claudeModelCatalogEntry{key: key, cancel: func() {}}
	s := &Service{cfg: cfg, claudeCatalog: map[string]*claudeModelCatalogEntry{a.ID: old}}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", claudeModelsTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"stale-private-model"}]}`)), Request: r}, nil
	}))
	go func() { defer close(finished); s.fetchClaudeModels(ctx, cfg, a, token, base, old) }()
	<-entered
	changed := a.Clone()
	changed.Metadata = map[string]any{"access_token": "sk-ant-oat-new"}
	s.claudeModelsForAuth(cfg, changed, time.Now())
	close(release)
	<-finished
	if listed := s.claudeModelsForAuth(cfg, changed, time.Now()); len(listed) != 0 {
		t.Fatalf("stale fetch crossed credential generation: %#v", listed)
	}
}
