package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func codexRefreshFixture() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: "codex.json", FileName: "codex.json", Provider: "codex",
		Metadata: map[string]any{
			"type": "codex", "access_token": "access-old", "refresh_token": "refresh-old",
			"last_refresh": "2026-10-12T12:00:00Z",
			"note":         "original", "proxy_url": "http://original", "disabled": false,
		},
	}
}

func writeCodexRefreshFixture(t *testing.T, path string, metadata map[string]any) {
	t.Helper()
	if errWrite := codex.WriteJSONFileAtomic(path, metadata); errWrite != nil {
		t.Fatal(errWrite)
	}
}

func readCodexRefreshFixture(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(raw, &metadata); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return metadata
}

func TestFileTokenStoreCodexRefreshPreservesDiskConfiguration(t *testing.T) {
	for _, withStorage := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata", true: "storage"}[withStorage], func(t *testing.T) {
			baseDir := t.TempDir()
			path := filepath.Join(baseDir, "codex.json")
			base := codexRefreshFixture()
			updated := base.Clone()
			updated.Metadata["access_token"] = "access-new"
			updated.Metadata["refresh_token"] = "refresh-new"
			updated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, nil, updated.Metadata)
			if withStorage {
				updated.Storage = &codex.CodexTokenStorage{AccessToken: "stale-storage"}
			}
			// Operator changes do not change the credential version.
			disk := base.Clone().Metadata
			disk["note"] = "disk-note"
			disk["new_property"] = map[string]any{"keep": true}
			disk["disabled"] = true
			delete(disk, "proxy_url")
			writeCodexRefreshFixture(t, path, disk)

			store := NewFileTokenStore()
			store.SetBaseDir(baseDir)
			if _, errSave := store.Save(context.Background(), updated); errSave != nil {
				t.Fatal(errSave)
			}
			saved := readCodexRefreshFixture(t, path)
			if saved["note"] != "disk-note" || saved["disabled"] != true || saved["new_property"] == nil {
				t.Fatalf("disk configuration overwritten: %#v", saved)
			}
			if _, exists := saved["proxy_url"]; exists {
				t.Fatal("deleted disk property was restored")
			}
			if saved["access_token"] != "access-new" || saved["refresh_token"] != "refresh-new" {
				t.Fatalf("refreshed credential was not saved: %#v", saved)
			}
			if updated.CodexRefreshPatch != nil || !updated.Disabled {
				t.Fatalf("saved auth was not synchronized: %#v", updated)
			}
		})
	}
}

func TestFileTokenStoreCodexStaleRefreshKeepsRotatedGrant(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "codex.json")
	base := codexRefreshFixture()
	writeCodexRefreshFixture(t, path, base.Metadata)
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	rotated := base.Clone()
	rotated.Metadata["access_token"] = "access-new"
	rotated.Metadata["refresh_token"] = "refresh-new"
	rotated.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, nil, rotated.Metadata)
	if _, errSave := store.Save(context.Background(), rotated); errSave != nil {
		t.Fatal(errSave)
	}
	// A refresh started from the same old version must not overwrite the rotated grant.
	stale := base.Clone()
	stale.Metadata["access_token"] = "access-stale-result"
	stale.Metadata["refresh_token"] = "refresh-stale-result"
	stale.CodexRefreshPatch = codex.NewCredentialRefreshPatch(base.Metadata, nil, stale.Metadata)
	if _, errSave := store.Save(context.Background(), stale); errSave != nil {
		t.Fatal(errSave)
	}
	// A stale configuration snapshot is not a credential update either.
	if _, errSave := store.Save(context.Background(), base.Clone()); errSave != nil {
		t.Fatal(errSave)
	}
	if saved := readCodexRefreshFixture(t, path); saved["refresh_token"] != "refresh-new" {
		t.Fatalf("token rotation lost: %#v", saved)
	}
}

func TestFileTokenStoreCodexLoginReplacementIsExplicit(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "codex.json")
	base := codexRefreshFixture()
	writeCodexRefreshFixture(t, path, base.Metadata)
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	login := &cliproxyauth.Auth{
		ID: "codex.json", FileName: "codex.json", Provider: "codex",
		Storage: &codex.CodexTokenStorage{AccessToken: "login-new", RefreshToken: "login-ref-new"},
	}
	if _, errSave := store.Save(cliproxyauth.WithAuthCreationIntent(context.Background()), login); errSave != nil {
		t.Fatal(errSave)
	}
	stale := base.Clone()
	stale.Storage = &codex.CodexTokenStorage{AccessToken: "access-old", RefreshToken: "refresh-old"}
	if _, errSave := store.Save(context.Background(), stale); errSave != nil {
		t.Fatal(errSave)
	}
	if saved := readCodexRefreshFixture(t, path); saved["refresh_token"] != "login-ref-new" {
		t.Fatalf("stale storage save overwrote the login: %#v", saved)
	}
}
