package neoroute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type routeFixture struct {
	route     *Store
	identity  *neo.Store
	pool      *neopool.Store
	manager   *coreauth.Manager
	admin     neo.Principal
	user      neo.User
	principal neo.Principal
	ctx       context.Context
	vm        VirtualModel
	accounts  []neopool.Account
}

func newRouteFixture(t *testing.T, draw string) *routeFixture {
	t.Helper()
	ctx := context.Background()
	identity, err := neo.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := identity.Close(); err != nil {
			t.Error(err)
		}
	})
	session, _, err := identity.Bootstrap(ctx, "captain", "Captain", "a-test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	admin := neo.Principal{UserID: session.UserID, Role: "admin", Session: true}
	manager := coreauth.NewManager(nil, nil, nil)
	for _, suffix := range []string{"a", "b"} {
		id := "route-test-" + uuid.NewString() + suffix
		a := &coreauth.Auth{ID: id, Provider: "primary", Status: coreauth.StatusActive}
		if _, err = manager.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "primary", []*registry.ModelInfo{{ID: "model", Object: "model", ContextLength: 10000}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	pool, err := neopool.New(identity.DB(), manager)
	if err != nil {
		t.Fatal(err)
	}
	identity.SetAccountScopeValidator(pool.ScopeOwned)
	snapshot, err := pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range snapshot.Accounts {
		if err = pool.AssignOwner(ctx, a.ID, admin.UserID); err != nil {
			t.Fatal(err)
		}
		if err = pool.ConfigureWindow(ctx, a.ID, admin.UserID, "day", 1000000, time.Now().Add(-time.Hour).UnixMilli(), true); err != nil {
			t.Fatal(err)
		}
	}
	route, err := New(identity, pool)
	if err != nil {
		t.Fatal(err)
	}
	vm := VirtualModel{Name: "route", Stages: []Stage{{ID: "s0", Model: "primary/model", Accounts: AccountScope{All: true}, Draw: draw, PerAccount: .5, Cap: .8, Window: "day", Per: "user"}}, Terminal: Terminal{Kind: "limit", Message: "Wait for reset"}}
	vm, err = route.SaveVirtualModel(neo.WithPrincipal(ctx, admin), vm, true)
	if err != nil {
		t.Fatal(err)
	}
	user, err := identity.CreateUser(ctx, neo.UserInput{Handle: "client", Role: "service", State: "active", Grants: []neo.Grant{{VirtualModel: vm.ID, Allotment: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := identity.MintKey(ctx, admin, user.ID, "test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := identity.AuthenticateKey(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = pool.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &routeFixture{route: route, identity: identity, pool: pool, manager: manager, admin: admin, user: user, principal: principal, ctx: neo.WithPrincipal(ctx, principal), vm: vm, accounts: snapshot.Accounts}
}
func boundedRequest(stream bool) Request {
	return Request{Asked: "neoproxy/route", Protocol: "openai", Body: []byte(`{"model":"neoproxy/route","messages":[{"role":"user","content":"hello"}],"max_tokens":32}`), Stream: stream}
}
func reported(ctx context.Context, selection Selection, in, out int64) {
	usage.PublishRecord(ctx, usage.Record{RequestID: uuid.NewString(), Provider: selection.Provider, Model: selection.Model, AuthID: selection.AuthID, Detail: usage.Detail{InputTokens: in, OutputTokens: out, TotalTokens: in + out, TokenBreakdown: usage.NewSubsetTokenBreakdown(in, 0, 0, out, 0, in+out)}})
}
func successful(ctx context.Context, selection Selection) (*Execution, error) {
	reported(ctx, selection, 10, 5)
	return &Execution{Body: []byte(`{"ok":true}`)}, nil
}
func loadRecord(t *testing.T, f *routeFixture, id string) RequestRecord {
	t.Helper()
	var raw []byte
	if err := f.identity.DB().QueryRow(`SELECT body FROM neoroute_requests WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record RequestRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}
func (f *routeFixture) save(t *testing.T) {
	t.Helper()
	var err error
	f.vm, err = f.route.SaveVirtualModel(neo.WithPrincipal(context.Background(), f.admin), f.vm, false)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpperBoundAcceptsExplicitTextMaximumWithoutCatalogueContext(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":"choose-a"}],"max_tokens":32}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"max_completion_tokens":32}`,
		`{"input":"hello","max_output_tokens":32}`,
		`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"max_output_tokens":32}`,
		`{"messages":[{"role":"user","content":"images and audio are ordinary words"}],"max_tokens":32}`,
	} {
		bound, err := upperBound(Request{Body: []byte(body)}, neopool.Model{})
		if err != nil || bound != int64(len(body))+32 {
			t.Fatalf("explicit text bound=%d error=%v body=%s", bound, err, body)
		}
	}
}

func TestUpperBoundRequiresCatalogueCeilingForStructuredMedia(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image"}}]}],"max_tokens":32}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"image-bytes"}}]}],"max_tokens":32}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/image"}]}],"max_output_tokens":32}`,
		`{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"audio-bytes","format":"wav"}}]}],"max_tokens":32}`,
		`{"input":[{"role":"user","content":[{"type":"input_file","file_data":"file-bytes"}]}],"max_output_tokens":32}`,
	} {
		if _, err := upperBound(Request{Body: []byte(body)}, neopool.Model{}); err == nil {
			t.Fatalf("media request accepted a byte-only bound: %s", body)
		}
		bound, err := upperBound(Request{Body: []byte(body)}, neopool.Model{Context: 4096})
		if err != nil || bound != 4096 {
			t.Fatalf("media context bound=%d error=%v body=%s", bound, err, body)
		}
	}
}

func TestDurableCursorAndReadOnlySimulation(t *testing.T) {
	f := newRouteFixture(t, "alternate")
	for i := 0; i < 2; i++ {
		d, err := f.route.Simulate(f.ctx, boundedRequest(false))
		if err != nil || d.AccountID != f.accounts[0].ID {
			t.Fatalf("simulation rotated: %+v %v", d, err)
		}
	}
	var count int
	if err := f.identity.DB().QueryRow(`SELECT count(*) FROM neo_pool_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("simulation mutated ledger %d %v", count, err)
	}
	result, err := f.route.Run(f.ctx, boundedRequest(false), successful)
	if err != nil {
		t.Fatal(err)
	}
	record := loadRecord(t, f, result.Headers.Get("X-NeoProxy-Request-ID"))
	if record.Decision.AccountID != f.accounts[0].ID || record.Tokens.In != 10 || record.Tokens.Out != 5 {
		t.Fatalf("record %+v", record)
	}
	reopened, err := New(f.identity, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	f.route = reopened
	d, err := f.route.Simulate(f.ctx, boundedRequest(false))
	if err != nil || d.AccountID != f.accounts[1].ID {
		t.Fatalf("cursor not durable: %+v %v", d, err)
	}
	usageRows, err := f.pool.Usage(context.Background(), f.user.ID)
	if err != nil || len(usageRows) != 1 || usageRows[0].Tokens != 15 || usageRows[0].Reserved != 0 {
		t.Fatalf("usage %+v %v", usageRows, err)
	}
}
func TestRealFailureDemotesAndPreservesAsked(t *testing.T) {
	f := newRouteFixture(t, "fill")
	f.vm.Stages = append(f.vm.Stages, Stage{ID: "s1", Model: "primary/model", Accounts: AccountScope{All: true}, Draw: "fill", PerAccount: .5, Cap: .8, Window: "day", Per: "user"})
	f.save(t)
	attempts := 0
	result, err := f.route.Run(f.ctx, boundedRequest(false), func(ctx context.Context, selection Selection) (*Execution, error) {
		attempts++
		if selection.Asked != "neoproxy/route" || selection.AuthID == "" || selection.Provider != "primary" {
			t.Fatal(selection)
		}
		if selection.Decision.StageID == "s0" {
			return nil, fmt.Errorf("forced upstream failure")
		}
		return successful(ctx, selection)
	})
	if err != nil {
		t.Fatal(err)
	}
	record := loadRecord(t, f, result.Headers.Get("X-NeoProxy-Request-ID"))
	if attempts != 2 || record.Asked != "neoproxy/route" || record.Decision.StageID != "s1" || record.Decision.Trail[0].Skipped != "upstream-error" || record.Decision.Trail[0].Chosen == "" {
		t.Fatalf("attempts=%d record=%+v", attempts, record)
	}
	rows, err := f.pool.Usage(context.Background(), f.user.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v %v", rows, err)
	}
	for _, row := range rows {
		if row.StageID == "s0" && row.Tokens == 0 {
			t.Fatal("unknown failed execution was falsely free")
		}
	}
}
func TestAtomicReservationRollsBackCursor(t *testing.T) {
	f := newRouteFixture(t, "alternate")
	f.vm.Stages[0].PerAccount = .000001
	f.vm.Stages[0].Cap = .000001
	f.save(t)
	called := false
	_, err := f.route.Run(f.ctx, boundedRequest(false), func(context.Context, Selection) (*Execution, error) { called = true; return nil, nil })
	var policy *Error
	if !errors.As(err, &policy) || policy.Code != "neoproxy_limit" || called {
		t.Fatalf("called=%v error=%v", called, err)
	}
	cursor, err := f.route.cursor(context.Background(), f.vm.ID, "s0")
	if err != nil || cursor != 0 {
		t.Fatalf("failed reservations advanced cursor: %d %v", cursor, err)
	}
	var count int
	_ = f.identity.DB().QueryRow(`SELECT count(*) FROM neo_pool_attempts`).Scan(&count)
	if count != 0 {
		t.Fatal("rejected reservations persisted", count)
	}
}

func TestConcurrentRoutersReserveAndAdvanceOneDurableCursor(t *testing.T) {
	f := newRouteFixture(t, "alternate")
	other, err := New(f.identity, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	const requests = 8
	results := make(chan error, requests)
	start := make(chan struct{})
	for i := 0; i < requests; i++ {
		router := f.route
		if i%2 != 0 {
			router = other
		}
		go func() {
			<-start
			_, err := router.Run(f.ctx, boundedRequest(false), successful)
			results <- err
		}()
	}
	close(start)
	for i := 0; i < requests; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	cursor, err := f.route.cursor(t.Context(), f.vm.ID, "s0")
	if err != nil || cursor != requests {
		t.Fatalf("committed cursor=%d expected=%d error=%v", cursor, requests, err)
	}
	rows, err := f.pool.Usage(t.Context(), f.user.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("usage rows=%+v error=%v", rows, err)
	}
	for _, row := range rows {
		if row.Tokens != requests/2*15 || row.Reserved != 0 {
			t.Fatalf("alternate draw lost a reservation or cursor update: %+v", row)
		}
	}
}
func TestBudgetFreePassthroughStillScopedAndAttributed(t *testing.T) {
	f := newRouteFixture(t, "fill")
	f.vm.Stages[0].Cap = 0
	f.vm.Terminal = Terminal{Kind: "passthrough", Model: "primary/model"}
	f.save(t)
	result, err := f.route.Run(f.ctx, boundedRequest(false), successful)
	if err != nil {
		t.Fatal(err)
	}
	record := loadRecord(t, f, result.Headers.Get("X-NeoProxy-Request-ID"))
	if record.Decision.Direct || record.Decision.StageID != "" || record.Decision.VirtualModel != f.vm.ID {
		t.Fatal(record)
	}
	rows, err := f.pool.Usage(context.Background(), f.user.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("passthrough drew stage budget: %+v %v", rows, err)
	}
	stats, err := f.pool.Stats(context.Background(), f.admin.UserID, false)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, stat := range stats {
		total += stat.InputTokens + stat.OutputTokens
	}
	if total != 15 {
		t.Fatalf("unattributed passthrough tokens %d", total)
	}
}
func TestQueuedRequestCancellationAndGrantRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			f := newRouteFixture(t, "fill")
			f.vm.Stages[0].Cap = 0
			f.vm.Terminal = Terminal{Kind: "queue", Message: "Waiting"}
			f.save(t)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			done := make(chan error, 1)
			entered := make(chan struct{})
			observed := &observedPool{Pool: f.pool, entered: entered}
			f.route.pools = observed
			go func() { _, err := f.route.Run(ctx, boundedRequest(false), successful); done <- err }()
			<-entered
			if revoke {
				_, err := f.identity.UpdateUser(context.Background(), f.user.ID, neo.UserInput{Handle: f.user.Handle, Role: "service", State: "active"})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			select {
			case err := <-done:
				if revoke {
					var policy *Error
					if !errors.As(err, &policy) || policy.Decision.Reason != "not-granted" {
						t.Fatalf("revoked queued grant: %v", err)
					}
				} else if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel error %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("queue did not wake")
			}
		})
	}
}

type observedPool struct {
	Pool
	entered chan struct{}
	once    sync.Once
}

func (p *observedPool) Usage(ctx context.Context, user string) ([]neopool.Usage, error) {
	rows, err := p.Pool.Usage(ctx, user)
	p.once.Do(func() { close(p.entered) })
	return rows, err
}
func TestQueueWakesOnPolicyEligibility(t *testing.T) {
	f := newRouteFixture(t, "fill")
	f.vm.Stages[0].Cap = 0
	f.vm.Terminal.Kind = "queue"
	f.save(t)
	entered := make(chan struct{})
	f.route.pools = &observedPool{Pool: f.pool, entered: entered}
	done := make(chan error, 1)
	go func() { _, err := f.route.Run(f.ctx, boundedRequest(false), successful); done <- err }()
	<-entered
	f.vm.Stages[0].Cap = .8
	f.save(t)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queue missed policy wake")
	}
}
func TestPartialStreamNeverReplaysAndEarlyFailureDoes(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			f := newRouteFixture(t, "fill")
			f.vm.Stages = append(f.vm.Stages, Stage{ID: "s1", Model: "primary/model", Accounts: AccountScope{All: true}, Draw: "fill", PerAccount: .5, Cap: .8, Window: "day", Per: "user"})
			f.save(t)
			calls := 0
			result, err := f.route.Run(f.ctx, boundedRequest(true), func(ctx context.Context, selection Selection) (*Execution, error) {
				calls++
				chunks := make(chan Chunk, 3)
				if selection.Decision.StageID == "s0" {
					if partial {
						chunks <- Chunk{Data: []byte("first answer")}
					}
					chunks <- Chunk{Err: fmt.Errorf("forced stream failure")}
				} else {
					reported(ctx, selection, 10, 5)
					chunks <- Chunk{Data: []byte("fallback answer")}
				}
				close(chunks)
				return &Execution{Chunks: chunks}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var body strings.Builder
			var terminal error
			for chunk := range result.Chunks {
				body.Write(chunk.Data)
				if chunk.Err != nil {
					terminal = chunk.Err
				}
			}
			if partial {
				if calls != 1 || body.String() != "first answer" || terminal == nil {
					t.Fatalf("partial replay calls=%d body=%s err=%v", calls, body.String(), terminal)
				}
			} else if calls != 2 || body.String() != "fallback answer" || terminal != nil {
				t.Fatalf("early fallback calls=%d body=%s err=%v", calls, body.String(), terminal)
			}
		})
	}
}

type testScript struct {
	evaluate func(context.Context, ScriptInput) (ScriptProposal, error)
}

func (h testScript) Evaluate(ctx context.Context, input ScriptInput) (ScriptProposal, error) {
	return h.evaluate(ctx, input)
}
func (h testScript) Validate(string) error { return nil }
func TestScriptRuntimeRevalidatesAuthorityAndInput(t *testing.T) {
	f := newRouteFixture(t, "fill")
	f.vm.Script = "a real configured source"
	if _, err := f.route.SaveVirtualModel(neo.WithPrincipal(context.Background(), f.admin), f.vm, false); err == nil {
		t.Fatal("configured source accepted without evaluator")
	}
	f.route.SetScriptHook(testScript{evaluate: func(ctx context.Context, input ScriptInput) (ScriptProposal, error) {
		if !strings.Contains(string(input.Request), "hello") {
			return ScriptProposal{}, fmt.Errorf("request snapshot missing")
		}
		_, err := f.identity.UpdateUser(ctx, f.user.ID, neo.UserInput{Handle: f.user.Handle, Role: "service", State: "active"})
		if err != nil {
			return ScriptProposal{}, err
		}
		return ScriptProposal{StageID: "s0", AccountID: f.accounts[0].ID}, nil
	}})
	f.save(t)
	d, err := f.route.Simulate(f.ctx, boundedRequest(false))
	if err != nil || d.Reason != "not-granted" {
		t.Fatalf("script froze revoked grant: %+v %v", d, err)
	}
}
func TestScriptMissingHookAndExhaustedTerminal(t *testing.T) {
	f := newRouteFixture(t, "fill")
	f.route.SetScriptHook(testScript{evaluate: func(context.Context, ScriptInput) (ScriptProposal, error) {
		t.Fatal("exhausted stage invoked impossible proposal")
		return ScriptProposal{}, nil
	}})
	f.vm.Script = "configured"
	f.vm.Stages[0].Cap = 0
	f.save(t)
	d, err := f.route.Simulate(f.ctx, boundedRequest(false))
	if err != nil || d.Outcome != "limited" {
		t.Fatalf("terminal %+v %v", d, err)
	}
	f.route.SetScriptHook(nil)
	_, err = f.route.Simulate(f.ctx, boundedRequest(false))
	var policy *Error
	if !errors.As(err, &policy) || policy.Code != "neoproxy_script_error" {
		t.Fatalf("missing evaluator silently ignored: %v", err)
	}
}
func TestUserLimitsDoNotInvokeExecutor(t *testing.T) {
	for _, tokens := range []bool{false, true} {
		t.Run(fmt.Sprint(tokens), func(t *testing.T) {
			f := newRouteFixture(t, "fill")
			one := int64(1)
			limits := neo.Limits{RPM: &one}
			if tokens {
				limits = neo.Limits{TokensPerDay: &one}
			}
			_, err := f.identity.UpdateUser(context.Background(), f.user.ID, neo.UserInput{Handle: f.user.Handle, Role: "service", State: "active", Grants: []neo.Grant{{VirtualModel: f.vm.ID, Allotment: 1}}, Limits: limits})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			execute := func(ctx context.Context, s Selection) (*Execution, error) { calls++; return successful(ctx, s) }
			if !tokens {
				if _, err = f.route.Run(f.ctx, boundedRequest(false), execute); err != nil {
					t.Fatal(err)
				}
			}
			_, err = f.route.Run(f.ctx, boundedRequest(false), execute)
			var policy *Error
			if !errors.As(err, &policy) || policy.Code != "neoproxy_user_limit" {
				t.Fatalf("limit %v", err)
			}
			wantCalls := 0
			if !tokens {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("executor calls %d want %d", calls, wantCalls)
			}
		})
	}
}
