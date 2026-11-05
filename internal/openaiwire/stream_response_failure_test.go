package openaiwire

import (
	"errors"
	"fmt"
	"testing"
)

func TestResponseFailurePreservesSourceServerError(t *testing.T) {
	frame := `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"synthetic server failure"},"usage":{"input_tokens":7,"output_tokens":2}}}`
	for _, unsafe := range []bool{false, true} {
		t.Run(fmt.Sprint(unsafe), func(t *testing.T) {
			body := "data: {\"type\":\"response.created\"}\n\n"
			if unsafe {
				body += "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"
			}
			body += "data: " + frame + "\n\n"
			attempts, emitted := 0, 0
			err := scriptedTurn(body, &attempts).Run(t.Context(), func([]byte) error { emitted++; return nil })
			var streamErr *StreamError
			var inBand *InBandError
			if !errors.As(err, &streamErr) || errors.As(err, &inBand) || err.Error() != "server_error: synthetic server failure" || IsIncompleteStream(err) {
				t.Fatalf("failure = %v; want source-formatted statusless stream failure", err)
			}
			wantAttempts, wantEmitted := 2, 0
			if unsafe {
				wantAttempts, wantEmitted = 1, 2
			}
			if attempts != wantAttempts || emitted != wantEmitted {
				t.Errorf("attempts = %d, emitted = %d; want %d, %d", attempts, emitted, wantAttempts, wantEmitted)
			}
			if string(FailureEvent(fmt.Errorf("wrapped: %w", err))) != frame {
				t.Error("failure lost its original frame and usage")
			}
		})
	}
}

func TestResponseFailureTerminalAliases(t *testing.T) {
	for _, kind := range []string{"response.failed", "response.completed", "response.incomplete", "response.done"} {
		for _, status := range []string{"failed", "cancelled"} {
			for _, tc := range []struct {
				name     string
				details  string
				message  string
				attempts int
				status   int
			}{
				{"status details fallback", `"error":null,"status_details":{"error":{"code":"rate_limit_exceeded","message":"synthetic rate limit"}}`, "429 synthetic rate limit (rate_limit_exceeded)", 2, 429},
				{"primary error wins", `"error":{"code":"invalid_request_error","message":"synthetic invalid request"},"status_details":{"error":{"code":"rate_limit_exceeded","message":"ignored"}}`, "invalid_request_error: synthetic invalid request", 1, 0},
				{"statusless source error", `"status_details":{"error":{"code":"server_error","message":"synthetic server failure"}}`, "server_error: synthetic server failure", 2, 0},
				{"incomplete details bypass throttle probe", `"error":{"code":"rate_limit_exceeded","message":"synthetic rate limit"},"incomplete_details":{"reason":"content_filter"}`, "rate_limit_exceeded: synthetic rate limit", 2, 0},
				{"incomplete reason", `"incomplete_details":{"reason":"content_filter"}`, "incomplete: content_filter", 1, 0},
			} {
				t.Run(kind+"/"+status+"/"+tc.name, func(t *testing.T) {
					frame := fmt.Sprintf(`{"type":%q,"response":{"status":%q,%s,"usage":{"input_tokens":7,"output_tokens":2}}}`, kind, status, tc.details)
					attempts, emitted := 0, 0
					err := scriptedTurn("data: "+frame+"\n\n", &attempts).Run(t.Context(), func([]byte) error { emitted++; return nil })
					if err == nil || err.Error() != tc.message || attempts != tc.attempts || emitted != 0 || IsIncompleteStream(err) {
						t.Fatalf("failure = %v, attempts = %d, emitted = %d; want %q, %d, 0", err, attempts, emitted, tc.message, tc.attempts)
					}
					var inBand *InBandError
					if tc.status != 0 && (!errors.As(err, &inBand) || inBand.Status != tc.status) {
						t.Errorf("failure = %v; want in-band status %d", err, tc.status)
					}
					if string(FailureEvent(err)) != frame {
						t.Error("failure lost its original frame and usage")
					}
				})
			}
		}
	}
}

func TestResponseIncompleteReasonDistinguishesFilterFromLength(t *testing.T) {
	for _, kind := range []string{"response.completed", "response.incomplete", "response.done"} {
		for _, reason := range []string{"content_filter", "max_output_tokens"} {
			t.Run(kind+"/"+reason, func(t *testing.T) {
				frame := fmt.Sprintf(`{"type":%q,"response":{"status":"incomplete","incomplete_details":{"reason":%q},"usage":{"input_tokens":7,"output_tokens":2}}}`, kind, reason)
				attempts, emitted := 0, 0
				err := scriptedTurn("data: "+frame+"\n\n", &attempts).Run(t.Context(), func([]byte) error { emitted++; return nil })
				if reason == "max_output_tokens" {
					if err != nil || attempts != 1 || emitted != 1 {
						t.Fatalf("length completion = %v, attempts = %d, emitted = %d", err, attempts, emitted)
					}
					return
				}
				if err == nil || err.Error() != "incomplete: content_filter" || attempts != 1 || emitted != 0 || string(FailureEvent(err)) != frame {
					t.Fatalf("filter failure = %v, attempts = %d, emitted = %d", err, attempts, emitted)
				}
			})
		}
	}
}

func TestInBandQuotaRetainsStatusWithoutLocalRetry(t *testing.T) {
	for _, message := range []string{
		"subscription quota exhausted",
		"account rate limit reached",
		"Monthly spending cap exceeded",
		"Insufficient credits; please retry",
		"credential-scope refusal: server error",
	} {
		t.Run(message, func(t *testing.T) {
			frame := fmt.Sprintf(`{"type":"error","error":{"code":429,"message":%q,"resets_at":1793900857}}`, message)
			attempts, emitted := 0, 0
			err := scriptedTurn("data: "+frame+"\n\n", &attempts).Run(t.Context(), func([]byte) error { emitted++; return nil })
			var inBand *InBandError
			if !errors.As(err, &inBand) || inBand.Status != 429 || inBand.Message != "429 "+message || attempts != 1 || emitted != 0 {
				t.Fatalf("failure = %v, attempts = %d, emitted = %d", err, attempts, emitted)
			}
			if string(FailureEvent(err)) != frame {
				t.Error("quota failure lost original event")
			}
		})
	}
	for _, message := range []string{"subscription rate limit per minute", "Concurrent requests quota exceeded"} {
		t.Run(message, func(t *testing.T) {
			frame := fmt.Sprintf(`{"type":"error","error":{"status":429,"message":%q}}`, message)
			attempts := 0
			err := scriptedTurn("data: "+frame+"\n\n", &attempts).Run(t.Context(), func([]byte) error { return nil })
			if err == nil || attempts != 2 {
				t.Fatalf("transient failure = %v, attempts = %d; want one retry", err, attempts)
			}
		})
	}
}
