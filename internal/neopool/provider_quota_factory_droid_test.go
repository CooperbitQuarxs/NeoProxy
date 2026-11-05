package neopool

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Factory Droid's billing limits become pool windows scoped to the models
// each pool bills: Standard credits gate Claude, Droid Core gates Inkling,
// a lapsed window reads as unused, and the extra balance gates nothing.
func TestFactoryDroidUsageWindows(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	payload := []byte(`{"limits":{` +
		`"standard":{"fiveHour":{"usedPercent":12.5,"windowEnd":"2026-11-05T14:00:00Z"},"weekly":{"usedPercent":100,"windowEnd":"2026-11-04T00:00:00Z"}},` +
		`"core":{"weekly":{"usedPercent":77,"windowEnd":"2026-11-07T00:00:00Z"}}},"extraUsageBalanceCents":1250}`)
	usage, ok := factorydroid.ParseUsage(payload, now)
	if !ok {
		t.Fatal("no usage parsed")
	}
	windows := factoryDroidUsageWindows(&coreauth.Auth{ID: "factory-pool-canonical", Provider: factorydroid.Provider}, usage)
	if len(windows) != 3 {
		t.Fatalf("windows = %+v, want standard 5h and week, core week", windows)
	}
	for _, tc := range []struct {
		index       int
		kind        string
		used        float64
		resetsAt    time.Time
		gates, free string
	}{
		{0, "5h", .125, time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC), "factory-droid/claude-fable-5.1", "factory-droid/inkling"},
		{1, "week", 0, time.Time{}, "factory-droid/claude-fable-5.1", "factory-droid/inkling"},
		{2, "week", .77, time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), "factory-droid/inkling", "factory-droid/claude-fable-5.1"},
	} {
		w := windows[tc.index]
		if w.Kind != tc.kind || !w.Known || w.Used != tc.used {
			t.Errorf("window %d = %+v, want %s used %v", tc.index, w, tc.kind, tc.used)
		}
		if want := int64(0); !tc.resetsAt.IsZero() {
			want = tc.resetsAt.UnixMilli()
			if w.ResetsAt != want {
				t.Errorf("window %d resets %d, want %d", tc.index, w.ResetsAt, want)
			}
		} else if w.ResetsAt != want {
			t.Errorf("lapsed window %d resets %d, want none", tc.index, w.ResetsAt)
		}
		if !slices.Contains(w.Models, tc.gates) || slices.Contains(w.Models, tc.free) {
			t.Errorf("window %d gates %v; want %s gated and %s free", tc.index, w.Models, tc.gates, tc.free)
		}
	}
}

// A pool window gates the models the account registered under any name: an
// OAuth alias or a forced prefix renames the inventory id, and the window
// still scopes it by the model the pool bills.
func TestFactoryDroidUsageWindowsCoverRegisteredNames(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	usage, ok := factorydroid.ParseUsage([]byte(`{"limits":{"core":{"weekly":{"usedPercent":100,"windowEnd":"2026-11-07T00:00:00Z"}}}}`), now)
	if !ok {
		t.Fatal("no usage parsed")
	}
	auth := &coreauth.Auth{ID: "factory-pool-aliases", Provider: factorydroid.Provider}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, factorydroid.Provider, []*registry.ModelInfo{
		{ID: "inks", MetadataModelID: "inkling", NeoPi: &registry.NeoPiModel{ID: "inkling", Provider: factorydroid.Provider}},
		{ID: "work/inkling", MetadataModelID: "inkling", NeoPi: &registry.NeoPiModel{ID: "inkling", Provider: factorydroid.Provider}},
		{ID: "fable", MetadataModelID: "claude-fable-5.1", NeoPi: &registry.NeoPiModel{ID: "claude-fable-5.1", Provider: factorydroid.Provider}},
	})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	windows := factoryDroidUsageWindows(auth, usage)
	if len(windows) != 1 {
		t.Fatalf("windows = %+v, want the core week", windows)
	}
	for _, id := range []string{"factory-droid/inks", "factory-droid/work/inkling"} {
		if !slices.Contains(windows[0].Models, id) {
			t.Errorf("core window %v does not gate %s", windows[0].Models, id)
		}
	}
	if slices.Contains(windows[0].Models, "factory-droid/fable") {
		t.Errorf("core window %v gates the Standard-billed fable alias", windows[0].Models)
	}
}

// NeoPi bounds the Factory billing read only by the caller's signal; once
// connected, the probe carries no request or idle deadline (AGENTS.md).
func TestFactoryDroidUsageProbeHasNoDeadline(t *testing.T) {
	clients := &quotaClients{clients: map[string]*http.Client{}}
	client := clients.client(&coreauth.Auth{ID: "factory", Provider: factorydroid.Provider})
	if client.Timeout != 0 {
		t.Errorf("client timeout = %v, want none", client.Timeout)
	}
	if transport, ok := client.Transport.(*http.Transport); !ok || transport.IdleConnTimeout != 0 {
		t.Errorf("transport idle timeout = %v, want none", transport.IdleConnTimeout)
	}
}
