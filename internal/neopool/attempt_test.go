package neopool

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestAttemptCaptureIsSynchronousIdempotentAndActualAccountBound(t *testing.T) {
	manager := usage.NewManager(1)
	defer manager.Stop()
	ctx, capture := WithAttempt(context.Background(), "attempt")
	record := usage.Record{RequestID: "execution-one", AuthID: "auth-one", Provider: "codex", Model: "model", Detail: usage.Detail{InputTokens: 10, OutputTokens: 3, CacheReadTokens: 4, CachedTokens: 4, TotalTokens: 13}}
	manager.Publish(ctx, record)
	manager.Publish(ctx, record)
	settled, err := capture.Settlement(AccountID("auth-one"))
	if err != nil {
		t.Fatal(err)
	}
	if !settled.UsageKnown || settled.InputTokens != 10 || settled.OutputTokens != 3 || settled.CachedTokens != 4 {
		t.Fatalf("synchronous usage or deduplication failed: %+v", settled)
	}
	if _, err = capture.Settlement(AccountID("not-the-executor")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("selection substituted for actual auth: %v", err)
	}
	// A failed execution with reported consumption still costs those exact tokens.
	failedCtx, failed := WithAttempt(context.Background(), "failed")
	record.RequestID = "execution-two"
	record.Failed = true
	manager.Publish(failedCtx, record)
	settled, err = failed.Settlement(AccountID("auth-one"))
	if err != nil || !settled.UsageKnown || settled.InputTokens != 10 {
		t.Fatalf("failed reported use lost: %+v %v", settled, err)
	}
}
func TestAttemptMissingAndConflictingReportsStayUnknown(t *testing.T) {
	manager := usage.NewManager(1)
	defer manager.Stop()
	ctx, capture := WithAttempt(context.Background(), "unknown")
	value, err := capture.Settlement(AccountID("auth"))
	if err != nil || value.UsageKnown {
		t.Fatalf("missing use fabricated: %+v %v", value, err)
	}
	record := usage.Record{RequestID: "execution", AuthID: "auth", Provider: "codex", Model: "model", Failed: true}
	manager.Publish(ctx, record)
	value, err = capture.Settlement(AccountID("auth"))
	if err != nil || value.UsageKnown {
		t.Fatalf("empty failure fabricated exact zero: %+v %v", value, err)
	}
	record.Detail = usage.Detail{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}
	manager.Publish(ctx, record)
	if _, err = capture.Settlement(AccountID("auth")); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting duplicate report accepted: %v", err)
	}
}
func TestAttemptAnthropicCacheUsesCanonicalTokenBreakdown(t *testing.T) {
	manager := usage.NewManager(1)
	defer manager.Stop()
	ctx, capture := WithAttempt(context.Background(), "anthropic")
	manager.Publish(ctx, usage.Record{RequestID: "claude-execution", AuthID: "claude-auth", Provider: "claude", ExecutorType: "claude", Model: "model", Detail: usage.Detail{InputTokens: 10, OutputTokens: 3, CacheReadTokens: 5, CacheCreationTokens: 2, CachedTokens: 7, TotalTokens: 20}})
	settled, err := capture.Settlement(AccountID("claude-auth"))
	if err != nil {
		t.Fatal(err)
	}
	if !settled.UsageKnown || settled.InputTokens != 17 || settled.OutputTokens != 3 || settled.CachedTokens != 5 {
		t.Fatalf("canonical Anthropic input/cache accounting: %+v", settled)
	}
}

func TestAttemptCachedProjectionDoesNotAddLedgerDebit(t *testing.T) {
	for _, cached := range []int64{0, 4} {
		t.Run(fmt.Sprintf("canonical-cache-%d", cached), func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			f.assign(t, 0, "owner-a")
			f.meter(t, 0, "day", 100)
			reservation := f.reservation("cached-subset", 0, 20)
			if err := f.pool.Reserve(ctx, reservation); err != nil {
				t.Fatal(err)
			}
			ctx, capture := WithAttempt(ctx, reservation.ID)
			manager := usage.NewManager(1)
			defer manager.Stop()
			// The canonical breakdown, including an explicit zero cache bucket,
			// wins over the ambiguous legacy CachedTokens display field.
			manager.Publish(ctx, usage.Record{
				RequestID: reservation.RequestID, AuthID: f.accounts[0].EngineAuthID,
				Provider: "codex", Model: "model",
				Detail: usage.Detail{InputTokens: 10, OutputTokens: 3, CachedTokens: 9,
					TokenBreakdown: usage.NewSubsetTokenBreakdown(10, cached, 0, 3, 0, 13)},
			})
			settled, err := capture.Settlement(reservation.AccountID)
			if err != nil {
				t.Fatal(err)
			}
			if !settled.UsageKnown || settled.InputTokens != 10 || settled.OutputTokens != 3 || settled.CachedTokens != cached {
				t.Fatalf("canonical cached projection: %+v", settled)
			}
			if err = f.pool.Settle(ctx, settled); err != nil {
				t.Fatal(err)
			}
			rows, err := f.pool.Usage(ctx, "user-a")
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Tokens != 13 || rows[0].Fraction != .13 || rows[0].Reserved != 0 {
				t.Fatalf("cached input charged twice: %+v", rows)
			}
			stats, err := f.pool.Stats(ctx, "owner-a", false)
			if err != nil {
				t.Fatal(err)
			}
			if len(stats) != 1 || stats[0].InputTokens != 10 || stats[0].OutputTokens != 3 || stats[0].CachedTokens != cached {
				t.Fatalf("owner cached projection: %+v", stats)
			}
		})
	}
}
