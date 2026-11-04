package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func lockAuth(a *Auth, expires time.Time) {
	a.Metadata[MetadataRefreshLocked] = true
	a.Metadata[MetadataImportedFrom] = map[string]any{
		"source":        "neopi",
		"profile":       "/home/test/.omp/agent",
		"credential_id": "7",
		"imported_at":   float64(1_700_000_000_000),
	}
	a.Metadata["expired"] = expires.UTC().Format(time.RFC3339)
}

// A locked credential is never refreshed: not on a 401, not by the background
// refresher, not by a forced (manual) refresh, even when a refresh token is
// present in its file.
func TestRefreshLockedCredentialIsNeverRefreshed(t *testing.T) {
	m, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)
	locked, _ := m.GetByID(primary.ID)
	lockAuth(locked, time.Now().Add(time.Hour))
	if _, err := m.Update(context.Background(), locked); err != nil {
		t.Fatalf("update: %v", err)
	}

	resp, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute error = %v, want fallback to backup", err)
	}
	if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("payload = %q, want backup response", got)
	}
	if got := executor.RefreshCalls(); got != 0 {
		t.Fatalf("Refresh calls after 401 = %d, want 0", got)
	}
	current, _ := m.GetByID(primary.ID)
	if current.StatusMessage != RefreshLockedMessage {
		t.Fatalf("status message = %q, want %q", current.StatusMessage, RefreshLockedMessage)
	}

	m.refreshAuth(context.Background(), primary.ID)
	if _, errForce := m.ForceRefreshAuth(context.Background(), primary.ID); !errors.Is(errForce, ErrRefreshLocked) {
		t.Fatalf("ForceRefreshAuth error = %v, want ErrRefreshLocked", errForce)
	}
	if _, refreshed := m.tryRefreshAfterUnauthorized(context.Background(), current, &Error{HTTPStatus: http.StatusUnauthorized, Message: "401"}, false); refreshed {
		t.Fatal("tryRefreshAfterUnauthorized refreshed a locked credential")
	}
	if got := executor.RefreshCalls(); got != 0 {
		t.Fatalf("Refresh calls = %d, want 0", got)
	}
	if final, _ := m.GetByID(primary.ID); final.Metadata["access_token"] != "stale-access-token" {
		t.Fatalf("locked access token was replaced: %v", final.Metadata["access_token"])
	}
}

func TestRefreshLockedExpiredCredentialStopsWithMessage(t *testing.T) {
	m, executor, primary, _, _ := newUnauthorizedRefreshFixture(t, false)
	locked, _ := m.GetByID(primary.ID)
	lockAuth(locked, time.Now().Add(-time.Minute))
	if _, err := m.Update(context.Background(), locked); err != nil {
		t.Fatalf("update: %v", err)
	}
	current, _ := m.GetByID(primary.ID)
	if m.shouldRefresh(current, time.Now()) {
		t.Fatal("shouldRefresh = true for a refresh-locked credential")
	}
	if !RefreshLockExpired(current, time.Now()) {
		t.Fatal("RefreshLockExpired = false for an expired locked credential")
	}
	if _, err := m.ForceRefreshAuth(context.Background(), primary.ID); !errors.Is(err, ErrRefreshLocked) {
		t.Fatalf("ForceRefreshAuth error = %v, want ErrRefreshLocked", err)
	}
	if got := executor.RefreshCalls(); got != 0 {
		t.Fatalf("Refresh calls = %d, want 0", got)
	}
	final, _ := m.GetByID(primary.ID)
	if !final.Unavailable || final.Status != StatusError || final.StatusMessage != RefreshLockedMessage {
		t.Fatalf("state = unavailable:%v status:%s message:%q, want expired with lock message", final.Unavailable, final.Status, final.StatusMessage)
	}
}

func TestImportedFromNeoPiProvenance(t *testing.T) {
	a := &Auth{Metadata: map[string]any{}}
	if IsRefreshLocked(a) {
		t.Fatal("plain auth reported locked")
	}
	if _, ok := ImportedFromNeoPi(a); ok {
		t.Fatal("plain auth reported imported")
	}
	lockAuth(a, time.Now().Add(time.Hour))
	if !IsRefreshLocked(a) {
		t.Fatal("locked auth not reported locked")
	}
	p, ok := ImportedFromNeoPi(a)
	if !ok || p.Profile != "/home/test/.omp/agent" || p.CredentialID != "7" || p.ImportedAt != 1_700_000_000_000 {
		t.Fatalf("provenance = %+v, %v", p, ok)
	}
}
