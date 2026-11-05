package executor

import (
	"net/http"
	"testing"
	"time"
)

func TestNeoPiCompletionsRetryHints(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		headers http.Header
		body    string
		want    time.Duration
		ok      bool
	}{
		{"none", nil, "nothing", 0, false},
		{"immediate", http.Header{"Retry-After-Ms": {"0"}}, "try again in 1h", 0, true},
		{"header priority", http.Header{"Retry-After": {"1.5"}, "X-Ratelimit-Reset-After": {"4"}}, "try again in 10s", 1500 * time.Millisecond, true},
		{"longest body", nil, "Please retry in 250ms; resets in 2hr 15min", 135 * time.Minute, true},
		{"body immediate", nil, "retry-after-ms=0", 0, true},
		{"future date", http.Header{"Retry-After": {"Thu, 01 Jan 2026 00:00:05 GMT"}}, "", 5 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := neoPiCompletionsRetryHint(tc.headers, []byte(tc.body), now)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
