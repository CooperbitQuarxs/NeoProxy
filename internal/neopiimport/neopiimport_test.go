package neopiimport

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// fakeJWT builds an unsigned token with Codex-style claims; never a real secret.
func fakeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(`{"alg":"none"}`)) + "." + enc.EncodeToString(payload) + ".sig"
}

type fixtureRow struct {
	provider, credType string
	data               map[string]any
	disabled           string
}

// newProfile writes a synthetic NeoPi profile using NeoPi's schema and WAL
// journal. The writer stays open so recent rows live only in the WAL.
func newProfile(t *testing.T, rows []fixtureRow) (string, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, DBFileName)+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		CREATE TABLE auth_credentials (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider TEXT NOT NULL,
			credential_type TEXT NOT NULL,
			data TEXT NOT NULL,
			disabled_cause TEXT DEFAULT NULL,
			identity_key TEXT DEFAULT NULL,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		);`); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		raw, _ := json.Marshal(row.data)
		var disabled any
		if row.disabled != "" {
			disabled = row.disabled
		}
		if _, err = db.Exec(`INSERT INTO auth_credentials(provider, credential_type, data, disabled_cause) VALUES (?,?,?,?)`, row.provider, row.credType, string(raw), disabled); err != nil {
			t.Fatal(err)
		}
	}
	return dir, db
}

func fixture(t *testing.T) (string, map[string]Credential) {
	t.Helper()
	hour := float64(time.Now().Add(time.Hour).UnixMilli())
	past := float64(time.Now().Add(-time.Hour).UnixMilli())
	codexAccess := fakeJWT(t, map[string]any{
		"exp":                            time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth":    map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-fixture"},
		"https://api.openai.com/profile": map[string]any{"email": "codex@example.test"},
	})
	rows := []fixtureRow{
		{"openai-codex", "oauth", map[string]any{"access": codexAccess, "refresh": "fixture-refresh-codex", "expires": hour}, ""},
		{"anthropic", "oauth", map[string]any{"access": "sk-ant-oat01-fixture", "refresh": "fixture-refresh-claude", "expires": hour, "email": "claude@example.test", "orgId": "org-fixture", "orgName": "Fixture Org"}, ""},
		{"anthropic", "api_key", map[string]any{"key": "sk-ant-api03-fixturekey1234"}, ""},
		{"openrouter", "api_key", map[string]any{"key": "sk-or-v1-fixture5678"}, ""},
		{"huggingface", "api_key", map[string]any{"key": "hf_fixturekey90ab"}, ""},
		{"cursor", "oauth", map[string]any{"access": "cursor-fixture", "refresh": "x", "expires": hour}, ""},
		{"google-antigravity", "oauth", map[string]any{"access": "ya29.fixture", "refresh": "x", "expires": hour, "email": "ag@example.test"}, ""},
		{"google-antigravity", "oauth", map[string]any{"access": "ya29.fixture2", "refresh": "x", "expires": hour, "email": "ag2@example.test", "projectId": "proj-fixture"}, ""},
		{"zai", "oauth", map[string]any{"access": "zai-minted.fixturekeyzz99", "refresh": "", "expires": 0, "email": "zai@example.test"}, ""},
		{"openai-codex", "oauth", map[string]any{"access": "expired-fixture", "refresh": "x", "expires": past}, ""},
		{"openrouter", "api_key", map[string]any{"key": "sk-or-v1-disabled0000"}, "removed by user"},
		{"nous-portal", "api_key", map[string]any{"key": "nous-fixture-key-0000"}, ""},
		{"xai-oauth", "oauth", map[string]any{"access": "xai-fixture-access", "refresh": "x", "expires": hour, "accountId": "sub-fixture", "email": "xai@example.test"}, ""},
		{"kimi-code", "oauth", map[string]any{"access": "kimi-fixture-access", "refresh": "x", "expires": hour}, ""},
		{"devin", "oauth", map[string]any{"access": "devin-fixture-token", "refresh": "devin-fixture-token", "expires": hour, "apiEndpoint": "https://api.devin.ai"}, ""},
		{"openai", "api_key", map[string]any{"key": "sk-fixture-pinned-0000", "tlsTrust": map[string]any{"ca": "x"}}, ""},
		{"nvidia", "api_key", map[string]any{"key": "nvapi-fixturekey1717"}, ""},
		{"vercel-ai-gateway", "api_key", map[string]any{"key": "vck_fixturekey1818"}, ""},
		{"marineris", "api_key", map[string]any{"key": "mrn_fixturekey1919"}, ""},
	}
	dir, _ := newProfile(t, rows)
	// NeoPi's Marineris extension keeps the gateway address beside the profile,
	// unnormalized as a person typed it at /login.
	if err := os.MkdirAll(filepath.Join(dir, "extensions", "marineris"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extensions", "marineris", "config.json"), []byte(`{"baseUrl":"127.0.0.1:18801/"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	creds, err := Read(context.Background(), dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(creds) != len(rows) {
		t.Fatalf("Read returned %d rows, want %d (WAL rows must be visible)", len(creds), len(rows))
	}
	byID := map[string]Credential{}
	for _, c := range creds {
		byID[c.ID] = c
	}
	return dir, byID
}

func TestDescribeMapsEveryProvider(t *testing.T) {
	_, creds := fixture(t)
	now := time.Now()
	want := []struct {
		id, provider, kind, target, label, plan string
		importable                              bool
		reason                                  string
	}{
		{"1", "openai-codex", KindSubscription, "codex", "codex@example.test", "pro", true, ""},
		{"2", "anthropic", KindSubscription, "claude", "claude@example.test", "", true, ""},
		{"3", "anthropic", KindAPIKey, "claude", "…1234", "", true, ""},
		{"4", "openrouter", KindAPIKey, "openrouter", "…5678", "", true, ""},
		{"5", "huggingface", KindAPIKey, "huggingface", "…90ab", "", true, ""},
		{"6", "cursor", KindSubscription, "", "NeoPi credential 6", "", false, "Cursor"},
		{"7", "google-antigravity", KindSubscription, "antigravity", "ag@example.test", "", false, "project"},
		{"8", "google-antigravity", KindSubscription, "antigravity", "ag2@example.test", "", true, ""},
		{"9", "zai", KindAPIKey, "zai", "zai@example.test", "", true, ""},
		{"10", "openai-codex", KindSubscription, "codex", "NeoPi credential 10", "", false, "expired"},
		{"11", "openrouter", KindAPIKey, "openrouter", "…0000", "", false, "disabled"},
		{"12", "nous-portal", KindAPIKey, "nous-portal", "…0000", "", true, ""},
		{"13", "xai-oauth", KindSubscription, "xai", "xai@example.test", "", true, ""},
		{"14", "kimi-code", KindSubscription, "kimi", "NeoPi credential 14", "", true, ""},
		{"15", "devin", KindSubscription, "devin", "NeoPi credential 15", "", true, ""},
		{"16", "openai", KindAPIKey, "codex", "…0000", "", false, "TLS"},
		{"17", "nvidia", KindAPIKey, "nvidia", "…1717", "", true, ""},
		{"18", "vercel-ai-gateway", KindAPIKey, "vercel-ai-gateway", "…1818", "", true, ""},
		{"19", "marineris", KindAPIKey, "marineris", "…1919", "", true, ""},
	}
	for _, w := range want {
		got := Describe(creds[w.id], now)
		target := ""
		if got.NeoProxyProvider != nil {
			target = *got.NeoProxyProvider
		}
		plan := ""
		if got.Plan != nil {
			plan = *got.Plan
		}
		if got.Provider != w.provider || got.Kind != w.kind || target != w.target || got.Label != w.label || plan != w.plan || got.Importable != w.importable || !strings.Contains(got.Reason, w.reason) {
			t.Errorf("credential %s = %+v (target %q plan %q), want %+v", w.id, got, target, plan, w)
		}
		encoded, _ := json.Marshal(got)
		for _, secret := range []string{"fixture-refresh", "sk-ant-oat01", "fixturekey", "ya29", "xai-fixture-access"} {
			if strings.Contains(string(encoded), secret) {
				t.Errorf("candidate %s leaks a secret: %s", w.id, encoded)
			}
		}
	}
}

func TestBuildAuthFileNativeFormatsAreRefreshLocked(t *testing.T) {
	profile, creds := fixture(t)
	now := time.Now()
	cfg := &config.Config{AuthDir: t.TempDir()}
	for _, tc := range []struct {
		id, provider string
		check        func(map[string]any) string
	}{
		{"1", "codex", func(m map[string]any) string {
			if m["account_id"] != "acct-fixture" || m["plan_type"] != "pro" || m["email"] != "codex@example.test" {
				return "codex identity/plan missing"
			}
			return ""
		}},
		{"2", "claude", func(m map[string]any) string {
			if m["access_token"] != "sk-ant-oat01-fixture" || m["organization_uuid"] != "org-fixture" {
				return "claude token/org missing"
			}
			return ""
		}},
		{"8", "antigravity", func(m map[string]any) string {
			if m["project_id"] != "proj-fixture" {
				return "antigravity project missing"
			}
			return ""
		}},
		{"13", "xai", func(m map[string]any) string {
			if m["auth_kind"] != "oauth" || m["sub"] != "sub-fixture" {
				return "xai oauth kind/sub missing"
			}
			return ""
		}},
		{"14", "kimi", func(m map[string]any) string { return "" }},
		{"15", "devin", func(m map[string]any) string {
			if m["session_token"] != "devin-session-token$devin-fixture-token" {
				return "devin session token not in engine form"
			}
			return ""
		}},
	} {
		file, err := BuildAuthFile(creds[tc.id], profile, now, now)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if file.Provider != tc.provider || file.FileName != FileName(tc.provider, profile, tc.id) {
			t.Fatalf("%s: file %s/%s", tc.provider, file.Provider, file.FileName)
		}
		if strings.Contains(string(file.Data), "refresh_token") || strings.Contains(string(file.Data), "fixture-refresh") || strings.Contains(string(file.Data), `"x"`) {
			t.Fatalf("%s: refresh token copied into auth file", tc.provider)
		}
		var meta map[string]any
		if err = json.Unmarshal(file.Data, &meta); err != nil {
			t.Fatal(err)
		}
		if msg := tc.check(meta); msg != "" {
			t.Fatalf("%s: %s", tc.provider, msg)
		}
		if _, ok := meta["expired"].(string); !ok {
			t.Fatalf("%s: expiry not recorded", tc.provider)
		}
		// The engine's own file synthesis must accept the file as its provider.
		auths, err := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{Config: cfg, AuthDir: cfg.AuthDir, Now: now, IDGenerator: synthesizer.NewStableIDGenerator()}, filepath.Join(cfg.AuthDir, file.FileName), file.Data)
		if err != nil || len(auths) == 0 {
			t.Fatalf("%s: engine synthesis failed: %v", tc.provider, err)
		}
		a := auths[0]
		if a.Provider != tc.provider || !coreauth.IsRefreshLocked(a) {
			t.Fatalf("%s: synthesized provider %q locked=%v", tc.provider, a.Provider, coreauth.IsRefreshLocked(a))
		}
		provenance, ok := coreauth.ImportedFromNeoPi(a)
		if !ok || provenance.Profile != profile || provenance.CredentialID != tc.id || provenance.ImportedAt != now.UnixMilli() {
			t.Fatalf("%s: provenance %+v %v", tc.provider, provenance, ok)
		}
		if exp, okExp := a.ExpirationTime(); !okExp || exp.Before(now) {
			t.Fatalf("%s: engine expiry %v %v", tc.provider, exp, okExp)
		}
	}
	if _, err := BuildAuthFile(creds["6"], profile, now, now); !errors.Is(err, ErrNotImportable) {
		t.Fatalf("cursor: err = %v, want ErrNotImportable", err)
	}
	if _, err := BuildAuthFile(creds["3"], profile, now, now); !errors.Is(err, ErrNotImportable) {
		t.Fatalf("api key as auth file: err = %v, want ErrNotImportable", err)
	}
}

func TestBuildAPIKey(t *testing.T) {
	_, creds := fixture(t)
	now := time.Now()
	for _, tc := range []struct{ id, provider, key, baseURL string }{
		{"3", "claude", "sk-ant-api03-fixturekey1234", ""},
		{"4", "openrouter", "sk-or-v1-fixture5678", ""},
		{"5", "huggingface", "hf_fixturekey90ab", ""},
		{"9", "zai", "zai-minted.fixturekeyzz99", ""},
		{"12", "nous-portal", "nous-fixture-key-0000", ""},
		{"17", "nvidia", "nvapi-fixturekey1717", ""},
		{"18", "vercel-ai-gateway", "vck_fixturekey1818", ""},
		{"19", "marineris", "mrn_fixturekey1919", "http://127.0.0.1:18801/v1"},
	} {
		got, err := BuildAPIKey(creds[tc.id], now)
		if err != nil || got.Provider != tc.provider || got.APIKey != tc.key || got.BaseURL != tc.baseURL {
			t.Fatalf("credential %s = %+v, %v", tc.id, got, err)
		}
	}
	for _, id := range []string{"1", "11", "16"} {
		if _, err := BuildAPIKey(creds[id], now); !errors.Is(err, ErrNotImportable) {
			t.Fatalf("credential %s: err = %v, want ErrNotImportable", id, err)
		}
	}
}

// Grok Build is NeoPi's mirror of the xai-oauth sign-in: no email, no account
// id and a sentinel refresh value. It imports as the engine's xai sign-in with
// the account taken from the token's subject.
func TestGrokBuildImportsAsXAISignIn(t *testing.T) {
	access := fakeJWT(t, map[string]any{"sub": "sub-grok-fixture", "exp": time.Now().Add(time.Hour).Unix()})
	profile, _ := newProfile(t, []fixtureRow{
		{"grok-build", "oauth", map[string]any{"access": access, "refresh": "__grok-build-shadows-xai-oauth__", "expires": float64(time.Now().Add(time.Hour).UnixMilli())}, ""},
		{"grok-build", "oauth", map[string]any{"access": access, "refresh": "__grok-build-shadows-xai-oauth__", "expires": float64(time.Now().Add(-time.Hour).UnixMilli())}, ""},
	})
	creds, err := Read(context.Background(), profile)
	if err != nil || len(creds) != 2 {
		t.Fatalf("Read = %d, %v", len(creds), err)
	}
	now := time.Now()
	live := Describe(creds[0], now)
	if !live.Importable || live.Kind != KindSubscription || live.NeoProxyProvider == nil || *live.NeoProxyProvider != "xai" {
		t.Fatalf("live grok-build = %+v", live)
	}
	if expired := Describe(creds[1], now); expired.Importable || !strings.Contains(expired.Reason, "Import xai-oauth instead") {
		t.Fatalf("expired grok-build = %+v", expired)
	}
	file, err := BuildAuthFile(creds[0], profile, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file.Data), "shadows-xai-oauth") {
		t.Fatal("refresh sentinel copied into auth file")
	}
	var meta map[string]any
	if err = json.Unmarshal(file.Data, &meta); err != nil {
		t.Fatal(err)
	}
	if file.Provider != "xai" || meta["type"] != "xai" || meta["auth_kind"] != "oauth" || meta["access_token"] != access || meta["sub"] != "sub-grok-fixture" {
		t.Fatalf("grok-build auth file = %s", file.Data)
	}
	if meta[coreauth.MetadataRefreshLocked] != true {
		t.Fatal("grok-build import is not refresh-locked")
	}
}

func TestProfileValidationAndReadOnly(t *testing.T) {
	profile, db := newProfile(t, []fixtureRow{{"openrouter", "api_key", map[string]any{"key": "sk-or-v1-fixture5678"}, ""}})
	before, _ := os.ReadDir(profile)
	if got, err := ResolveProfile(profile + "/"); err != nil || got != profile {
		t.Fatalf("ResolveProfile = %q, %v", got, err)
	}
	for _, bad := range []string{"relative/agent", filepath.Join(profile, "missing"), t.TempDir()} {
		if _, err := ResolveProfile(bad); !errors.Is(err, ErrNotProfile) {
			t.Fatalf("ResolveProfile(%q) err = %v, want ErrNotProfile", bad, err)
		}
	}
	notNeoPi := t.TempDir()
	if err := os.WriteFile(filepath.Join(notNeoPi, DBFileName), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(context.Background(), notNeoPi); !errors.Is(err, ErrNotProfile) {
		t.Fatalf("Read(non-sqlite) err = %v, want ErrNotProfile", err)
	}
	if _, err := Find(context.Background(), profile, "999"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("Find(missing) err = %v, want ErrCredentialNotFound", err)
	}
	c, err := Find(context.Background(), profile, "1")
	if err != nil || c.Provider != "openrouter" {
		t.Fatalf("Find = %+v, %v", c, err)
	}
	after, _ := os.ReadDir(profile)
	if len(after) != len(before) {
		t.Fatalf("profile directory changed: %d -> %d entries", len(before), len(after))
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM auth_credentials`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("NeoPi database changed: %d %v", count, err)
	}
}

func TestDefaultProfileHonorsEnvironment(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "/srv/neopi/agent")
	if got, _ := DefaultProfile(); got != "/srv/neopi/agent" {
		t.Fatalf("DefaultProfile = %q", got)
	}
	t.Setenv("PI_CODING_AGENT_DIR", "")
	t.Setenv("HOME", "/home/fixture")
	if got, _ := DefaultProfile(); got != "/home/fixture/.omp/agent" {
		t.Fatalf("DefaultProfile = %q", got)
	}
}
