package management

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type ownershipGuardStore struct{ saved bool }

func (s *ownershipGuardStore) Save(context.Context, *coreauth.Auth) (string, error) {
	s.saved = true
	return "synthetic-auth.json", nil
}
func (s *ownershipGuardStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }
func (s *ownershipGuardStore) Delete(context.Context, string) error           { return nil }

func TestAccountOwnershipGuardPrecedesCredentialPersistence(t *testing.T) {
	store := &ownershipGuardStore{}
	denied := errors.New("owned by another user")
	h := &Handler{cfg: &config.Config{AuthDir: t.TempDir()}, tokenStore: store, accountOwnershipHook: func(ctx context.Context, _ *coreauth.Auth) error {
		if coreauth.AccountLoginOwner(ctx) != "owner-a" {
			t.Error("lost authenticated initiator")
		}
		return denied
	}}
	source := neo.WithPrincipal(context.Background(), neo.Principal{UserID: "owner-a", Role: neo.RoleOwner, Session: true})
	ctx := inheritAccountLoginOwner(context.Background(), source)
	if _, err := h.saveTokenRecord(ctx, &coreauth.Auth{ID: "existing.json", Provider: "codex"}); !errors.Is(err, denied) {
		t.Fatalf("guard denial lost: %v", err)
	}
	if store.saved {
		t.Fatal("another owner's credential was overwritten before checking ownership")
	}
}
func TestPluginCompletionKeepsOriginalOwnerNotPollingAdmin(t *testing.T) {
	const state = "neo-owner-plugin-attribution"
	if err := RegisterPluginOAuthSession(state, "test-provider", nil); err != nil {
		t.Fatal(err)
	}
	defer CompleteOAuthSession(state)
	owner := inheritAccountLoginOwner(context.Background(), neo.WithPrincipal(context.Background(), neo.Principal{UserID: "owner-a", Role: neo.RoleOwner, Session: true}))
	bindPluginLoginOwner(owner, state)
	poller := inheritAccountLoginOwner(context.Background(), neo.WithPrincipal(context.Background(), neo.Principal{UserID: "admin-b", Role: neo.RoleAdmin, Session: true}))
	if got := coreauth.AccountLoginOwner(oauthInitiatingOwner(poller, state)); got != "owner-a" {
		t.Fatalf("completion attributed to poller: %q", got)
	}
	keyOnly := inheritAccountLoginOwner(context.Background(), neo.WithPrincipal(context.Background(), neo.Principal{UserID: "owner-a", Role: neo.RoleOwner, Session: false}))
	if coreauth.AccountLoginOwner(keyOnly) != "" {
		t.Fatal("inference key obtained login ownership authority")
	}
}
