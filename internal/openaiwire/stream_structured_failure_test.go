package openaiwire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStructuredInBandClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		frame   string
		status  int
		message string
	}{
		{"nested status", `{"type":"error","error":{"status":429,"message":"Try again"}}`, 429, "429 Try again (error)"},
		{"string status", `{"error":{"status":"503","message":"Backend offline"}}`, 503, "503 Backend offline"},
		{"numeric code", `{"code":502,"message":"Backend offline"}`, 502, "502 Backend offline"},
		{"numeric integer representation", `{"error":{"status":5.03e2,"message":"Backend offline"}}`, 503, "503 Backend offline"},
		{"named code", `{"error":{"type":"rate_limit_error"}}`, 429, "429 Provider returned an in-band provider error (rate_limit_error)"},
		{"camel code", `{"error":{"code":"ThrottlingAllocationQuota"}}`, 429, "429 Provider returned an in-band provider error (throttling_allocation_quota)"},
		{"dotted code", `{"error":{"code":"Throttling.AllocationQuota"}}`, 429, "429 Provider returned an in-band provider error (throttling_allocation_quota)"},
		{"standalone type", `{"type":"rate_limit_error"}`, 429, "429 Provider returned an in-band provider error (rate_limit_error)"},
		{"double wrapped", `{"error":{"error":{"code":"server_busy","message":"At capacity"}}}`, 503, "503 At capacity (server_busy)"},
		{"bounded third wrapper", `{"error":{"error":{"error":{"code":"overloaded"}}}}`, 503, "503 Provider returned an in-band provider error (overloaded)"},
		{"failed response", `{"type":"response.failed","response":{"error":{"status":504,"message":"Backend offline"}}}`, 504, "504 Backend offline"},
		{"failed response root status", `{"type":"response.failed","response":{"status":503,"error":{"message":"Backend offline"}}}`, 503, "503 Backend offline"},
		{"statusless transient", `{"type":"error","error":{"code":"temporary","message":"Please retry your request later"}}`, 0, "Please retry your request later (temporary)"},
		{"string envelope", `{"error":"The provider is overloaded"}`, 0, "The provider is overloaded"},
		{"prose never becomes status", `{"error":{"message":"429 Too many requests"}}`, 0, "429 Too many requests"},
		{"nonretryable status not synthesized", `{"error":{"status":401,"message":"Please retry"}}`, 0, "Please retry"},
		{"opaque message", `{"error":{"status":429,"message":"{}"}}`, 429, "429 Provider returned an in-band provider error"},
		{"flattened message", `{"error":{"status":503,"message":"<b>Service</b>\n unavailable"}}`, 503, "503 Service unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := classifyInBandFrame([]byte(tc.frame))
			if failure == nil {
				t.Fatal("failure was not classified")
			}
			if failure.Status != tc.status || failure.Message != tc.message {
				t.Errorf("failure = status %d, %q; want %d, %q", failure.Status, failure.Message, tc.status, tc.message)
			}
			if string(FailureEvent(fmt.Errorf("wrapped: %w", failure))) != tc.frame {
				t.Error("wrapped failure lost its original frame")
			}
		})
	}
}

func TestStructuredInBandNonretryableExceptions(t *testing.T) {
	for _, code := range []string{
		"insufficient_quota", "usage_limit_reached", "quota_exceeded", "quota_reached", "quota_insufficient",
		"invalid_request_error", "content_filter", "context_length_exceeded", "context_window_exceeded", "billing_error", "balance_exhausted",
	} {
		t.Run(code, func(t *testing.T) {
			frame := `{"type":"response.failed","response":{"error":{"status":429,"code":"` + code + `","message":"Please retry"}}}`
			attempts, emitted := 0, 0
			err := scriptedTurn("data: {\"type\":\"response.created\"}\n\ndata: "+frame+"\n\n", &attempts).Run(t.Context(), func([]byte) error { emitted++; return nil })
			var failure *StreamError
			if !errors.As(err, &failure) || attempts != 1 || emitted != 0 {
				t.Fatalf("err = %v, attempts = %d, emitted = %d", err, attempts, emitted)
			}
			if string(FailureEvent(fmt.Errorf("wrapped: %w", err))) != frame {
				t.Error("nonretryable failure lost its terminal frame")
			}
		})
	}
	for _, frame := range []string{
		`{"type":"response.created","response":{"status":"in_progress"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
		`{"error":{"type":"server_error","message":"Backend failed"}}`,
		`{"error":{"status":400,"message":"Invalid request"}}`,
		`{"error":{"status":"503.0","message":"Backend failed"}}`,
		`{"error":{"status":503.5,"message":"Backend failed"}}`,
		`{"error":{"error":{"error":{"error":{"status":503}}}}}`,
	} {
		if failure := classifyInBandFrame([]byte(frame)); failure != nil {
			t.Errorf("classified %s as %v", frame, failure)
		}
	}
}

func TestStructuredFailureRetriesOnlyBeforeReplayUnsafeOutput(t *testing.T) {
	created := `{"type":"response.created","response":{"id":"failed_attempt"}}`
	delta := `{"type":"response.output_text.delta","delta":"hello"}`
	failure := `{"type":"response.failed","response":{"error":{"status":503,"message":"Backend offline"}}}`
	for _, unsafe := range []bool{false, true} {
		t.Run(fmt.Sprint(unsafe), func(t *testing.T) {
			body := "data: " + created + "\n\n"
			if unsafe {
				body += "data: " + delta + "\n\n"
			}
			body += "data: " + failure + "\n\n"
			attempts := 0
			var events []string
			err := scriptedTurn(body, &attempts).Run(t.Context(), func(event []byte) error { events = append(events, string(event)); return nil })
			var inBand *InBandError
			if !errors.As(err, &inBand) || inBand.Status != 503 || string(FailureEvent(err)) != failure {
				t.Fatalf("failure = %v", err)
			}
			wantAttempts, wantEvents := 2, 0
			if unsafe {
				wantAttempts, wantEvents = 1, 2
			}
			if attempts != wantAttempts || len(events) != wantEvents {
				t.Errorf("attempts = %d, events = %q; want %d attempts, %d events", attempts, events, wantAttempts, wantEvents)
			}
		})
	}
}

func TestStructuredRetryDiscardsFailedAttemptEvents(t *testing.T) {
	attempts := 0
	turn := &Turn{
		Request: &Request{URL: "http://upstream.invalid/responses", Header: http.Header{}},
		Wait:    func(context.Context, time.Duration) error { return nil },
		Do: func(*http.Request) (*http.Response, error) {
			attempts++
			body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"failed_attempt\"}}\n\ndata: {\"error\":{\"code\":\"server_busy\"}}\n\n"
			if attempts == 2 {
				body = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"successful_attempt\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		},
	}
	var events []string
	err := turn.Run(t.Context(), func(event []byte) error { events = append(events, string(event)); return nil })
	if err != nil || attempts != 2 || len(events) != 2 {
		t.Fatalf("err = %v, attempts = %d, events = %q", err, attempts, events)
	}
	if strings.Contains(strings.Join(events, ""), "failed_attempt") {
		t.Errorf("failed attempt escaped buffering: %q", events)
	}
}

func TestFlatNonretryableFailureDoesNotBecomeIncompleteStream(t *testing.T) {
	for _, frame := range []string{
		`{"code":"invalid_request_error","message":"Please retry"}`,
		`{"type":"insufficient_quota"}`,
		`{"error":{"code":"billing_error","status":503,"message":"Capacity unavailable"}}`,
		`{"status":401,"message":"Authentication failed"}`,
	} {
		attempts, emitted := 0, 0
		err := scriptedTurn("data: {\"type\":\"response.created\"}\n\ndata: "+frame+"\n\n", &attempts).Run(t.Context(), func([]byte) error { emitted++; return nil })
		var failure *StreamError
		if !errors.As(err, &failure) || IsIncompleteStream(err) || attempts != 1 || emitted != 0 {
			t.Errorf("frame = %s, err = %v, attempts = %d, emitted = %d", frame, err, attempts, emitted)
		}
		if string(FailureEvent(err)) != frame {
			t.Errorf("failure lost frame %s", frame)
		}
	}
}
