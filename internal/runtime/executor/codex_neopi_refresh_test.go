package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type codexRefreshCapture struct {
	Now       int64          `json:"now"`
	Original  map[string]any `json:"original"`
	Minted    map[string]any `json:"minted"`
	Scenarios []struct {
		Name          string          `json:"name"`
		API           string          `json:"api"`
		Current       map[string]any  `json:"current"`
		Observed      map[string]any  `json:"observed"`
		Minted        map[string]any  `json:"minted"`
		DuringRefresh json.RawMessage `json:"duringRefresh"`
		Force         bool            `json:"force"`
		Missing       bool            `json:"missing"`
		Disabled      bool            `json:"disabled"`
		RefreshError  string          `json:"refreshError"`
		Result        map[string]any  `json:"result"`
		Stored        map[string]any  `json:"stored"`
		RefreshCalls  int             `json:"refreshCalls"`
	} `json:"scenarios"`
}

func readCodexRefreshCapture(t *testing.T) codexRefreshCapture {
	t.Helper()
	raw, err := os.ReadFile("../../auth/codex/testdata/neopi_refresh_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture codexRefreshCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	return capture
}

func codexCapturedMetadata(credential map[string]any) map[string]any {
	metadata := make(map[string]any, len(credential))
	for key, value := range credential {
		switch key {
		case "type":
			value = "codex"
		case "access":
			key = "access_token"
		case "refresh":
			key = "refresh_token"
		case "accountId":
			key = "account_id"
		case "expires":
			key = "expired"
			value = time.UnixMilli(int64(value.(float64))).UTC().Format(time.RFC3339Nano)
		}
		metadata[key] = value
	}
	return metadata
}

func assertCodexCapturedCredential(t *testing.T, actual, expected map[string]any) {
	t.Helper()
	for key, want := range codexCapturedMetadata(expected) {
		if !reflect.DeepEqual(actual[key], want) {
			t.Errorf("credential field %s=%v; source=%v", key, actual[key], want)
		}
	}
}

func TestCodexNeoPiRefreshLifecycleCapture(t *testing.T) {
	capture := readCodexRefreshCapture(t)
	for _, scenario := range capture.Scenarios {
		switch scenario.Name {
		case "fresh-outside-skew", "exact-sixty-second-boundary", "inside-refresh-skew", "expired", "force-fresh", "adopt-peer", "adopt-peer-even-forced", "refresh-stale-peer", "cas-lost-to-peer", "cas-lost-to-removal", "transient-failure", "preserve-subtype-metadata", "minted-short-lived", "minted-already-expired":
		case "stored-fresh-beats-expired-jwt", "stored-expired-beats-fresh-jwt", "cas-lost-to-expiry-edit", "cas-lost-to-email-edit":
		case "missing-row", "definitive-failure", "definitive-failure-peer-wins", "definitive-failure-expiry-edit-wins", "definitive-failure-email-edit-wins", "definitive-failure-removal-wins", "transient-failure-peer-wins":
		default:
			continue
		}
		t.Run(scenario.Name, func(t *testing.T) {
			current := scenario.Current
			if current == nil {
				current = capture.Original
			}
			observed := scenario.Observed
			if observed == nil {
				observed = current
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "credential.json")
			if !scenario.Missing {
				if err := codexauth.WriteJSONFileAtomic(path, codexCapturedMetadata(current)); err != nil {
					t.Fatal(err)
				}
			}
			auth := &cliproxyauth.Auth{ID: "credential.json", Provider: "codex", Metadata: codexCapturedMetadata(observed), Attributes: map[string]string{cliproxyauth.AttributePath: path}}
			engine := NewCodexExecutor(&config.Config{AuthDir: dir})
			engine.refreshClock = func() time.Time { return time.UnixMilli(capture.Now) }
			calls := 0
			engine.refreshToken = func(_ context.Context, attempted *cliproxyauth.Auth) (*codexauth.CodexTokenData, error) {
				calls++
				if attempted.Metadata["refresh_token"] != current["refresh"] {
					t.Errorf("exchange did not use current persisted refresh grant")
				}
				if len(scenario.DuringRefresh) > 0 {
					if string(scenario.DuringRefresh) == `"remove"` {
						if err := os.Remove(path); err != nil {
							t.Error(err)
						}
					} else {
						var peer map[string]any
						if err := json.Unmarshal(scenario.DuringRefresh, &peer); err != nil {
							return nil, err
						}
						if err := codexauth.WriteJSONFileAtomic(path, codexCapturedMetadata(peer)); err != nil {
							t.Error(err)
						}
					}
				}
				if scenario.RefreshError != "" {
					return nil, errors.New(scenario.RefreshError)
				}
				minted := scenario.Minted
				if minted == nil {
					minted = capture.Minted
				}
				return &codexauth.CodexTokenData{AccessToken: minted["access"].(string), RefreshToken: minted["refresh"].(string), Expire: time.UnixMilli(int64(minted["expires"].(float64))).UTC().Format(time.RFC3339Nano)}, nil
			}
			ctx := context.Background()
			if scenario.Force {
				ctx = cliproxyauth.WithForceRefresh(ctx)
			}
			refreshed, err := engine.Refresh(ctx, auth)
			expected := scenario.Result
			if scenario.API == "" && expected != nil {
				expected, _ = expected["credential"].(map[string]any)
			}
			if expected != nil {
				if err != nil || refreshed == nil {
					t.Fatalf("refresh failed: %v", err)
				}
				assertCodexCapturedCredential(t, refreshed.Metadata, expected)
			} else if err == nil {
				t.Fatal("refresh returned a credential where the source has none")
			}
			if calls != scenario.RefreshCalls {
				t.Errorf("token exchanges=%d; source=%d", calls, scenario.RefreshCalls)
			}
			raw, readErr := os.ReadFile(path)
			if scenario.Disabled {
				if readErr != nil {
					t.Fatal(readErr)
				}
				var stored map[string]any
				if err := json.Unmarshal(raw, &stored); err != nil {
					t.Fatal(err)
				}
				if stored["disabled"] != true || refreshed == nil || !refreshed.Disabled {
					t.Fatal("definitively failed grant remained enabled on disk or in the refresh result")
				}
				assertCodexCapturedCredential(t, stored, current)
			} else if scenario.Stored == nil {
				if !os.IsNotExist(readErr) {
					t.Errorf("removed credential reappeared: %v", readErr)
				}
			} else {
				if readErr != nil {
					t.Fatal(readErr)
				}
				var stored map[string]any
				if err := json.Unmarshal(raw, &stored); err != nil {
					t.Fatal(err)
				}
				assertCodexCapturedCredential(t, stored, scenario.Stored)
			}
			assertCodexCapturedCredential(t, auth.Metadata, observed)
		})
	}
}

func TestCodexNeoPiRefreshRecoveryMint(t *testing.T) {
	capture := readCodexRefreshCapture(t)
	clock := time.UnixMilli(capture.Now)
	engine := NewCodexExecutor(&config.Config{})
	engine.refreshClock = func() time.Time { return clock }
	var calls atomic.Int32
	engine.refreshToken = func(_ context.Context, _ *cliproxyauth.Auth) (*codexauth.CodexTokenData, error) {
		calls.Add(1)
		return &codexauth.CodexTokenData{AccessToken: "minted-access-fake", RefreshToken: "minted-refresh-fake", Expire: clock.Add(time.Hour).UTC().Format(time.RFC3339Nano)}, nil
	}
	original := &cliproxyauth.Auth{ID: "recovery", Provider: "codex", Metadata: codexCapturedMetadata(capture.Original)}
	minted, err := engine.Refresh(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	minted.RejectedAccessToken = "minted-access-fake"
	reused, err := engine.Refresh(context.Background(), minted)
	if err != nil || calls.Load() != 1 || reused.RejectedAccessToken != "" {
		t.Fatalf("auth recovery reminted or retained rejection: calls=%d err=%v", calls.Load(), err)
	}
	_, err = engine.Refresh(cliproxyauth.WithForceRefresh(context.Background()), reused)
	if err != nil || calls.Load() != 2 {
		t.Fatalf("explicit force did not mint: calls=%d err=%v", calls.Load(), err)
	}
	clock = clock.Add(5 * time.Minute)
	_, err = engine.Refresh(context.Background(), minted)
	if err != nil || calls.Load() != 3 {
		t.Fatalf("expired recovery cooldown suppressed refresh: calls=%d err=%v", calls.Load(), err)
	}
	if strings.Contains(original.Metadata["access_token"].(string), "minted") {
		t.Fatal("refresh mutated the caller snapshot")
	}
}

type codexRefreshPublicationStore struct {
	*sdkauth.FileTokenStore
	beforeSave func(*cliproxyauth.Auth)
}

func (s *codexRefreshPublicationStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if s.beforeSave != nil {
		s.beforeSave(auth)
	}
	return s.FileTokenStore.Save(ctx, auth)
}

func TestCodexNeoPiRefreshFailurePublication(t *testing.T) {
	for _, tc := range []struct {
		name, failure, race      string
		quota, disabled, success bool
	}{
		{name: "invalid-grant", failure: "invalid_grant", disabled: true},
		{name: "bare-unauthorized", failure: "401 Unauthorized", disabled: true},
		{name: "transient", failure: "temporarily_unavailable"},
		{name: "transient-401", failure: "401 temporarily_unavailable"},
		{name: "peer-during-exchange", failure: "invalid_grant", race: "exchange", success: true},
		{name: "peer-before-publication", failure: "invalid_grant", race: "publication", success: true},
		{name: "expiry-edit-before-publication", failure: "invalid_grant", race: "expiry", success: true},
		{name: "peer-preserves-quota", failure: "invalid_grant", race: "publication", quota: true, success: true},
		{name: "deletion-before-disable-publication", failure: "invalid_grant", race: "remove"},
		{name: "deletion-before-mint-publication", race: "remove"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "credential.json")
			now := time.Now().Truncate(time.Millisecond)
			store := &codexRefreshPublicationStore{FileTokenStore: sdkauth.NewFileTokenStore()}
			store.SetBaseDir(dir)
			manager := cliproxyauth.NewManager(store, nil, nil)
			engine := NewCodexExecutor(&config.Config{AuthDir: dir})
			engine.refreshClock = func() time.Time { return now }
			manager.RegisterExecutor(engine)
			initial := &cliproxyauth.Auth{
				ID: "credential.json", FileName: "credential.json", Provider: "codex", Status: cliproxyauth.StatusActive,
				Attributes: map[string]string{cliproxyauth.AttributePath: path},
				Metadata:   map[string]any{"type": "codex", "access_token": "original-access-fake", "refresh_token": "original-refresh-fake", "expired": now.Add(-time.Minute).Format(time.RFC3339Nano)},
			}
			if tc.quota {
				initial.Quota.Exceeded = true
				initial.Quota.Reason = "credential_quota"
				initial.Quota.NextRecoverAt = now.Add(2 * time.Hour)
				initial.Unavailable = true
				initial.Status = cliproxyauth.StatusError
				initial.NextRetryAfter = initial.Quota.NextRecoverAt
			}
			base, err := manager.Register(context.Background(), initial)
			if err != nil {
				t.Fatal(err)
			}
			peer := base.Clone()
			peer.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339Nano)
			if tc.race != "expiry" {
				peer.Metadata["access_token"] = "peer-access-fake"
				peer.Metadata["refresh_token"] = "peer-refresh-fake"
			}
			writePeer := func() {
				if err := codexauth.WriteJSONFileAtomic(path, peer.Metadata); err != nil {
					t.Error(err)
				}
			}
			if tc.race != "" && tc.race != "exchange" {
				store.beforeSave = func(candidate *cliproxyauth.Auth) {
					if candidate.CodexRefreshPatch == nil {
						return
					}
					store.beforeSave = nil
					if tc.race == "remove" {
						if err := os.Remove(path); err != nil {
							t.Error(err)
						}
					} else {
						writePeer()
					}
				}
			}
			calls := 0
			engine.refreshToken = func(context.Context, *cliproxyauth.Auth) (*codexauth.CodexTokenData, error) {
				calls++
				if tc.race == "exchange" {
					writePeer()
				}
				if tc.failure != "" {
					return nil, errors.New(tc.failure)
				}
				return &codexauth.CodexTokenData{AccessToken: "minted-fake", RefreshToken: "minted-refresh-fake", Expire: now.Add(time.Hour).Format(time.RFC3339Nano)}, nil
			}
			result, err := manager.ForceRefreshAuth(context.Background(), base.ID)
			if (err == nil) != tc.success {
				t.Fatalf("refresh success=%v; want=%v: %v", err == nil, tc.success, err)
			}
			current, ok := manager.GetByID(base.ID)
			if !ok || current.Disabled != tc.disabled {
				t.Fatalf("published disabled=%v; want=%v", current != nil && current.Disabled, tc.disabled)
			}
			if tc.success {
				if result == nil || current.Metadata["access_token"] != peer.Metadata["access_token"] || current.Metadata["expired"] != peer.Metadata["expired"] {
					t.Fatal("winning login/edit did not reach the manager and caller")
				}
			}
			if tc.quota && (!current.Unavailable || !current.Quota.Exceeded || !current.Quota.NextRecoverAt.Equal(initial.Quota.NextRecoverAt)) {
				t.Fatal("lost disable CAS erased the winner's independent quota cooldown")
			}
			raw, errRead := os.ReadFile(path)
			if tc.race == "remove" {
				if !os.IsNotExist(errRead) || current.Metadata["access_token"] != base.Metadata["access_token"] {
					t.Fatal("removed credential resurrected or unpersisted state was published")
				}
				return
			}
			if errRead != nil {
				t.Fatal(errRead)
			}
			var disk map[string]any
			if err := json.Unmarshal(raw, &disk); err != nil {
				t.Fatal(err)
			}
			disabled, _ := disk["disabled"].(bool)
			if disabled != tc.disabled {
				t.Fatalf("durable disabled=%v; want=%v", disabled, tc.disabled)
			}
			if tc.disabled {
				// A disabled credential carries no cooldown: the manager clears it on disable.
				if current.Status != cliproxyauth.StatusDisabled || !current.NextRefreshAfter.IsZero() || current.LastError == nil {
					t.Fatalf("definitive failure was published as a retryable cooldown: status=%v nextRefresh=%v lastError=%v", current.Status, current.NextRefreshAfter, current.LastError)
				}
				if _, err := manager.ForceRefreshAuth(context.Background(), base.ID); err == nil || calls != 1 {
					t.Fatal("disabled grant was retried without a new login")
				}
			}
		})
	}
}
