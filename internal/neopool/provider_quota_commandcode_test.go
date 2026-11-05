package neopool

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/commandcode"
)

// A Command Code report gates routing as NeoPi's rotation reads it: an
// exceeded window is exhausted whatever its fraction, a window without a cap
// is reported but unknown, and an empty credit balance exhausts the account
// while a positive one adds no meter.
func TestCommandCodeUsageWindowsGateLikeNeoPi(t *testing.T) {
	low, high := 0.2, 0.5
	resets := time.UnixMilli(1793854800000)
	usage := &commandcode.Usage{Balance: 0, Limits: []commandcode.UsageLimit{
		{ID: "commandcode:5h", WindowID: "5h", DurationMs: int64(5 * time.Hour / time.Millisecond), ResetsAt: resets, UsedFraction: &low, Status: "exhausted"},
		{ID: "commandcode:7d", WindowID: "7d", DurationMs: int64(7 * 24 * time.Hour / time.Millisecond), UsedFraction: &high, Status: "ok"},
	}}
	windows := commandCodeUsageWindows(usage)
	if len(windows) != 3 {
		t.Fatalf("windows = %+v, want 5h, week and the empty balance", windows)
	}
	if w := windows[0]; w.Kind != "5h" || !w.Known || w.Used != 1 || w.ResetsAt != resets.UnixMilli() || w.StartsAt != resets.Add(-5*time.Hour).UnixMilli() {
		t.Errorf("exceeded 5h window = %+v, want exhausted until its reset", w)
	}
	if w := windows[1]; w.Kind != "week" || !w.Known || w.Used != 0.5 {
		t.Errorf("weekly window = %+v, want half used", w)
	}
	if w := windows[2]; w.Kind != "month" || !w.Known || w.Used != 1 {
		t.Errorf("balance window = %+v, want an exhausted gate", w)
	}

	usage = &commandcode.Usage{Balance: 12, Limits: []commandcode.UsageLimit{{ID: "commandcode:5h", WindowID: "5h", Status: "unknown"}}}
	windows = commandCodeUsageWindows(usage)
	if len(windows) != 1 || windows[0].Kind != "5h" || windows[0].Known {
		t.Fatalf("windows = %+v, want one unknown 5h window and no balance meter", windows)
	}
}
