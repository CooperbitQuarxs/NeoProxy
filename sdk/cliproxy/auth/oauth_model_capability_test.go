package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// A NeoPi-ported OAuth account executes with the record its discovery
// registered, including models NeoPi's bundled seed does not carry, and with
// the seed until discovery answers.
func TestOAuthCapabilityUsesDiscoveredRoster(t *testing.T) {
	for _, provider := range []string{"devin", "kimi"} {
		t.Run(provider, func(t *testing.T) {
			auth := &Auth{ID: "roster-" + provider, Provider: provider, Metadata: map[string]any{"auth_kind": "oauth"}}
			discovered := &registry.ModelInfo{ID: "discovered-only", NeoPi: &registry.NeoPiModel{ID: "discovered-only", Provider: provider}}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, provider, []*registry.ModelInfo{discovered})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			if info, ok := lookupOAuthModelCapability(auth, "discovered-only"); !ok || info == nil || info.NeoPi == nil || info.NeoPi.ID != "discovered-only" {
				t.Fatalf("discovered model capability = (%+v, %t), want the registered record", info, ok)
			}

			reg.UnregisterClient(auth.ID)
			seed := registry.DevinBundledModels()
			if provider == "kimi" {
				seed = registry.KimiBundledModels()
			}
			if info, ok := lookupOAuthModelCapability(auth, seed[0].ID); !ok || info == nil || info.NeoPi == nil {
				t.Fatalf("seed model before discovery = (%+v, %t), want NeoPi's bundled record", info, ok)
			}
		})
	}
}
