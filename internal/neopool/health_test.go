package neopool

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestElapsedEngineCooldownBecomesEligibleWithoutRefreshLoop(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.assign(t, 0, "owner-a")
	f.meter(t, 0, "day", 100)
	a, _ := f.manager.GetByID(f.accounts[0].EngineAuthID)
	a.Unavailable = true
	a.Status = coreauth.StatusError
	a.NextRetryAfter = f.at.Add(time.Minute)
	if _, err := f.manager.Update(ctx, a); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range snapshot.Accounts {
		if account.ID == f.accounts[0].ID && (account.State != "cooldown" || account.CooldownUntil != a.NextRetryAfter.UnixMilli()) {
			t.Fatalf("lost real cooldown: %+v", account)
		}
	}
	f.at = f.at.Add(2 * time.Minute)
	snapshot, err = f.pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range snapshot.Accounts {
		if account.ID == f.accounts[0].ID && account.State != "healthy" {
			t.Fatalf("elapsed engine cooldown stayed blocked: %+v", account)
		}
	}
	if err = f.pool.Reserve(ctx, f.reservation("recovered", 0, 10)); err != nil {
		t.Fatalf("canonical engine eligibility not used: %v", err)
	}
}
