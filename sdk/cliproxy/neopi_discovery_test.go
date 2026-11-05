package cliproxy

import (
	"context"
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
