package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServiceCatalogStartupAndConfigReload(t *testing.T) {
	fixture := func(name string) string {
		t.Helper()
		path, errAbs := filepath.Abs(filepath.Join("..", "..", "internal", "registry", "models", name))
		if errAbs != nil {
			t.Fatal(errAbs)
		}
		return path
	}
	writeCatalog := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
			t.Fatal(errWrite)
		}
		return path
	}
	// Byte-distinct copies of a valid catalog: the store publishes raw bytes.
	original := registry.GetCodexClientModelsJSON()
	originalPath := writeCatalog("original.json", original)
	first := append(append([]byte(nil), original...), '\n')
	second := append(append([]byte(nil), original...), '\n', '\n')
	cfg := &config.Config{Models: config.ModelCatalogs{
		Catalog:      fixture("models.json"),
		CodexCatalog: writeCatalog("first.json", first),
	}}
	s := &Service{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	awaitCatalog := func(want []byte) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for string(registry.GetCodexClientModelsJSON()) != string(want) {
			if time.Now().After(deadline) {
				t.Fatal("service did not publish the configured Codex catalog")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	defer func() {
		cancel()
		restoreCtx, restoreCancel := context.WithCancel(context.Background())
		defer restoreCancel()
		restoreCfg := *cfg
		restoreCfg.Home.Enabled = false
		restoreCfg.Models = config.ModelCatalogs{Catalog: fixture("models.json"), CodexCatalog: originalPath}
		restore := &Service{cfg: &restoreCfg}
		if string(registry.GetCodexClientModelsJSON()) != string(original) {
			restore.startModelCatalogUpdaters(restoreCtx)
			awaitCatalog(original)
		}
	}()

	// Exercise the startup hook used by Run without starting a listening server.
	s.startModelCatalogUpdaters(ctx)
	awaitCatalog(first)
	updated := *cfg
	updated.Models.CodexCatalog = writeCatalog("second.json", second)
	if commit := s.commitConfigUpdate(&updated); commit.cfg == nil {
		t.Fatal("config update rejected")
	}
	awaitCatalog(second)

	// The same source must restart under a new service lifetime after cancellation.
	cancel()
	if errWrite := os.WriteFile(updated.Models.CodexCatalog, first, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	restartCtx, restartCancel := context.WithCancel(context.Background())
	defer restartCancel()
	s.startModelCatalogUpdaters(restartCtx)
	awaitCatalog(first)
}
