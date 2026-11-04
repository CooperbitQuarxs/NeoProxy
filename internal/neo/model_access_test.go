package neo

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMatchModelPatterns(t *testing.T) {
	for _, test := range []struct {
		patterns []string
		id       string
		want     bool
	}{
		{[]string{"gpt-5"}, "gpt-5", true},
		{[]string{"gpt-5"}, "gpt-5-mini", false},
		{[]string{"gpt-5*"}, "gpt-5-mini", true},
		{[]string{"gpt-5*"}, "gpt-5", true},
		{[]string{"gpt-5*"}, "gpt-4o", false},
		{[]string{"*"}, "anything", true},
		{[]string{"o3", "gpt-5*"}, "o3", true},
		{nil, "o3", false},
	} {
		if got := MatchModel(test.patterns, test.id); got != test.want {
			t.Errorf("MatchModel(%q, %q) = %v", test.patterns, test.id, got)
		}
	}
}

func TestAllowsDirectLayersProviderUserAndKey(t *testing.T) {
	p := Principal{ProviderAccess: []string{"codex", "xai"}, ModelAccess: ModelAccess{"codex": {"gpt-5*"}}}
	for _, test := range []struct {
		provider, model string
		want            bool
	}{
		{"codex", "codex/gpt-5", true},
		{"codex", "codex/gpt-5-mini", true},
		{"codex", "codex/o3", false},
		{"xai", "xai/grok-4", true}, // no entry: every exposed model
		{"zai", "zai/glm-4.6", false},
	} {
		if got := p.AllowsDirect(test.provider, test.model); got != test.want {
			t.Errorf("user %s: got %v", test.model, got)
		}
	}
	p.KeyModels = []string{"codex/gpt-5-mini", "xai/*"}
	for _, test := range []struct {
		provider, model string
		want            bool
	}{
		{"codex", "codex/gpt-5", false},
		{"codex", "codex/gpt-5-mini", true},
		{"xai", "xai/grok-4", true},
	} {
		if got := p.AllowsDirect(test.provider, test.model); got != test.want {
			t.Errorf("key %s: got %v", test.model, got)
		}
	}
	p.KeyModels = []string{}
	if p.AllowsDirect("xai", "xai/grok-4") {
		t.Fatal("empty stored key scope widened")
	}
}

func TestModelAccessValidationStorageAndPruning(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	admin := testUser(t, s, "admin", RoleAdmin)
	u := testUser(t, s, "member", RoleMember)
	in := testInput(u)
	in.ProviderAccess = []string{"codex"}
	for _, bad := range []ModelAccess{
		{"codex": {}},
		{"codex": {""}},
		{"codex": {"gpt*5"}},
		{"codex": {"gpt-5**"}},
		{"codex": {"gpt 5"}},
		{"codex": {"o3", "o3"}},
		{" codex": {"o3"}},
	} {
		in.ModelAccess = bad
		if _, err := s.UpdateUser(ctx, u.ID, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %v: %v", bad, err)
		}
	}
	in.ModelAccess = ModelAccess{"codex": {"gpt-5*", "o3"}, "xai": {"grok-4"}}
	u, err := s.UpdateUser(ctx, u.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ModelAccess{"codex": {"gpt-5*", "o3"}}); !reflect.DeepEqual(u.ModelAccess, want) {
		t.Fatalf("stored %v, want %v (xai has no provider access)", u.ModelAccess, want)
	}
	// A client that predates modelAccess keeps the allowlist when it saves.
	in.ModelAccess = nil
	if u, err = s.UpdateUser(ctx, u.ID, in); err != nil || len(u.ModelAccess["codex"]) != 2 {
		t.Fatalf("nil modelAccess dropped the allowlist: %v %v", u.ModelAccess, err)
	}
	p, err := s.PrincipalForUser(ctx, u.ID)
	if err != nil || !reflect.DeepEqual(p.ModelAccess, u.ModelAccess) {
		t.Fatalf("principal %v %v", p.ModelAccess, err)
	}
	in.ModelAccess = ModelAccess{}
	if u, err = s.UpdateUser(ctx, u.ID, in); err != nil || u.ModelAccess != nil {
		t.Fatalf("empty modelAccess kept %v %v", u.ModelAccess, err)
	}
	raw, _ := json.Marshal(u)
	var shape map[string]any
	_ = json.Unmarshal(raw, &shape)
	if _, ok := shape["modelAccess"]; ok {
		t.Fatalf("empty allowlist serialized: %s", raw)
	}

	if _, _, err = s.Mint(ctx, testActor(admin), u.ID, KeyRequest{Label: "bad", Models: []string{"codex/gpt*5"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("bad key pattern accepted", err)
	}
	key, secret, err := s.Mint(ctx, testActor(admin), u.ID, KeyRequest{Label: "narrow", Models: []string{"codex/gpt-5*"}})
	if err != nil || !reflect.DeepEqual(key.Models, []string{"codex/gpt-5*"}) {
		t.Fatal(key, err)
	}
	p, err = s.AuthenticateKey(ctx, secret)
	if err != nil || !reflect.DeepEqual(p.KeyModels, []string{"codex/gpt-5*"}) {
		t.Fatalf("key principal %v %v", p.KeyModels, err)
	}
	_, secret, err = s.Mint(ctx, testActor(admin), u.ID, KeyRequest{Label: "wide", Models: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = s.AuthenticateKey(ctx, secret); err != nil || p.KeyModels != nil {
		t.Fatalf("empty models narrowed the key %v %v", p.KeyModels, err)
	}
	if u, err = s.User(ctx, u.ID); err != nil || len(u.Keys) != 2 {
		t.Fatalf("listed keys %+v %v", u.Keys, err)
	}
	for _, k := range u.Keys {
		if (k.Label == "narrow") != reflect.DeepEqual(k.Models, []string{"codex/gpt-5*"}) || (k.Label == "wide" && k.Models != nil) {
			t.Fatalf("listed key %+v", k)
		}
	}
}

func TestIdentitySchemaUpgradesFromV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u := testUser(t, s, "legacy", RoleMember)
	for _, q := range []string{"ALTER TABLE neo_users DROP COLUMN model_access", "ALTER TABLE neo_keys DROP COLUMN models", "PRAGMA user_version=1"} {
		if _, err = s.DB().ExecContext(t.Context(), q); err != nil {
			t.Fatal(q, err)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.DB().QueryRowContext(t.Context(), "PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatal(version, err)
	}
	got, err := s.User(t.Context(), u.ID)
	if err != nil || got.ModelAccess != nil {
		t.Fatalf("upgraded user %+v %v", got, err)
	}
}
