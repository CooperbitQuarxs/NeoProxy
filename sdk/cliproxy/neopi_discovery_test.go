package cliproxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// A listing that names no models serves the rows the merge keeps for one
// cycle and is retried after the short interval, as NeoPi's model manager
// does not cache it as authoritative; a listing that names models stays for
// the full TTL.
func TestNeoPiDiscoveryRetriesEmptyListingSoon(t *testing.T) {
	kept := []*ModelInfo{{ID: "black-forest-labs/FLUX-2-pro", Type: registry.OpenAIImageModelType}}
	for _, tc := range []struct {
		name      string
		result    error
		wantStale bool
	}{
		{"empty listing", fmt.Errorf("deepinfra models: %w", &registry.NeoPiEmptyListing{Models: kept}), true},
		{"listing with models", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &neoPiDiscovery{
				provider: "deepinfra", ttl: 2 * time.Hour, retry: 5 * time.Minute,
				bundled: func(*coreauth.Auth) []*ModelInfo { return nil },
				fetch: func(context.Context, *config.Config, *coreauth.Auth) ([]*ModelInfo, error) {
					if tc.result != nil {
						return nil, tc.result
					}
					return kept, nil
				},
				entries: map[string]*neoPiDiscoveryEntry{},
			}
			account := &coreauth.Auth{ID: "deepinfra-account"}
			(&Service{}).queueNeoPiDiscovery(d, account)
			var fetchedAt time.Time
			for range 10000 {
				d.mu.Lock()
				entry := d.entries[account.ID]
				done := entry != nil && !entry.inFlight
				if done {
					fetchedAt = entry.fetchedAt
				}
				d.mu.Unlock()
				if done {
					break
				}
				runtime.Gosched()
			}
			if fetchedAt.IsZero() {
				t.Fatal("the listing was not stored")
			}
			models, stale := d.models(account, fetchedAt.Add(6*time.Minute))
			if len(models) != 1 || models[0].ID != kept[0].ID {
				t.Fatalf("roster = %v, want the kept image row", models)
			}
			if stale != tc.wantStale {
				t.Errorf("stale after 6m = %v, want %v", stale, tc.wantStale)
			}
		})
	}
}

// Factory Droid's listing belongs to the account scope it was fetched for
// (NeoPi's model-cache namespace holds org, residency and inference region):
// once a refresh moves the same account to another scope, the cached rows
// stop serving and a fetch for the new scope is due.
func TestFactoryDroidDiscoveryFollowsAccountScope(t *testing.T) {
	account := func(region string) *coreauth.Auth {
		return &coreauth.Auth{ID: "factory-scope", Provider: "factory-droid", Metadata: map[string]any{
			"type": "factory-droid", "access_token": "token", "org_id": "org-1", "region": region}}
	}
	d := factoryDroidDiscovery
	t.Cleanup(func() { forgetNeoPiDiscovery("factory-scope") })
	now := time.Now()
	global := []*ModelInfo{{ID: "global-only", NeoPi: &registry.NeoPiModel{ID: "global-only"}}}
	d.mu.Lock()
	d.entries["factory-scope"] = &neoPiDiscoveryEntry{models: global, fetchedAt: now, scope: d.scopeOf(account("global"))}
	d.mu.Unlock()
	if models, stale := d.models(account("global"), now); stale || len(models) != 1 || models[0].ID != "global-only" {
		t.Fatalf("same scope = (%v, stale %v), want the fresh cached listing", models, stale)
	}
	models, stale := d.models(account("eu"), now)
	if !stale {
		t.Error("a listing from another scope is not due for refetch")
	}
	for _, model := range models {
		if model.ID == "global-only" {
			t.Errorf("EU account served the global listing: %v", models)
		}
	}
}

// The account a listing belongs to is the credential's own identity, as in
// NeoPi's model-cache namespace: the JWT's external organization and user,
// else the opaque bearer. A record replaced by another user's sign-in under
// the same file and organization does not inherit the previous roster.
func TestFactoryDroidDiscoveryFollowsCredentialIdentity(t *testing.T) {
	jwt := func(sub string) string {
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"external_org_id":"org-ext","sub":"` + sub + `"}`))
		return "eyJhbGciOiJub25lIn0." + payload + ".sig"
	}
	for _, tc := range []struct{ name, first, second string }{
		{"jwt users", jwt("user-a"), jwt("user-b")},
		{"opaque bearers", "opaque-a", "opaque-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := func(token string) *coreauth.Auth {
				return &coreauth.Auth{ID: "factory-identity", Provider: "factory-droid", Metadata: map[string]any{
					"type": "factory-droid", "access_token": token, "org_id": "org-1", "region": "global"}}
			}
			d := factoryDroidDiscovery
			t.Cleanup(func() { forgetNeoPiDiscovery("factory-identity") })
			now := time.Now()
			d.mu.Lock()
			d.entries["factory-identity"] = &neoPiDiscoveryEntry{models: []*ModelInfo{{ID: "first-user-only"}}, fetchedAt: now, scope: d.scopeOf(account(tc.first))}
			d.mu.Unlock()
			models, stale := d.models(account(tc.second), now)
			if !stale {
				t.Error("another user's listing is not due for refetch")
			}
			for _, model := range models {
				if model.ID == "first-user-only" {
					t.Errorf("second user served the first user's listing: %v", models)
				}
			}
		})
	}
}
