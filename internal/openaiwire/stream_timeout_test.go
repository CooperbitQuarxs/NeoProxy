package openaiwire

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// This exceeds the former watchdog without introducing wall-clock delays.
const slowResponseDelay = 301 * time.Second

const completedStreamBody = "data: {\"type\":\"response.completed\",\"response\":{}}\n\n"

type slowResponseReader struct {
	ctx    context.Context
	reader io.Reader
	delay  time.Duration
}

func (r *slowResponseReader) Read(p []byte) (int, error) {
	if r.delay > 0 {
		time.Sleep(r.delay)
		r.delay = 0
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func TestTurnDoesNotImposeResponseTimeout(t *testing.T) {
	for _, phase := range []string{"headers", "body", "backoff"} {
		for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(phase+"/"+http.StatusText(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					attempts, waits, events := 0, 0, 0
					turn := &Turn{
						Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}},
						Do: func(request *http.Request) (*http.Response, error) {
							attempts++
							if _, ok := request.Context().Deadline(); ok {
								t.Fatal("turn imposed a request deadline")
							}
							if attempts == 1 {
								if phase == "headers" {
									time.Sleep(slowResponseDelay)
									if err := request.Context().Err(); err != nil {
										return nil, err
									}
								}
								delay := time.Duration(0)
								if phase == "body" {
									delay = slowResponseDelay
								}
								return &http.Response{
									StatusCode: status,
									Header:     http.Header{},
									Body: io.NopCloser(&slowResponseReader{
										ctx: request.Context(), reader: strings.NewReader("temporarily unavailable"), delay: delay,
									}),
								}, nil
							}
							return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(completedStreamBody))}, nil
						},
						Wait: func(ctx context.Context, _ time.Duration) error {
							waits++
							if phase == "backoff" {
								time.Sleep(slowResponseDelay)
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

func TestTurnAllowsConnectedTransportToWaitForHeaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// An in-memory connection gives the HTTP transport an established
		// connection while allowing its server's header delay to use virtual time.
		transport := &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				client, server := net.Pipe()
				go func() {
					defer func() { _ = server.Close() }()
					request, err := http.ReadRequest(bufio.NewReader(server))
					if err != nil {
						t.Errorf("reading request: %v", err)
						return
					}
					_ = request.Body.Close()
					time.Sleep(slowResponseDelay)
					_, err = fmt.Fprintf(server, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(completedStreamBody), completedStreamBody)
					if err != nil {
						t.Errorf("writing delayed response: %v", err)
					}
				}()
				return client, nil
			},
		}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport}
		turn := &Turn{
			Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}},
			Do:      client.Do,
		}
		events := 0
		start := time.Now()
		err := turn.Run(t.Context(), func([]byte) error { events++; return nil })
		if err != nil || events != 1 {
			t.Fatalf("err = %v, events = %d", err, events)
		}
		if elapsed := time.Since(start); elapsed != slowResponseDelay {
			t.Fatalf("elapsed = %v, want %v", elapsed, slowResponseDelay)
		}
	})
}

func TestTurnPreservesCallerCancellation(t *testing.T) {
	for _, phase := range []string{"headers", "backoff"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			turn := &Turn{
				Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}},
				Do: func(request *http.Request) (*http.Response, error) {
					if phase == "headers" {
						cancel()
						return nil, request.Context().Err()
					}
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
}
