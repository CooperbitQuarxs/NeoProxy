package neopool

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const (
	metaQuotaOlderUsage = `{"is_subs_active":true,"subs_usage":{"window":{"window_duration_mins":1440,"used_percent":87,"resets_at":"2026-11-06T12:00:00Z"},"weekly":{"used_percent":91,"resets_at":"2026-11-09T00:00:00Z"}}}`
	metaQuotaNewerUsage = `{"is_subs_active":true,"subs_usage":{"window":{"window_duration_mins":300,"used_percent":32,"resets_at":"2026-11-05T15:00:00Z"},"weekly":{"used_percent":48,"resets_at":"2026-11-09T00:00:00Z"}}}`
	metaQuotaIdleUsage  = `{"is_subs_active":true}`
)

type metaQuotaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f metaQuotaRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type metaQuotaReply struct {
	status int
	body   string
}

func (r metaQuotaReply) response(req *http.Request) *http.Response {
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body)), Header: http.Header{}, Request: req}
}

func newMetaQuotaFixture(t *testing.T, roundTrip metaQuotaRoundTripFunc) (*quotaProber, *coreauth.Auth) {
	t.Helper()
	q := newQuotaProber()
	client := &http.Client{Transport: roundTrip}
	q.clientFor = func(*coreauth.Auth) *http.Client { return client }
	a := &coreauth.Auth{ID: t.Name(), Provider: "meta", Metadata: map[string]any{"auth_kind": "oauth", "dca_token": "meta-account-token"}}
	return q, a
}

func assertMetaQuotaUnknown(t *testing.T, windows []Window) {
	t.Helper()
	if len(windows) == 0 {
		t.Fatal("failed Muse probe has no unavailable windows")
	}
	for _, window := range windows {
		if window.Known || window.Source != quotaSourceUnavailable {
			t.Fatalf("definitive Muse failure retained last-good usage: %+v", windows)
		}
	}
}

func assertMetaQuotaNewerUsage(t *testing.T, windows []Window) {
	t.Helper()
	if len(windows) != 2 {
		t.Fatalf("newer Muse usage windows: %+v", windows)
	}
	five := windowByKind(t, windows, "5h")
	week := windowByKind(t, windows, "week")
	if !five.Known || five.Used != .32 || !week.Known || week.Used != .48 {
		t.Fatalf("newer Muse usage was replaced: %+v", windows)
	}
}

func assertMetaQuotaIdleUsage(t *testing.T, windows []Window, carryWeekly bool) {
	t.Helper()
	wantCount := 1
	if carryWeekly {
		wantCount++
	}
	if len(windows) != wantCount {
		t.Fatalf("idle Muse usage has incorrect carry-over: %+v", windows)
	}
	five := windowByKind(t, windows, "5h")
	if !five.Known || five.Used != 0 {
		t.Fatalf("idle Muse rolling window: %+v", five)
	}
	if carryWeekly {
		week := windowByKind(t, windows, "week")
		if !week.Known || week.Used != .48 || week.ResetsAt != time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC).UnixMilli() {
			t.Fatalf("idle Muse response did not carry the accepted weekly usage: %+v", week)
		}
	}
}

// A slow probe may answer its caller, but it cannot publish carry-over data or
// invalidate a last-good report after a newer generation has been accepted.
func TestQuotaMetaOverlappingGenerations(t *testing.T) {
	olderUsage := metaQuotaReply{http.StatusOK, metaQuotaOlderUsage}
	newerUsage := metaQuotaReply{http.StatusOK, metaQuotaNewerUsage}
	unauthorized := metaQuotaReply{http.StatusUnauthorized, `{"error":"revoked"}`}
	forbidden := metaQuotaReply{http.StatusForbidden, `{"error":"forbidden"}`}
	inactive := metaQuotaReply{http.StatusOK, `{"is_subs_active":false}`}
	for _, test := range []struct {
		name         string
		older, newer metaQuotaReply
		olderFailure bool
		newerFailure bool
	}{
		{name: "older-usage-newer-usage", older: olderUsage, newer: newerUsage},
		{name: "older-401-newer-usage", older: unauthorized, newer: newerUsage, olderFailure: true},
		{name: "older-403-newer-usage", older: forbidden, newer: newerUsage, olderFailure: true},
		{name: "older-inactive-newer-usage", older: inactive, newer: newerUsage, olderFailure: true},
		{name: "older-usage-newer-401", older: olderUsage, newer: unauthorized, newerFailure: true},
		{name: "older-usage-newer-403", older: olderUsage, newer: forbidden, newerFailure: true},
		{name: "older-usage-newer-inactive", older: olderUsage, newer: inactive, newerFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			releaseOlder := sync.OnceFunc(func() { close(release) })
			defer releaseOlder()
			var requests atomic.Int32
			q, a := newMetaQuotaFixture(t, func(req *http.Request) (*http.Response, error) {
				switch requests.Add(1) {
				case 1:
					close(started)
					select {
					case <-release:
						return test.older.response(req), nil
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				case 2:
					return test.newer.response(req), nil
				case 3:
					return (metaQuotaReply{http.StatusOK, metaQuotaIdleUsage}).response(req), nil
				default:
					return nil, errors.New("unexpected Muse quota request")
				}
			})
			now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
			olderDone := make(chan error, 1)
			go func() { olderDone <- q.refresh(t.Context(), a, now) }()
			<-started
			newerErr := q.refresh(t.Context(), a, now.Add(time.Minute))
			during := q.windows(a, now.Add(time.Minute))
			releaseOlder()
			olderErr := <-olderDone
			if (olderErr != nil) != test.olderFailure || (newerErr != nil) != test.newerFailure {
				t.Fatalf("refresh errors: older=%v newer=%v", olderErr, newerErr)
			}
			after := q.windows(a, now.Add(2*time.Minute))
			if test.newerFailure {
				assertMetaQuotaUnknown(t, during)
				assertMetaQuotaUnknown(t, after)
			} else {
				assertMetaQuotaNewerUsage(t, during)
				assertMetaQuotaNewerUsage(t, after)
			}
			if err := q.refresh(t.Context(), a, now.Add(3*time.Minute)); err != nil {
				t.Fatal(err)
			}
			assertMetaQuotaIdleUsage(t, q.windows(a, now.Add(3*time.Minute)), !test.newerFailure)
		})
	}
}

// auth/usage.ts purges last-good data on 401/403; Muse maps an explicitly
// inactive subscription to 403 too. Neither view may survive that rejection.
func TestQuotaMetaDefinitiveFailuresInvalidateBothCaches(t *testing.T) {
	for _, test := range []struct {
		name       string
		reply      metaQuotaReply
		wantStatus int
	}{
		{name: "unauthorized", reply: metaQuotaReply{http.StatusUnauthorized, `{"error":"revoked"}`}, wantStatus: http.StatusUnauthorized},
		{name: "forbidden", reply: metaQuotaReply{http.StatusForbidden, `{"error":"forbidden"}`}, wantStatus: http.StatusForbidden},
		{name: "inactive-subscription", reply: metaQuotaReply{http.StatusOK, `{"is_subs_active":false}`}, wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			replies := []metaQuotaReply{
				{http.StatusOK, metaQuotaOlderUsage},
				test.reply,
				{http.StatusServiceUnavailable, `{"error":"temporary"}`},
				{http.StatusOK, metaQuotaIdleUsage},
			}
			next := 0
			q, a := newMetaQuotaFixture(t, func(req *http.Request) (*http.Response, error) {
				if next >= len(replies) {
					return nil, errors.New("unexpected Muse quota request")
				}
				reply := replies[next]
				next++
				return reply.response(req), nil
			})
			now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
			if err := q.refresh(t.Context(), a, now); err != nil {
				t.Fatal(err)
			}
			if windows := q.windows(a, now); len(windows) != 2 || !windowByKind(t, windows, "week").Known {
				t.Fatalf("initial Muse usage was not cached: %+v", windows)
			}
			err := q.refresh(t.Context(), a, now.Add(time.Minute))
			var usageErr *metaauth.UsageError
			if !errors.As(err, &usageErr) || usageErr.Status != test.wantStatus {
				t.Fatalf("definitive Muse failure = %v, want status %d", err, test.wantStatus)
			}
			assertMetaQuotaUnknown(t, q.windows(a, now.Add(time.Minute)))
			if err := q.refresh(t.Context(), a, now.Add(2*time.Minute)); !errors.Is(err, errNoMetaUsage) {
				t.Fatalf("transient Muse failure = %v", err)
			}
			assertMetaQuotaUnknown(t, q.windows(a, now.Add(2*time.Minute)))
			if err := q.refresh(t.Context(), a, now.Add(3*time.Minute)); err != nil {
				t.Fatal(err)
			}
			assertMetaQuotaIdleUsage(t, q.windows(a, now.Add(3*time.Minute)), false)
		})
	}
}

func TestQuotaMetaTransientFailureRetainsBothCaches(t *testing.T) {
	replies := []metaQuotaReply{
		{http.StatusOK, metaQuotaNewerUsage},
		{http.StatusServiceUnavailable, `{"error":"temporary"}`},
		{http.StatusOK, metaQuotaIdleUsage},
	}
	next := 0
	q, a := newMetaQuotaFixture(t, func(req *http.Request) (*http.Response, error) {
		if next >= len(replies) {
			return nil, errors.New("unexpected Muse quota request")
		}
		reply := replies[next]
		next++
		return reply.response(req), nil
	})
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := q.refresh(t.Context(), a, now); err != nil {
		t.Fatal(err)
	}
	if err := q.refresh(t.Context(), a, now.Add(time.Minute)); !errors.Is(err, errNoMetaUsage) {
		t.Fatalf("transient Muse failure = %v", err)
	}
	assertMetaQuotaNewerUsage(t, q.windows(a, now.Add(time.Minute)))
	if err := q.refresh(t.Context(), a, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertMetaQuotaIdleUsage(t, q.windows(a, now.Add(2*time.Minute)), true)
}
