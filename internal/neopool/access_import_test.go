package neopool_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiimport"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const neoPiRefreshSecret = "neopi-refresh-secret-never-copied"

// neoPiVault writes a synthetic NeoPi profile with NeoPi's credential schema
// in WAL mode, keeping the writer open like a running NeoPi.
func neoPiVault(t *testing.T, rows ...[3]any) (string, *sql.DB) {
	t.Helper()
	profile := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(profile, neopiimport.DBFileName)+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`CREATE TABLE auth_credentials (id INTEGER PRIMARY KEY AUTOINCREMENT, provider TEXT NOT NULL, credential_type TEXT NOT NULL, data TEXT NOT NULL, disabled_cause TEXT DEFAULT NULL, identity_key TEXT DEFAULT NULL, created_at INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		data, _ := json.Marshal(row[2])
		if _, err = db.Exec(`INSERT INTO auth_credentials(provider,credential_type,data) VALUES(?,?,?)`, row[0], row[1], string(data)); err != nil {
			t.Fatal(err)
		}
	}
	return profile, db
}

func codexAccess(exp time.Time, plan string) string {
	return jwt(map[string]any{"exp": exp.Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan, "chatgpt_account_id": "acct-neopi"}})
}

func importedEngineAuth(t *testing.T, f *accessFixture) *coreauth.Auth {
	t.Helper()
	for _, a := range f.manager.List() {
		if _, ok := coreauth.ImportedFromNeoPi(a); ok {
			return a
		}
	}
	t.Fatal("no imported engine credential")
	return nil
}

func TestNeoPiImportListsImportsAndLocksRefresh(t *testing.T) {
	f := newAccessFixture(t)
	access := codexAccess(time.Now().Add(time.Hour), "pro")
	profile, _ := neoPiVault(t,
		[3]any{"openai-codex", "oauth", map[string]any{"access": access, "refresh": neoPiRefreshSecret, "expires": time.Now().Add(time.Hour).UnixMilli(), "email": "codex@example.test", "accountId": "acct-neopi"}},
		[3]any{"openrouter", "api_key", map[string]any{"key": routerKey}},
		[3]any{"github-copilot", "oauth", map[string]any{"access": "copilot-token", "refresh": "x", "expires": time.Now().Add(time.Hour).UnixMilli()}},
	)
	listPath := "/v8/neo/access/import/neopi?profile=" + profile

	f.json("owner-a", "GET", listPath, "", 403)
	f.json("admin", "GET", "/v8/neo/access/import/neopi?profile="+t.TempDir(), "", 400)
	f.json("admin", "GET", "/v8/neo/access/import/neopi?profile=relative/agent", "", 400)
	raw := f.json("admin", "GET", listPath, "", 200)
	assertNoSecrets(t, raw, access, neoPiRefreshSecret, routerKey)
	var listed struct {
		Profile     string                  `json:"profile"`
		Credentials []neopiimport.Candidate `json:"credentials"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Profile != profile || len(listed.Credentials) != 3 {
		t.Fatalf("list = %s", raw)
	}
	codex, router, copilot := listed.Credentials[0], listed.Credentials[1], listed.Credentials[2]
	if codex.Kind != "subscription" || *codex.NeoProxyProvider != "codex" || *codex.Plan != "pro" || codex.Label != "codex@example.test" || !codex.Importable || codex.AlreadyImported != nil {
		t.Fatalf("codex candidate = %+v", codex)
	}
	if router.Kind != "apiKey" || *router.NeoProxyProvider != "openrouter" || router.Label != "…0002" || !router.Importable {
		t.Fatalf("openrouter candidate = %+v", router)
	}
	if copilot.Importable || copilot.Reason == "" || copilot.NeoProxyProvider != nil {
		t.Fatalf("github-copilot candidate = %+v", copilot)
	}

	f.json("owner-a", "POST", "/v8/neo/access/import/neopi", `{"profile":"`+profile+`","credentialIds":["1"]}`, 403)
	raw = f.json("admin", "POST", "/v8/neo/access/import/neopi", `{"profile":"`+profile+`","credentialIds":["1","2","3"],"ownerId":"owner-a"}`, 200)
	assertNoSecrets(t, raw, access, neoPiRefreshSecret, routerKey)
	var result struct {
		Items  []neopool.AccessItem `json:"items"`
		Failed []struct {
			CredentialID string `json:"credentialId"`
			Error        string `json:"error"`
		} `json:"failed"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || len(result.Failed) != 1 || result.Failed[0].CredentialID != "3" || !strings.Contains(result.Failed[0].Error, "Copilot") {
		t.Fatalf("import result = %s", raw)
	}
	sub, key := result.Items[0], result.Items[1]
	if sub.Kind != "imported" || sub.Refresh != "locked" || sub.Provider != "codex" || sub.OwnerID != "owner-a" || sub.ImportedFrom == nil ||
		sub.ImportedFrom.Profile != profile || sub.ImportedFrom.CredentialID != "1" || sub.ImportedFrom.ImportedAt == 0 ||
		!slices.Contains(sub.Actions, "reimport") || !slices.Contains(sub.Actions, "signInSeparately") || slices.Contains(sub.Actions, "refresh") || sub.Health != "healthy" {
		t.Fatalf("imported subscription item = %+v", sub)
	}
	if key.Kind != "apiKey" || key.Provider != "openrouter" || key.OwnerID != "owner-a" || key.ImportedFrom != nil {
		t.Fatalf("imported key item = %+v", key)
	}

	locked := importedEngineAuth(t, f)
	data, err := os.ReadFile(filepath.Join(f.authDir, locked.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), neoPiRefreshSecret) || strings.Contains(string(data), "refresh_token") || !strings.HasPrefix(locked.FileName, "neopi-codex-") {
		t.Fatalf("auth file %s carries a refresh token", locked.FileName)
	}
	if locked.Metadata["access_token"] != access || locked.Metadata["account_id"] != "acct-neopi" || !coreauth.IsRefreshLocked(locked) {
		t.Fatal("imported codex credential is not in native, locked form")
	}
	if _, err = f.manager.ForceRefreshAuth(context.Background(), locked.ID); !errors.Is(err, coreauth.ErrRefreshLocked) {
		t.Fatalf("manual refresh of an imported credential: %v, want ErrRefreshLocked", err)
	}

	raw = f.json("admin", "GET", listPath, "", 200)
	if err = json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Credentials[0].AlreadyImported == nil || *listed.Credentials[0].AlreadyImported != sub.ID ||
		listed.Credentials[1].AlreadyImported == nil || *listed.Credentials[1].AlreadyImported != key.ID {
		t.Fatalf("alreadyImported not reported: %s", raw)
	}
	raw = f.json("admin", "POST", "/v8/neo/access/import/neopi", `{"profile":"`+profile+`","credentialIds":["1"]}`, 422)
	if !strings.Contains(string(raw), "Re-import") {
		t.Fatalf("duplicate import: %s", raw)
	}
}

func TestNeoPiReimportReplacesTokenInPlace(t *testing.T) {
	f := newAccessFixture(t)
	first := codexAccess(time.Now().Add(time.Hour), "plus")
	profile, vault := neoPiVault(t,
		[3]any{"openai-codex", "oauth", map[string]any{"access": first, "refresh": neoPiRefreshSecret, "expires": time.Now().Add(time.Hour).UnixMilli(), "email": "codex@example.test"}},
		[3]any{"openrouter", "api_key", map[string]any{"key": routerKey}},
	)
	raw := f.json("admin", "POST", "/v8/neo/access/import/neopi", `{"profile":"`+profile+`","credentialIds":["1","2"],"ownerId":"owner-a"}`, 200)
	var result struct {
		Items []neopool.AccessItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	item, keyItem := result.Items[0], result.Items[1]

	// The copied token expires; NeoProxy may not refresh it and says so.
	expiredAuth := importedEngineAuth(t, f)
	expiredAuth.Metadata["expired"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	expiredAuth.Metadata["access_token"] = "opaque-expired-token"
	if _, err := f.manager.Update(context.Background(), expiredAuth); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.ForceRefreshAuth(context.Background(), expiredAuth.ID); !errors.Is(err, coreauth.ErrRefreshLocked) {
		t.Fatalf("refresh: %v", err)
	}
	list := listOf(t, f.json("admin", "GET", "/v8/neo/access", "", 200))
	for _, it := range list.Items {
		if it.ID == item.ID && (it.Health != "expired" || it.HealthMessage != coreauth.RefreshLockedMessage) {
			t.Fatalf("expired imported item = %s %q", it.Health, it.HealthMessage)
		}
	}

	// NeoPi refreshed the account meanwhile (new token, new plan).
	second := codexAccess(time.Now().Add(2*time.Hour), "pro")
	data, _ := json.Marshal(map[string]any{"access": second, "refresh": "rotated-" + neoPiRefreshSecret, "expires": time.Now().Add(2 * time.Hour).UnixMilli(), "email": "codex@example.test"})
	if _, err := vault.Exec(`UPDATE auth_credentials SET data=? WHERE id=1`, string(data)); err != nil {
		t.Fatal(err)
	}
	f.json("member-a", "POST", "/v8/neo/access/"+item.ID+"/reimport", "", 403)
	f.json("owner-b", "POST", "/v8/neo/access/"+item.ID+"/reimport", "", 403)
	f.json("admin", "POST", "/v8/neo/access/"+keyItem.ID+"/reimport", "", 400)
	raw = f.json("owner-a", "POST", "/v8/neo/access/"+item.ID+"/reimport", "", 200)
	assertNoSecrets(t, raw, second, neoPiRefreshSecret)
	again := itemOf(t, raw)
	if again.ID != item.ID || again.OwnerID != "owner-a" || again.Health != "healthy" || again.Kind != "imported" || again.Refresh != "locked" || again.Plan.ID != "pro" {
		t.Fatalf("re-imported item = %+v", again)
	}
	current := importedEngineAuth(t, f)
	if current.ID != expiredAuth.ID || current.Metadata["access_token"] != second || current.Unavailable || !coreauth.IsRefreshLocked(current) {
		t.Fatalf("engine credential after re-import: unavailable=%v token replaced=%v", current.Unavailable, current.Metadata["access_token"] == second)
	}
	fileData, err := os.ReadFile(filepath.Join(f.authDir, current.FileName))
	if err != nil || strings.Contains(string(fileData), neoPiRefreshSecret) {
		t.Fatalf("re-imported file carries a refresh token (%v)", err)
	}

	if _, err = vault.Exec(`DELETE FROM auth_credentials WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	raw = f.json("admin", "POST", "/v8/neo/access/"+item.ID+"/reimport", "", 404)
	if !strings.Contains(string(raw), "no longer has this credential") {
		t.Fatalf("missing NeoPi credential: %s", raw)
	}
}
