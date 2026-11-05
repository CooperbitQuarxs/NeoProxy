package neopiretry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type responseHookReader struct {
	t        *testing.T
	reader   io.Reader
	notified *bool
}

func (r *responseHookReader) Read(p []byte) (int, error) {
	if !*r.notified {
		r.t.Fatal("retryable response body read before OnResponse")
	}
	return r.reader.Read(p)
}

func TestDoNotifiesResponseBeforeBodyAndBackoff(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			attempts, notifications, waits := 0, 0, 0
			notified := false
			response, err := Do(t.Context(), func(attempt int) (*http.Response, error) {
				attempts++
				notified = false
				if attempt == 0 {
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{},
						Body: io.NopCloser(&responseHookReader{
							t: t, reader: strings.NewReader("temporarily unavailable"), notified: &notified,
						}),
					}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
			}, Options{
				MaxAttempts: 2,
				OnResponse: func(response *http.Response) {
					notifications++
					notified = true
					if response.Body == nil {
						t.Fatal("OnResponse did not receive the HTTP response")
					}
				},
				ShouldRetry: func(*http.Response, string) bool {
					if !notified {
						t.Fatal("ShouldRetry called before OnResponse")
					}
					return true
				},
				Wait: func(context.Context, time.Duration) error {
					waits++
					if !notified {
						t.Fatal("retry backoff started before OnResponse")
					}
					return nil
				},
			})
			if err != nil || response == nil || response.StatusCode != http.StatusOK {
				t.Fatalf("response = %v, err = %v", response, err)
			}
			if attempts != 2 || notifications != 2 || waits != 1 {
				t.Fatalf("attempts = %d, notifications = %d, waits = %d", attempts, notifications, waits)
			}
		})
	}
}

func TestDoNotifiesTerminalResponse(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			want := &http.Response{StatusCode: status, Body: http.NoBody}
			var notified *http.Response
			response, err := Do(t.Context(), func(int) (*http.Response, error) {
				return want, nil
			}, Options{MaxAttempts: 1, OnResponse: func(response *http.Response) { notified = response }})
			if err != nil || response != want || notified != want {
				t.Fatalf("response = %v, notification = %v, err = %v", response, notified, err)
			}
		})
	}
}

func TestDoDoesNotNotifyNetworkFailure(t *testing.T) {
	wantErr := errors.New("connection reset")
	_, err := Do(t.Context(), func(int) (*http.Response, error) {
		return nil, wantErr
	}, Options{
		MaxAttempts: 1,
		OnResponse:  func(*http.Response) { t.Fatal("OnResponse called without headers") },
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
