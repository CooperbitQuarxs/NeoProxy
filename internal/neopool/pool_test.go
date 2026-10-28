package neopool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type fixture struct {
	pool     *Store
	identity *neo.Store
	manager  *coreauth.Manager
	accounts []Account
	at       time.Time
	path     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "neo.sqlite")
	identity, err := neo.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if errClose := identity.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	for _, row := range []struct{ id, role string }{{"admin", "admin"}, {"owner-a", "owner"}, {"owner-b", "owner"}, {"user-a", "member"}, {"user-b", "member"}} {
		if _, err = identity.DB().Exec(`INSERT INTO neo_users(id,handle,name,role,state,created_at) VALUES(?,?,?,?,?,0)`, row.id, row.id, row.id, row.role, "active"); err != nil {
			t.Fatal(err)
		}
	}
	manager := coreauth.NewManager(nil, nil, nil)
	for i := 0; i < 2; i++ {
		authID := fmt.Sprintf("%s-%d-full-private@example.com.json", t.Name(), i)
		a := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "private-test-token"}}
		if _, err = manager.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "pool-test-model", ContextLength: 128000, DisplayName: "Pool Test Model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}
	pool, err := New(identity.DB(), manager)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{pool: pool, identity: identity, manager: manager, path: path, at: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
	pool.now = func() time.Time { return f.at }
	identity.SetAccountScopeValidator(pool.ScopeOwned)
	snap, err := pool.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.accounts = snap.Accounts
	return f
}
func (f *fixture) assign(t *testing.T, index int, owner string) {
	t.Helper()
	if err := f.pool.AssignOwner(context.Background(), f.accounts[index].ID, owner); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) meter(t *testing.T, index int, kind string, capacity int64) {
	t.Helper()
	if err := f.pool.ConfigureWindow(context.Background(), f.accounts[index].ID, "admin", kind, capacity, f.at.UnixMilli(), true); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) reservation(id string, index int, tokens int64) Reservation {
	return Reservation{ID: id, RequestID: "request-" + id, UserID: "user-a", VirtualModelID: "vm-test", StageID: "stage-one", AccountID: f.accounts[index].ID, AccountIDs: []string{f.accounts[0].ID, f.accounts[1].ID}, Window: "day", Tokens: tokens, PerAccount: 1, Cap: 1}
}

func TestInventoryUsesRealEngineAndUnknownQuota(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	snap, err := f.pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Accounts) != 2 || len(snap.Models) != 1 || snap.Models[0].Context != 128000 {
		t.Fatalf("real inventory: %+v", snap)
	}
	for _, a := range snap.Accounts {
		if a.OwnerID != "" || len(a.Windows) != 0 || a.State != "healthy" || a.EngineAuthID == "" {
			t.Fatalf("unknown/unowned state was fabricated: %+v", a)
		}
	}
	encoded, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private@example") || strings.Contains(string(encoded), "private-test-token") {
		t.Fatal("inventory leaked credential material")
	}
	f.assign(t, 0, "owner-a")
	f.meter(t, 0, "day", 100)
	if err = f.pool.SetExposure(ctx, "codex", false, true); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.SetExposure(ctx, "codex/pool-test-model", false, false); err != nil {
		t.Fatal(err)
	}
	other, err := New(f.identity.DB(), f.manager)
	if err != nil {
		t.Fatal(err)
	}
	other.now = f.pool.now
	again, err := other.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Providers[0].Exposed || again.Models[0].Exposed {
		t.Fatal("exposure was not persisted")
	}
	var metered Account
	for _, a := range again.Accounts {
		if a.ID == f.accounts[0].ID {
			metered = a
		}
	}
	if metered.OwnerID != "owner-a" || len(metered.Windows) != 1 || !metered.Windows[0].Known || metered.Windows[0].Source != "configured-metered" || metered.Windows[0].Unit != "tokens" {
		t.Fatalf("meter did not describe its source: %+v", metered)
	}
	a, _ := f.manager.GetByID(f.accounts[0].EngineAuthID)
	a.Disabled = true
	if _, err = f.manager.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	again, err = f.pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range again.Accounts {
		if account.ID == f.accounts[0].ID && account.State != "disabled" {
			t.Fatal("engine health ignored")
		}
	}
}

func TestOwnershipAndCurrentScopedKeys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.assign(t, 0, "owner-a")
	f.assign(t, 1, "owner-b")
	actor := neo.Principal{UserID: "owner-a", Role: neo.RoleOwner, Session: true}
	if _, _, err := f.identity.MintKey(ctx, actor, "owner-a", "unscoped", nil, nil); err == nil {
		t.Fatal("owner minted an unscoped key")
	}
	if _, _, err := f.identity.MintKey(ctx, actor, "owner-a", "foreign", []string{f.accounts[1].ID}, nil); err == nil {
		t.Fatal("owner minted another owner's pool")
	}
	_, secret, err := f.identity.MintKey(ctx, actor, "owner-a", "owned", []string{f.accounts[0].ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.identity.AuthenticateKey(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.AccountIDs) != 1 || p.AccountIDs[0] != f.accounts[0].ID {
		t.Fatal("key scope lost")
	}
	if err = f.pool.ConfigureWindow(ctx, f.accounts[1].ID, "owner-a", "day", 100, f.at.UnixMilli(), false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("foreign configuration: %v", err)
	}
	a, _ := f.manager.GetByID(f.accounts[1].EngineAuthID)
	if err = f.pool.BindLogin(ctx, "owner-a", a); !errors.Is(err, ErrForbidden) {
		t.Fatalf("login replaced another owner: %v", err)
	}
	f.assign(t, 0, "owner-b")
	if _, err = f.identity.AuthenticateKey(ctx, secret); err == nil {
		t.Fatal("transferred account retained old owner key authority")
	}
	f.meter(t, 0, "day", 100)
	r := f.reservation("transfer-race", 0, 10)
	r.ScopeOwnerID = "owner-a"
	if err = f.pool.Reserve(ctx, r); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stale ownership at reservation: %v", err)
	}
}

func TestConcurrentBudgetReservationAndIdempotentSettlement(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.assign(t, 0, "owner-a")
	f.assign(t, 1, "owner-b")
	f.meter(t, 0, "day", 100)
	f.meter(t, 1, "day", 100)
	var admitted atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			r := f.reservation(fmt.Sprintf("concurrent-%d", i), 0, 20)
			r.Cap = .5
			r.PerAccount = .5
			err := f.pool.Reserve(ctx, r)
			if err == nil {
				admitted.Add(1)
			} else if !errors.Is(err, ErrBudget) {
				t.Errorf("reserve: %v", err)
			}
		}(i)
	}
	group.Wait()
	if admitted.Load() != 2 {
		t.Fatalf("admitted=%d, want two bounds within 50 tokens", admitted.Load())
	}
	global := f.reservation("other-stage", 0, 70)
	global.StageID = "another-stage"
	if err := f.pool.Reserve(ctx, global); !errors.Is(err, ErrBudget) {
		t.Fatalf("global capacity overspent: %v", err)
	}
	// A different pool member is not debited by an excluded historic member.
	alone := f.reservation("excluded-pool", 1, 20)
	alone.AccountIDs = []string{f.accounts[1].ID}
	alone.Cap = .2
	if err := f.pool.Reserve(ctx, alone); err != nil {
		t.Fatalf("removed pool member debited cap: %v", err)
	}
	bad := Settlement{ID: alone.ID, AccountID: f.accounts[0].ID, InputTokens: 7, OutputTokens: 3, UsageKnown: true}
	if err := f.pool.Settle(ctx, bad); !errors.Is(err, ErrForbidden) {
		t.Fatalf("actual account mismatch: %v", err)
	}
	good := Settlement{ID: alone.ID, AccountID: alone.AccountID, InputTokens: 7, OutputTokens: 3, CachedTokens: 2, UsageKnown: true}
	if err := f.pool.Settle(ctx, good); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.Settle(ctx, good); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.Reserve(ctx, alone); err != nil {
		t.Fatalf("same attempt not idempotent: %v", err)
	}
	different := alone
	different.Tokens++
	if err := f.pool.Reserve(ctx, different); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting attempt accepted: %v", err)
	}
	good.OutputTokens++
	if err := f.pool.Settle(ctx, good); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting settlement accepted: %v", err)
	}
	stats, err := f.pool.Stats(ctx, "owner-b", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].InputTokens != 7 || stats[0].OutputTokens != 3 || stats[0].CachedTokens != 2 || stats[0].Attempts != 1 {
		t.Fatalf("double-charged or leaked stats: %+v", stats)
	}
}

func TestCursorAndReservationCommitTogether(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.assign(t, 0, "owner-a")
	f.meter(t, 0, "day", 100)
	if _, err := f.identity.DB().Exec(`CREATE TABLE test_cursor(value INTEGER NOT NULL);INSERT INTO test_cursor VALUES(0)`); err != nil {
		t.Fatal(err)
	}
	r := f.reservation("cursor", 0, 20)
	sentinel := errors.New("cursor CAS failed")
	if err := f.pool.ReserveAndAdvance(ctx, r, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE test_cursor SET value=value+1`); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var attempts, cursor int
	if err := f.identity.DB().QueryRow(`SELECT count(*) FROM neo_pool_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatal("failed cursor committed a reservation")
	}
	advance := func(tx *sql.Tx) error { _, err := tx.Exec(`UPDATE test_cursor SET value=value+1`); return err }
	if err := f.pool.ReserveAndAdvance(ctx, r, advance); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.ReserveAndAdvance(ctx, r, advance); err != nil {
		t.Fatal(err)
	}
	if err := f.identity.DB().QueryRow(`SELECT value FROM test_cursor`).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 1 {
		t.Fatalf("idempotent attempt advanced cursor %d times", cursor)
	}
}

func TestResetsRetriesCancellationAndHistoricalOwner(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.assign(t, 0, "owner-a")
	f.meter(t, 0, "day", 100)
	f.meter(t, 0, "5h", 100)
	r := f.reservation("attempt-one", 0, 40)
	r.RequestID = "one-request"
	if err := f.pool.Reserve(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Cancellation after dispatch without a usage report is not free work.
	if err := f.pool.Settle(ctx, Settlement{ID: r.ID, AccountID: r.AccountID}); err != nil {
		t.Fatal(err)
	}
	retry := r
	retry.ID = "attempt-two"
	if err := f.pool.Reserve(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.Settle(ctx, Settlement{ID: retry.ID, AccountID: r.AccountID, InputTokens: 8, OutputTokens: 2, UsageKnown: true}); err != nil {
		t.Fatal(err)
	}
	undispatched := f.reservation("undispatched", 0, 20)
	if err := f.pool.Reserve(ctx, undispatched); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.Release(ctx, undispatched.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := f.pool.Usage(ctx, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Tokens != 50 || rows[0].Reserved != 0 || rows[0].Fraction != .5 {
		t.Fatalf("retry/cancel accounting: %+v", rows)
	}
	if err = f.pool.ConfigureWindow(ctx, r.AccountID, "owner-a", "day", 1000, f.at.UnixMilli(), false); !errors.Is(err, ErrConflict) {
		t.Fatalf("active meter reset allowed: %v", err)
	}
	f.assign(t, 0, "owner-b")
	old, err := f.pool.Stats(ctx, "owner-a", false)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.pool.Stats(ctx, "owner-b", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 || old[0].EstimatedTokens != 40 || old[0].InputTokens != 8 || old[0].UnknownAttempts != 1 {
		t.Fatalf("old owner attribution lost: %+v", old)
	}
	for _, s := range current {
		if s.InputTokens != 0 || s.EstimatedTokens != 0 {
			t.Fatalf("transfer disclosed another owner's history: %+v", current)
		}
	}
	f.at = f.at.Add(24 * time.Hour)
	rows, err = f.pool.Usage(ctx, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("expired epoch counted: %+v", rows)
	}
	fresh := f.reservation("fresh-epoch", 0, 100)
	if err = f.pool.Reserve(ctx, fresh); err != nil {
		t.Fatalf("reset did not restore capacity: %v", err)
	}
}

func TestUnknownMeterAndCalendarBoundaries(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.assign(t, 0, "owner-a")
	if err := f.pool.Reserve(ctx, f.reservation("unknown", 0, 1)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invented a quota denominator: %v", err)
	}
	direct := f.reservation("direct", 0, 0)
	direct.BudgetFree = true
	direct.Window = ""
	if err := f.pool.Reserve(ctx, direct); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.Settle(ctx, Settlement{ID: direct.ID, AccountID: direct.AccountID, InputTokens: 5, OutputTokens: 2, UsageKnown: true}); err != nil {
		t.Fatal(err)
	}
	anchor := time.Date(2026, 1, 31, 12, 30, 0, 0, time.UTC)
	at := time.Date(2026, 2, 28, 12, 30, 0, 0, time.UTC)
	start, reset, err := windowBounds("month", anchor.UnixMilli(), at.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	if start != at.UnixMilli() || reset != time.Date(2026, 3, 31, 12, 30, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("calendar drift: %v -> %v", time.UnixMilli(start), time.UnixMilli(reset))
	}
}
