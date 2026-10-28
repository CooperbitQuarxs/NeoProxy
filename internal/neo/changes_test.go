package neo

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityChangesBroadcastOnlyCommittedMutations(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	before := s.Changes()
	u := testUser(t, s, "member", RoleMember)
	select {
	case <-before:
	default:
		t.Fatal("create did not wake observers")
	}
	before = s.Changes()
	if _, err := s.User(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-before:
		t.Fatal("read woke mutation observers")
	default:
	}
	invalid := testInput(u)
	invalid.Role = "invalid"
	if _, err := s.UpdateUser(ctx, u.ID, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	select {
	case <-before:
		t.Fatal("rejected update woke observers")
	default:
	}
	valid := testInput(u)
	valid.Grants = []Grant{{VirtualModel: "vm", Allotment: .5}}
	if _, err := s.UpdateUser(ctx, u.ID, valid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-before:
	default:
		t.Fatal("grant update did not wake queued requests")
	}
	before = s.Changes()
	key, _, err := s.MintKey(ctx, testActor(u), u.ID, "client", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-before:
	default:
		t.Fatal("key mint did not wake observers")
	}
	before = s.Changes()
	if err = s.RevokeKey(ctx, testActor(u), u.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-before:
	default:
		t.Fatal("key revocation did not wake queued requests")
	}
}
func TestUserValidationAndPrivateDatabaseInput(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := t.Context()
	base := UserInput{Handle: "member", Role: RoleMember, State: "active", Password: testPassword}
	for _, change := range []func(*UserInput){
		func(in *UserInput) { in.Handle = "Admin" },
		func(in *UserInput) { in.Password = "short" },
		func(in *UserInput) { in.Role = "operator" },
		func(in *UserInput) { in.Grants = []Grant{{VirtualModel: "vm", Allotment: math.NaN()}} },
		func(in *UserInput) { in.Grants = []Grant{{VirtualModel: "vm", Allotment: math.Inf(1)}} },
		func(in *UserInput) { in.Grants = []Grant{{VirtualModel: "vm", Allotment: 1.1}} },
		func(in *UserInput) {
			in.Grants = []Grant{{VirtualModel: "vm", Allotment: .5}, {VirtualModel: "vm", Allotment: .2}}
		},
	} {
		in := base
		change(&in)
		if _, err := s.CreateUser(ctx, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid user accepted: %#v %v", in, err)
		}
	}
	if _, err := Open(""); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty database path accepted", err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("not sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(link); err == nil {
		_ = db.Close()
		t.Fatal("database symlink accepted")
	}
	if _, err := s.DB().ExecContext(ctx, "PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	// Unknown future schemas must be refused rather than silently downgraded.
	if err := s.migrate(ctx); err == nil {
		t.Fatal("future schema accepted")
	}
}
