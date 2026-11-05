package neopiretry

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type delayedRetryReader struct {
	ctx    context.Context
	reader io.Reader
	delay  time.Duration
}

func (r *delayedRetryReader) Read(p []byte) (int, error) {
	if r.delay > 0 {
		time.Sleep(r.delay)
		r.delay = 0
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func TestDoDoesNotImposeResponseOrBackoffDeadline(t *testing.T) {
	for _, phase := range []string{"headers", "body", "backoff"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts, waits := 0, 0
				ctx := t.Context()
				response, err := Do(ctx, func(attempt int) (*http.Response, error) {
					attempts++
					if attempt == 0 {
						if phase == "headers" {
							time.Sleep(301 * time.Second)
							if err := ctx.Err(); err != nil {
								return nil, err
							}
						}
						delay := time.Duration(0)
						if phase == "body" {
							delay = 301 * time.Second
						}
						return &http.Response{
							StatusCode: http.StatusServiceUnavailable,
							Body: io.NopCloser(&delayedRetryReader{
								ctx: ctx, reader: strings.NewReader("temporarily unavailable"), delay: delay,
							}),
						}, nil
					}
					return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
				}, Options{
					MaxAttempts: 2,
					Wait: func(waitCtx context.Context, _ time.Duration) error {
						waits++
						if waitCtx != ctx {
							t.Fatal("retry replaced the caller context")
						}
						if phase == "backoff" {
							time.Sleep(301 * time.Second)
						}
						return waitCtx.Err()
					},
				})
				if err != nil || response == nil || response.StatusCode != http.StatusOK {
					t.Fatalf("response = %v, err = %v", response, err)
				}
				if attempts != 2 || waits != 1 {
					t.Fatalf("attempts = %d, waits = %d", attempts, waits)
				}
			})
		})
	}
}
