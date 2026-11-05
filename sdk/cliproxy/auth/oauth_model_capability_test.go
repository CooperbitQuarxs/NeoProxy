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

// A Factory Droid account executes the row its discovery registered: the
// live upstream rotation and routing source NeoPi routes by. Before
// discovery answers the executor builds NeoPi's registry row itself.
func TestOAuthCapabilityUsesFactoryDroidDiscoveredRow(t *testing.T) {
	auth := &Auth{ID: "roster-factory-droid", Provider: "factory-droid", Metadata: map[string]any{"type": "factory-droid", "access_token": "token"}}
	discovered := &registry.ModelInfo{ID: "claude-opus-5-5", NeoPi: &registry.NeoPiModel{ID: "claude-opus-5-5", Provider: "factory-droid",
		FactoryDroid: &registry.NeoPiFactoryDroid{Wire: "anthropic-messages", APIProviders: []string{"bedrock", "anthropic"}, RoutingSource: "configured_order"}}}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "factory-droid", []*registry.ModelInfo{discovered})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	info, ok := lookupOAuthModelCapability(auth, "claude-opus-5-5(high)")
	if !ok || info == nil || info.NeoPi == nil || info.NeoPi.FactoryDroid == nil || info.NeoPi.FactoryDroid.RoutingSource != "configured_order" {
		t.Fatalf("discovered model capability = (%+v, %t), want the account's registered row", info, ok)
	}
	reg.UnregisterClient(auth.ID)
	if info, ok := lookupOAuthModelCapability(auth, "claude-opus-5-5"); ok {
		t.Fatalf("capability before discovery = %+v, want none so the executor builds the registry row", info)
	}
}

// An OAuth alias or a forced prefix renames the registered row while it
// keeps its canonical identity; the request, resolved to the upstream id,
// still executes the account's discovered row rather than a rebuilt one.
func TestOAuthCapabilityFindsAliasedDiscoveredRow(t *testing.T) {
	for _, registeredID := range []string{"opus", "work/claude-opus-5-5"} {
		t.Run(registeredID, func(t *testing.T) {
			auth := &Auth{ID: "roster-alias-" + registeredID, Provider: "factory-droid", Metadata: map[string]any{"type": "factory-droid", "access_token": "token"}}
			discovered := &registry.ModelInfo{ID: registeredID, MetadataModelID: "claude-opus-5-5", NeoPi: &registry.NeoPiModel{ID: "claude-opus-5-5", Provider: "factory-droid",
				FactoryDroid: &registry.NeoPiFactoryDroid{Wire: "anthropic-messages", APIProviders: []string{"bedrock"}, RoutingSource: "configured_order"}}}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, "factory-droid", []*registry.ModelInfo{discovered})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			info, ok := lookupOAuthModelCapability(auth, "claude-opus-5-5")
			if !ok || info == nil || info.NeoPi == nil || info.NeoPi.FactoryDroid == nil || info.NeoPi.FactoryDroid.RoutingSource != "configured_order" {
				t.Fatalf("capability = (%+v, %t), want the aliased discovered row", info, ok)
			}
		})
	}
}
