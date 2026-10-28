package neo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPassword = "correct-horse-battery"

func newTestStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errClose := s.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, &now
}
func testUser(t *testing.T, s *Store, handle, role string) User {
	t.Helper()
	u, err := s.CreateUser(t.Context(), UserInput{Handle: handle, Name: handle, Role: role, State: "active", Password: testPassword})
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func testInput(u User) UserInput {
	return UserInput{Handle: u.Handle, Name: u.Name, Role: u.Role, State: u.State, ProviderAccess: u.ProviderAccess, Grants: u.Grants, Limits: u.Limits}
}
func testActor(u User) Principal { p := principal(u); p.Session = true; return p }

func TestPersistenceReopenAndPrivateHashes(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "data", "identity.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	session, token, err := s.Bootstrap(ctx, "captain", "Captain", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.User(ctx, session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := s.MintKey(ctx, testActor(u), u.ID, "client", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var passwordHash, sessionHash, keyHash string
	if err = s.DB().QueryRowContext(ctx, "SELECT password_hash FROM neo_users WHERE id=?", u.ID).Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if err = s.DB().QueryRowContext(ctx, "SELECT token_hash FROM neo_sessions WHERE user_id=?", u.ID).Scan(&sessionHash); err != nil {
		t.Fatal(err)
	}
	if err = s.DB().QueryRowContext(ctx, "SELECT token_hash FROM neo_keys WHERE user_id=?", u.ID).Scan(&keyHash); err != nil {
		t.Fatal(err)
	}
	if passwordHash == testPassword || sessionHash == token || keyHash == secret || !strings.HasPrefix(passwordHash, "$2") {
		t.Fatal("plaintext credential persisted")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("database mode %o", fi.Mode().Perm())
	}
	fi, err = os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0700 {
		t.Fatalf("directory mode %o", fi.Mode().Perm())
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err = s.Session(ctx, token); err != nil {
		t.Fatal("session did not survive reopen:", err)
	}
	if p, errAuth := s.AuthenticateKey(ctx, secret); errAuth != nil || p.UserID != u.ID || p.Session {
		t.Fatalf("key restore: %#v %v", p, errAuth)
	}
	restored, err := s.User(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(restored)
	for _, sensitive := range []string{testPassword, token, secret, passwordHash, keyHash} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatal("sensitive value returned in user JSON")
		}
	}
	if _, err = s.DB().ExecContext(ctx, "INSERT INTO neo_grants(user_id,virtual_model,allotment) VALUES('missing','vm',1)"); err == nil {
		t.Fatal("foreign keys disabled")
	}
}
func TestSessionAndPasswordLifecycle(t *testing.T) {
	s, now := newTestStore(t)
	u := testUser(t, s, "member", RoleMember)
	ctx := t.Context()
	if _, _, err := s.Login(ctx, u.Handle, "incorrect-password"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, _, err := s.Login(ctx, "unknown", testPassword); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	session, token, err := s.Login(ctx, u.Handle, testPassword)
	if err != nil || session.Role != "user" {
		t.Fatalf("login %#v %v", session, err)
	}
	if _, p, errSession := s.Session(ctx, token); errSession != nil || !p.Session || p.IsAdmin() {
		t.Fatalf("principal %#v %v", p, errSession)
	}
	*now = now.Add(SessionLifetime)
	if _, _, err = s.Session(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expired session accepted", err)
	}
	_, token, err = s.Login(ctx, u.Handle, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Session(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("logout not immediate", err)
	}
	_, token, err = s.Login(ctx, u.Handle, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := s.MintKey(ctx, testActor(u), u.ID, "client", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, u.ID, "incorrect-password", "replacement-password", false); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal(err)
	}
	if err = s.ChangePassword(ctx, u.ID, testPassword, "replacement-password", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Session(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("password retained session", err)
	}
	if _, err = s.AuthenticateKey(ctx, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("password retained key", err)
	}
	if _, _, err = s.Login(ctx, u.Handle, testPassword); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old password accepted", err)
	}
	if _, _, err = s.Login(ctx, u.Handle, "replacement-password"); err != nil {
		t.Fatal(err)
	}
}
func TestCurrentGrantsLimitsSuspensionAndRevocation(t *testing.T) {
	s, now := newTestStore(t)
	ctx := t.Context()
	u := testUser(t, s, "member", RoleMember)
	_, token, err := s.Login(ctx, u.Handle, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	key, secret, err := s.MintKey(ctx, testActor(u), u.ID, "client", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := testInput(u)
	in.Grants = []Grant{{VirtualModel: "vm-current", Allotment: .5}}
	in.ProviderAccess = []string{"codex"}
	rpm := int64(4)
	in.Limits.RPM = &rpm
	u, err = s.UpdateUser(ctx, u.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.AuthenticateKey(ctx, secret)
	if err != nil || p.Session || len(p.Grants) != 1 || p.Grants[0].VirtualModel != "vm-current" || *p.Limits.RPM != 4 {
		t.Fatalf("stale authority %#v %v", p, err)
	}
	in.Grants = []Grant{}
	in.ProviderAccess = nil
	in.State = "suspended"
	if _, err = s.UpdateUser(ctx, u.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RefreshPrincipal(ctx, p); !errors.Is(err, ErrSuspended) {
		t.Fatal("queued principal ignored suspension", err)
	}
	if _, _, err = s.Session(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("suspension retained session", err)
	}
	suspended, err := s.User(ctx, u.ID)
	if err != nil || len(suspended.Keys) != 1 {
		t.Fatal("suspension must retain key metadata", err)
	}
	in.State = "active"
	if _, err = s.UpdateUser(ctx, u.ID, in); err != nil {
		t.Fatal(err)
	}
	p, err = s.RefreshPrincipal(ctx, p)
	if err != nil || len(p.Grants) != 0 || len(p.ProviderAccess) != 0 {
		t.Fatal("removed grants survived", p, err)
	}
	if err = s.RevokeKey(ctx, testActor(u), u.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RefreshPrincipal(ctx, p); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("revoked queued principal accepted", err)
	}
	expiry := now.Add(time.Minute).UnixMilli()
	_, secret, err = s.MintKey(ctx, testActor(u), u.ID, "expires", nil, &expiry)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if _, err = s.AuthenticateKey(ctx, secret); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("expired key accepted", err)
	}
}
func TestOwnerKeyScopeUsesCurrentSharedDatabaseOutsideTransaction(t *testing.T) {
	s, _ := newTestStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	owner := testUser(t, s, "owner", RoleOwner)
	other := testUser(t, s, "other", RoleOwner)
	if _, err := s.DB().ExecContext(ctx, "CREATE TABLE test_ownership(id TEXT PRIMARY KEY,owner TEXT); INSERT INTO test_ownership VALUES('account-a',?),('account-b',?)", owner.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	s.SetAccountScopeValidator(func(ctx context.Context, userID string, ids []string) error {
		for _, id := range ids {
			var actual string
			if err := s.DB().QueryRowContext(ctx, "SELECT owner FROM test_ownership WHERE id=?", id).Scan(&actual); err != nil {
				return err
			}
			if actual != userID {
				return ErrForbidden
			}
		}
		return nil
	})
	actor := testActor(owner)
	if _, _, err := s.MintKey(ctx, actor, owner.ID, "all", nil, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("unscoped owner key accepted", err)
	}
	if _, _, err := s.MintKey(ctx, actor, owner.ID, "foreign", []string{"account-b"}, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("foreign owner pool accepted", err)
	}
	_, secret, err := s.MintKey(ctx, actor, owner.ID, "own", []string{"account-a"}, nil)
	if err != nil {
		t.Fatal("validator deadlocked or rejected own account:", err)
	}
	p, err := s.AuthenticateKey(ctx, secret)
	if err != nil || !p.AllowsAccount("account-a") || p.AllowsAccount("account-b") {
		t.Fatal(p, err)
	}
	if _, err = s.DB().ExecContext(ctx, "UPDATE test_ownership SET owner=? WHERE id='account-a'", other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RefreshPrincipal(ctx, p); !errors.Is(err, ErrForbidden) {
		t.Fatal("ownership reassignment retained key access", err)
	}
	if _, _, err = s.MintKey(ctx, actor, other.ID, "foreign-user", []string{"account-b"}, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("owner minted for another user", err)
	}
}
func TestBootstrapIsPermanentAndLastAdminProtected(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	session, _, err := s.Bootstrap(ctx, "captain", "Captain", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.User(ctx, session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteUser(ctx, u.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("last admin deleted", err)
	}
	in := testInput(u)
	in.Role = RoleOwner
	if _, err = s.UpdateUser(ctx, u.ID, in); !errors.Is(err, ErrForbidden) {
		t.Fatal("last admin demoted", err)
	}
	_ = testUser(t, s, "second-admin", RoleAdmin)
	if err = s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Bootstrap(ctx, "new-admin", "New", testPassword); !errors.Is(err, ErrConflict) {
		t.Fatal("bootstrap reopened", err)
	}
}
func TestInvitedServiceAndMissingPasswords(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	invited, err := s.CreateUser(ctx, UserInput{Handle: "invited", Role: RoleMember, State: "invited"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Login(ctx, invited.Handle, "password"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("missing verifier authenticated", err)
	}
	admin := testUser(t, s, "admin", RoleAdmin)
	_, secret, err := s.MintKey(ctx, testActor(admin), invited.ID, "invitation", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, errAuth := s.AuthenticateKey(ctx, secret); errAuth != nil || p.UserID != invited.ID {
		t.Fatal("invited activation failed", errAuth)
	}
	service := testUser(t, s, "service", RoleService)
	if _, _, err = s.Login(ctx, service.Handle, testPassword); !errors.Is(err, ErrForbidden) {
		t.Fatal("service acquired dashboard", err)
	}
	if _, _, err = s.MintKey(ctx, Principal{UserID: admin.ID, Role: RoleAdmin, KeyID: "inference"}, service.ID, "authority", nil, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("admin inference principal minted a key", err)
	}
}
