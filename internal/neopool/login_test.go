package neopool_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestOwnedAPIKeyUsesExistingEngineConfigurationAndStableAuthIdentity(t *testing.T) {
	identity, err := neo.Open(filepath.Join(t.TempDir(), "neo.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errClose := identity.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	if _, err = identity.DB().Exec(`INSERT INTO neo_users(id,handle,name,role,state,created_at) VALUES('owner-a','owner-a','Owner A','owner','active',0)`); err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	pool, err := neopool.New(identity.DB(), manager)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(cfgPath, []byte("port: 8317\nauth-dir: auths\n"), 0600); err != nil {
		t.Fatal(err)
	}
	engine := management.NewHandler(&config.Config{}, cfgPath, manager)
	neopool.NewHTTP(pool, engine)
	// The production service supplies this reload callback. Exercise its actual
	// existing synthesis/registration mechanism rather than a second key engine.
	var reloadErr error
	engine.SetConfigReloadHook(func(ctx context.Context, cfg *config.Config) {
		auths, errSynth := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
		if errSynth != nil {
			reloadErr = errSynth
			return
		}
		for _, a := range auths {
			if _, errRegister := manager.Register(ctx, a); errRegister != nil {
				reloadErr = errRegister
				return
			}
		}
	})
	ctx := neo.WithPrincipal(context.Background(), neo.Principal{UserID: "owner-a", Role: neo.RoleOwner, Session: true})
	record, err := engine.CreateAccountAPIKey(ctx, "codex", "synthetic-provider-key-not-a-real-secret")
	if err != nil {
		t.Fatal(err)
	}
	if reloadErr != nil {
		t.Fatal(reloadErr)
	}
	registered, ok := manager.GetByID(record.ID)
	if !ok || registered.Attributes["api_key"] != "synthetic-provider-key-not-a-real-secret" {
		t.Fatal("existing engine auth manager did not receive credential")
	}
	snapshot, err := pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range snapshot.Accounts {
		if a.ID == neopool.AccountID(record.ID) {
			found = true
			if a.OwnerID != "owner-a" || a.EngineAuthID != record.ID {
				t.Fatalf("login completion attribution: %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("actual engine credential absent from inventory")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "synthetic-provider-key-not-a-real-secret") {
		t.Fatal("credential not persisted in existing engine config")
	}
	if _, err = engine.CreateAccountAPIKey(ctx, "codex", "synthetic-provider-key-not-a-real-secret"); !errors.Is(err, coreauth.ErrAccountKeyConflict) {
		t.Fatalf("duplicate key silently adopted: %v", err)
	}
	if _, err = engine.CreateAccountAPIKey(ctx, "https://arbitrary-upstream.invalid", "another-key"); !errors.Is(err, coreauth.ErrAccountKeyInvalid) {
		t.Fatalf("owner chose arbitrary upstream: %v", err)
	}
	if _, err = engine.CreateAccountAPIKey(context.Background(), "codex", "another-key"); !errors.Is(err, coreauth.ErrAccountKeyInvalid) {
		t.Fatalf("operator request claimed owner without session: %v", err)
	}
}
