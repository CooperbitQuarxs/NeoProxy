package openaiwire

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// The Responses X-Stainless-Timeout is NeoPi's first-event timeout in
// seconds (openai-responses.ts, getOpenAIStreamFirstEventTimeoutMs): each
// timeout falls back to 300 s only when the row's compat leaves it out, the
// larger of the two is announced, and a disabled first-event timeout sends
// none.
func TestFirstEventTimeoutHeaderFollowsCompat(t *testing.T) {
	ms := func(value int) *int { return &value }
	for _, tc := range []struct {
		name        string
		first, idle *int
		want        string
	}{
		{"defaults", nil, nil, "300"},
		{"both shorter", ms(60_000), ms(60_000), "60"},
		{"short first, default idle", ms(60_000), nil, "300"},
		{"short first, idle disabled", ms(60_000), ms(0), "60"},
		{"longer idle", ms(60_000), ms(600_000), "600"},
		{"first disabled", ms(0), ms(300_000), ""},
		{"partial seconds", ms(90_500), ms(1_000), "90"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compat := &registry.NeoPiCompat{StreamFirstEventTimeoutMs: tc.first, StreamIdleTimeoutMs: tc.idle}
			if got := firstEventTimeoutHeader(compat); got != tc.want {
				t.Fatalf("X-Stainless-Timeout = %q, want %q", got, tc.want)
			}
		})
	}
}
