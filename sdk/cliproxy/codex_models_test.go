package cliproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestCodexDiscoveryReplacesRosterAndPreservesItOnTransientFailure(t *testing.T) {
	a := &coreauth.Auth{ID: "codex-live-roster", Provider: "codex", Metadata: map[string]any{"access_token": "fake-token", "account_id": "account-a"}}
	cfg := &config.Config{}
	token, account, base, key, scope := codexDiscoveryCredential(cfg, a)
	entry := &codexModelCatalogEntry{key: key, scope: scope, cancel: func() {}}
	s := &Service{cfg: cfg, codexCatalog: map[string]*codexModelCatalogEntry{a.ID: entry}}
	status, response := http.StatusOK, `{"models":[{"slug":"gpt-6.1-terra","supported_reasoning_levels":[{"effort":"high"}],"context_window":500000}]}`
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", claudeModelsTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Chatgpt-Account-Id") != account {
			t.Error("discovery crossed the account credential scope")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Etag": []string{`"live-roster"`}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	}))
	before := time.Now()
	s.fetchCodexModels(ctx, cfg, a, token, account, base, entry)
	listed := s.codexModelsForAuth(cfg, a, before)
	if len(listed) != 1 || listed[0].ID != "gpt-6.1-terra" || listed[0].Codex.ModelsETag != `"live-roster"` || !slices.Equal(listed[0].Thinking.Levels, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("live roster was merged into stale static models or lost metadata: %#v", listed)
	}
	if entry.nextFetch.Before(before.Add(codexModelTTL)) || entry.nextFetch.After(time.Now().Add(codexModelTTL)) {
		t.Fatal("successful non-empty discovery did not use the source two-hour cache")
	}
	status = http.StatusServiceUnavailable
	entry.cancel = func() {}
	before = time.Now()
	s.fetchCodexModels(ctx, cfg, a, token, account, base, entry)
	listed = s.codexModelsForAuth(cfg, a, before)
	if len(listed) != 1 || listed[0].ID != "gpt-6.1-terra" || entry.nextFetch.Before(before.Add(codexModelRetry)) || entry.nextFetch.After(time.Now().Add(codexModelRetry)) {
		t.Fatal("transient failure lost the last good roster or failed to schedule a short retry")
	}
	status, response = http.StatusOK, `{"models":[]}`
	entry.cancel = func() {}
	before = time.Now()
	s.fetchCodexModels(ctx, cfg, a, token, account, base, entry)
	if listed = s.codexModelsForAuth(cfg, a, before); len(listed) != 0 || entry.nextFetch.After(time.Now().Add(codexModelRetry)) {
		t.Fatal("successful empty discovery resurrected bundled models or froze the empty catalog")
	}
}

func TestCodexDiscoveryIsolatesAccountChangesButKeepsSameAccountRotation(t *testing.T) {
	a := &coreauth.Auth{ID: "codex-account-scope", Provider: "codex", Metadata: map[string]any{"access_token": "fake-one", "account_id": "account-a"}}
	cfg := &config.Config{}
	token, account, base, key, scope := codexDiscoveryCredential(cfg, a)
	entry := &codexModelCatalogEntry{key: key, scope: scope, cancel: func() {}}
	s := &Service{cfg: cfg, codexCatalog: map[string]*codexModelCatalogEntry{a.ID: entry}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", claudeModelsTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"account-private-model","service_tiers":[{"id":"priority"}]}]}`)), Request: r}, nil
	}))
	s.fetchCodexModels(ctx, cfg, a, token, account, base, entry)
	rotated := a.Clone()
	rotated.Metadata["access_token"] = "fake-two"
	listed := s.codexModelsForAuth(cfg, rotated, time.Now())
	if len(listed) != 1 || listed[0].ID != "account-private-model" {
		t.Fatal("same-account token rotation erased its live model roster")
	}
	(*listed[0].Codex.ServiceTiers)[0] = "mutated-by-consumer"
	if tiers := s.codexModelsForAuth(cfg, rotated, time.Now())[0].Codex.ServiceTiers; !slices.Equal(*tiers, []string{"priority"}) {
		t.Fatal("consumer mutation leaked into the cached account model")
	}
	otherAccount := rotated.Clone()
	otherAccount.Metadata["account_id"] = "account-b"
	for _, model := range s.codexModelsForAuth(cfg, otherAccount, time.Now()) {
		if model.ID == "account-private-model" {
			t.Fatal("another account inherited a private model roster")
		}
	}
	s.forgetCodexModelDiscovery(a.ID)
	for _, model := range s.codexModelsForAuth(cfg, a, time.Now()) {
		if model.ID == "account-private-model" {
			t.Fatal("removed credential retained its private roster")
		}
	}
}

func TestCodexDiscoveryDiscardsLateCredentialResults(t *testing.T) {
	a := &coreauth.Auth{ID: "codex-late-models", Provider: "codex", Metadata: map[string]any{"access_token": "fake-old", "account_id": "account-a"}}
	cfg := &config.Config{}
	token, account, base, key, scope := codexDiscoveryCredential(cfg, a)
	old := &codexModelCatalogEntry{key: key, scope: scope, cancel: func() {}}
	s := &Service{cfg: cfg, codexCatalog: map[string]*codexModelCatalogEntry{a.ID: old}}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", claudeModelsTransport(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-release
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"stale-private-model"}]}`)), Request: r}, nil
	}))
	go func() { defer close(finished); s.fetchCodexModels(ctx, cfg, a, token, account, base, old) }()
	<-entered
	changed := a.Clone()
	changed.Metadata["access_token"] = "fake-new"
	changed.Metadata["account_id"] = "account-b"
	s.codexModelsForAuth(cfg, changed, time.Now())
	close(release)
	<-finished
	for _, model := range s.codexModelsForAuth(cfg, changed, time.Now()) {
		if model.ID == "stale-private-model" {
			t.Fatal("late discovery crossed the credential generation fence")
		}
	}
}

func TestCodexDiscoveryPublishesAccountRostersToBothModelAPIs(t *testing.T) {
	cfg := &config.Config{}
	cfg.ForceModelPrefix = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	service := &Service{cfg: cfg, coreManager: manager}
	modelRegistry := registry.GetGlobalRegistry()
	accounts := make([]*coreauth.Auth, 0, 2)
	for _, name := range []string{"a", "b"} {
		auth, err := manager.Register(context.Background(), &coreauth.Auth{
			ID: "codex-discovery-http-" + name, Provider: "codex",
			Prefix: "discovery-" + name, Status: coreauth.StatusActive,
			Metadata: map[string]any{"access_token": "fake-" + name, "account_id": name},
		})
		if err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, auth)
		t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
		service.registerModelsForAuth(context.Background(), auth)
	}
	responses := map[string]string{
		"a": `{"models":[{"slug":"gpt-6.1-terra","context_window":500000,"supported_reasoning_levels":[{"effort":"high"}]}]}`,
		"b": `{"models":[{"slug":"account-b-only","context_window":128000}]}`,
	}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", claudeModelsTransport(func(r *http.Request) (*http.Response, error) {
		account := r.Header.Get("Chatgpt-Account-Id")
		body, ok := responses[account]
		if !ok || r.Header.Get("Authorization") != "Bearer fake-"+account {
			t.Errorf("unexpected discovery credential for account %q", account)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}))
	refresh := func(auth *coreauth.Auth) {
		token, account, base, _, _ := codexDiscoveryCredential(cfg, auth)
		entry := service.codexCatalog[auth.ID]
		entry.cancel = func() {}
		service.fetchCodexModels(ctx, cfg, auth, token, account, base, entry)
	}
	for _, auth := range accounts {
		refresh(auth)
	}

	router := gin.New()
	handler := openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router.GET("/v1/models", handler.OpenAIModels)
	server := httptest.NewServer(router)
	defer server.Close()
	assertRoster := func(want []string) {
		t.Helper()
		for _, path := range []string{"/v1/models", "/v1/models?client_version=0.159.0"} {
			response, err := server.Client().Get(server.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
				Models []struct {
					Slug string `json:"slug"`
				} `json:"models"`
			}
			errDecode := json.NewDecoder(response.Body).Decode(&payload)
			errClose := response.Body.Close()
			if response.StatusCode != http.StatusOK || errDecode != nil || errClose != nil {
				t.Fatalf("%s returned status %d, decode %v, close %v", path, response.StatusCode, errDecode, errClose)
			}
			var got []string
			for _, model := range payload.Data {
				if strings.HasPrefix(model.ID, "discovery-") {
					got = append(got, model.ID)
				}
			}
			for _, model := range payload.Models {
				if strings.HasPrefix(model.Slug, "discovery-") {
					got = append(got, model.Slug)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("%s account roster = %v, want %v", path, got, want)
			}
		}
	}
	assertRoster([]string{"discovery-a/gpt-6.1-terra", "discovery-b/account-b-only"})
	responses["a"] = `{"models":[]}`
	refresh(accounts[0])
	assertRoster([]string{"discovery-b/account-b-only"})
}
