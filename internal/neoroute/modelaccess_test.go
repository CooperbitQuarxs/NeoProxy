package neoroute

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// withDirectModels adds a primary account serving three more models.
func withDirectModels(t *testing.T, f *routeFixture) {
	t.Helper()
	ctx := context.Background()
	id := "route-test-" + uuid.NewString() + "c"
	if _, err := f.manager.Register(ctx, &coreauth.Auth{ID: id, Provider: "primary", Status: coreauth.StatusActive}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "primary", []*registry.ModelInfo{{ID: "gpt-5", Object: "model"}, {ID: "gpt-5-mini", Object: "model"}, {ID: "o3", Object: "model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, err := f.pool.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.AssignOwner(ctx, neopool.AccountID(id), f.admin.UserID); err != nil {
		t.Fatal(err)
	}
}

func directList(t *testing.T, f *routeFixture, ctx context.Context) map[string]bool {
	t.Helper()
	models, err := f.route.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]bool)
	for _, m := range models {
		if m != "neoproxy/route" {
			out[m] = true
		}
	}
	return out
}

func set(names ...string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

func TestModelAccessNarrowsListingAndResolution(t *testing.T) {
	f := newRouteFixture(t, "fill")
	withDirectModels(t, f)
	ctx := context.Background()
	in := neo.UserInput{Handle: f.user.Handle, Role: "service", State: "active", ProviderAccess: []string{"primary"}, Grants: []neo.Grant{{VirtualModel: f.vm.ID, Allotment: 1}}}
	if _, err := f.identity.UpdateUser(ctx, f.user.ID, in); err != nil {
		t.Fatal(err)
	}
	all := set("neoproxy/primary/model", "neoproxy/primary/gpt-5", "neoproxy/primary/gpt-5-mini", "neoproxy/primary/o3")
	if got := directList(t, f, f.ctx); !reflect.DeepEqual(got, all) {
		t.Fatalf("no allowlist must list every exposed model: %v", got)
	}

	in.ModelAccess = neo.ModelAccess{"primary": {"gpt-5*", "o3"}}
	if _, err := f.identity.UpdateUser(ctx, f.user.ID, in); err != nil {
		t.Fatal(err)
	}
	want := set("neoproxy/primary/gpt-5", "neoproxy/primary/gpt-5-mini", "neoproxy/primary/o3")
	if got := directList(t, f, f.ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("allowlisted listing %v want %v", got, want)
	}
	for asked, reason := range map[string]string{
		"neoproxy/primary/gpt-5-mini": "",
		"neoproxy/primary/o3":         "",
		"neoproxy/primary/model":      "no-model-access",
	} {
		d, err := f.route.Simulate(f.ctx, Request{Asked: asked, Protocol: "openai"})
		if err != nil {
			t.Fatal(err)
		}
		if d.Reason != reason || (reason == "" && (d.Outcome != "ok" || !d.Direct)) {
			t.Fatalf("%s: %+v", asked, d)
		}
	}
	// The allowlist narrows direct calls only; the virtual model still routes
	// to primary/model through its stage.
	if d, err := f.route.Simulate(f.ctx, boundedRequest(false)); err != nil || d.Outcome != "ok" || d.Model != "primary/model" {
		t.Fatalf("virtual model narrowed by direct allowlist: %+v %v", d, err)
	}

	_, secret, err := f.identity.Mint(ctx, f.admin, f.user.ID, neo.KeyRequest{Label: "only-o3", Models: []string{"primary/o3"}})
	if err != nil {
		t.Fatal(err)
	}
	keyed, err := f.identity.AuthenticateKey(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	keyCtx := neo.WithPrincipal(ctx, keyed)
	if got := directList(t, f, keyCtx); !reflect.DeepEqual(got, set("neoproxy/primary/o3")) {
		t.Fatalf("key-narrowed listing %v", got)
	}
	if d, err := f.route.Simulate(keyCtx, Request{Asked: "neoproxy/primary/gpt-5", Protocol: "openai"}); err != nil || d.Reason != "no-model-access" {
		t.Fatalf("key allowed a model outside its list: %+v %v", d, err)
	}
	if d, err := f.route.Simulate(keyCtx, Request{Asked: "neoproxy/primary/o3", Protocol: "openai"}); err != nil || d.Outcome != "ok" {
		t.Fatalf("key denied its own model: %+v %v", d, err)
	}

	// Clearing the user allowlist cannot widen the key past its own list.
	in.ModelAccess = neo.ModelAccess{}
	if _, err = f.identity.UpdateUser(ctx, f.user.ID, in); err != nil {
		t.Fatal(err)
	}
	if got := directList(t, f, f.ctx); !reflect.DeepEqual(got, all) {
		t.Fatalf("cleared allowlist listing %v", got)
	}
	if got := directList(t, f, keyCtx); !reflect.DeepEqual(got, set("neoproxy/primary/o3")) {
		t.Fatalf("key widened after user allowlist cleared %v", got)
	}
}
