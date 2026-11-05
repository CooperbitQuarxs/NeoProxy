package openaiwire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type watchdogDelayedReader struct {
	ctx    context.Context
	reader io.Reader
	delay  time.Duration
}

func (r *watchdogDelayedReader) Read(p []byte) (int, error) {
	if r.delay > 0 {
		time.Sleep(r.delay)
		r.delay = 0
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func TestWatchdogDisarmsOnRetryableHeaders(t *testing.T) {
	for _, phase := range []string{"body", "backoff"} {
		for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(phase+"/"+http.StatusText(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					attempts, waits, events := 0, 0, 0
					turn := &Turn{
						Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}},
						Do: func(request *http.Request) (*http.Response, error) {
							attempts++
							if err := request.Context().Err(); err != nil {
								t.Fatalf("connected attempt canceled before retry: %v", err)
							}
							if attempts == 1 {
								delay := time.Duration(0)
								if phase == "body" {
									delay = firstEventTimeout + time.Second
								}
								return &http.Response{
									StatusCode: status,
									Header:     http.Header{},
									Body: io.NopCloser(&watchdogDelayedReader{
										ctx: request.Context(), reader: strings.NewReader("temporarily unavailable"), delay: delay,
									}),
								}, nil
							}
							return &http.Response{
								StatusCode: http.StatusOK,
								Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{}}\n\n")),
							}, nil
						},
						Wait: func(ctx context.Context, _ time.Duration) error {
							waits++
							if phase == "backoff" {
								time.Sleep(firstEventTimeout + time.Second)
							}
							return ctx.Err()
						},
					}
					err := turn.Run(t.Context(), func([]byte) error { events++; return nil })
					if err != nil || attempts != 2 || waits != 1 || events != 1 {
						t.Fatalf("err = %v, attempts = %d, waits = %d, events = %d", err, attempts, waits, events)
					}
				})
			})
		}
	}
}

func TestWatchdogStillCancelsBeforeHeaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		turn := &Turn{
			Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}},
			Do: func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			},
		}
		start := time.Now()
		_, err := turn.attempt(t.Context(), nil, func([]byte) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want watchdog cancellation", err)
		}
		if elapsed := time.Since(start); elapsed != firstEventTimeout {
			t.Fatalf("watchdog fired after %v, want %v", elapsed, firstEventTimeout)
		}
	})
}

func TestWatchdogDisarmPreservesCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		turn := &Turn{
			Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}},
			Do: func(request *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody}, nil
			},
			Wait: func(ctx context.Context, _ time.Duration) error {
				cancel()
				return ctx.Err()
			},
		}
		err := turn.Run(ctx, func([]byte) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want caller cancellation", err)
		}
	})
}
