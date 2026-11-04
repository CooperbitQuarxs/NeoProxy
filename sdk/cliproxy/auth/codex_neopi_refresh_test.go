package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func TestCodexNeoPiRefreshSchedulingCapture(t *testing.T) {
	type credential struct {
		Access  string `json:"access"`
		Refresh string `json:"refresh"`
		Expires int64  `json:"expires"`
	}
	var capture struct {
		Now       int64      `json:"now"`
		Original  credential `json:"original"`
		Scenarios []struct {
			Name    string      `json:"name"`
			Current *credential `json:"current"`
			Result  struct {
				Refreshed bool `json:"refreshed"`
			} `json:"result"`
		} `json:"scenarios"`
	}
	raw, err := os.ReadFile("../../../internal/auth/codex/testdata/neopi_refresh_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(capture.Now)
	manager := NewManager(nil, nil, nil)
	for _, scenario := range capture.Scenarios {
		switch scenario.Name {
		case "fresh-outside-skew", "exact-sixty-second-boundary", "inside-refresh-skew", "expired":
		case "stored-fresh-beats-expired-jwt", "stored-expired-beats-fresh-jwt":
		default:
			continue
		}
		t.Run(scenario.Name, func(t *testing.T) {
			current := capture.Original
			if scenario.Current != nil {
				current = *scenario.Current
			}
			expiry := time.UnixMilli(current.Expires)
			auth := &Auth{ID: "source-capture", Provider: "codex", Metadata: map[string]any{
				"access_token": current.Access, "refresh_token": current.Refresh, "expired": expiry.Format(time.RFC3339Nano),
				// Old local scheduling knobs must not rotate this grant.
				"refresh_interval_seconds": 86400, "last_refresh": now.Add(-48 * time.Hour).Format(time.RFC3339),
			}}
			due, hasRefresh, refresh := CodexNeedsRefresh(auth, now)
			wantDue := expiry.Add(-time.Minute)
			if wantDue.Before(now) {
				wantDue = now
			}
			if !hasRefresh || refresh != scenario.Result.Refreshed || !due.Equal(wantDue) {
				t.Fatalf("source refresh=%v, got due=%s has=%v refresh=%v; want due=%s", scenario.Result.Refreshed, due, hasRefresh, refresh, wantDue)
			}
			if got := manager.shouldRefresh(auth, now); got != scenario.Result.Refreshed {
				t.Fatalf("manager refresh=%v, source=%v", got, scenario.Result.Refreshed)
			}
			scheduled, ok := nextRefreshCheckAt(now, auth, time.Second)
			if !ok || !scheduled.Equal(wantDue) {
				t.Fatalf("scheduled=(%s,%v), source due=%s", scheduled, ok, wantDue)
			}
			if actualExpiry, known := auth.AccessTokenExpirationTime(); !known || !actualExpiry.Equal(expiry) {
				t.Fatalf("access validity expiry=(%s,%v), source=%s", actualExpiry, known, expiry)
			}
		})
	}
}

// Observing Done synchronizes a caller after it has joined the manager flight,
// without sleeps or assumptions about goroutine scheduling.
type codexRefreshJoinContext struct {
	context.Context
	joined chan struct{}
	once   sync.Once
}

func (c *codexRefreshJoinContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}

func TestCodexNeoPiRefreshManagerSingleflight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	executor := &unauthorizedRefreshExecutor{
		id: "codex",
		onRefresh: func() {
			startOnce.Do(func() { close(started) })
			<-release
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &Auth{ID: "codex-flight", Provider: "codex", Metadata: map[string]any{
		"access_token": "old-access-fake", "refresh_token": "refresh-fake",
		"expired": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		_, err := manager.ForceRefreshAuth(context.Background(), auth.ID)
		results <- err
	}()
	<-started
	follower := &codexRefreshJoinContext{Context: context.Background(), joined: make(chan struct{})}
	go func() {
		_, err := manager.ForceRefreshAuth(follower, auth.ID)
		results <- err
	}()
	<-follower.joined
	cancelCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceledFollower := &codexRefreshJoinContext{Context: cancelCtx, joined: make(chan struct{})}
	canceledResult := make(chan error, 1)
	go func() {
		_, err := manager.ForceRefreshAuth(canceledFollower, auth.ID)
		canceledResult <- err
	}()
	<-canceledFollower.joined
	cancel()
	select {
	case err := <-canceledResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting caller cancellation = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled refresh waiter remained blocked")
	}
	releaseOnce.Do(func() { close(release) })
	for range 2 {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("a follower canceled the shared refresh: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("shared refresh did not complete")
		}
	}
	executor.mu.Lock()
	calls := executor.refreshCalls
	executor.mu.Unlock()
	if calls != 1 {
		t.Fatalf("concurrent forced token exchanges = %d; NeoPi source = 1", calls)
	}
	if _, err := manager.ForceRefreshAuth(context.Background(), auth.ID); err != nil {
		t.Fatal(err)
	}
	executor.mu.Lock()
	calls = executor.refreshCalls
	executor.mu.Unlock()
	if calls != 2 {
		t.Fatalf("later explicit force reused a completed flight: exchanges = %d", calls)
	}
}
