package executor

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
)

func TestXaiNeoPiError_MarksOnlyResponses404(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			err := xaiNeoPiError(&openaiwire.HTTPError{Status: status, Body: []byte(`{"error":{"message":"upstream failure"}}`)})
			var marker interface{ NeoPiResponsesNotFound() bool }
			marked := errors.As(fmt.Errorf("wrapped: %w", err), &marker) && marker.NeoPiResponsesNotFound()
			if marked != (status == http.StatusNotFound) {
				t.Fatalf("status %d: transport marker = %v", status, marked)
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(err, &statusErr) || statusErr.StatusCode() != status {
				t.Fatalf("mapped error lost HTTP status %d: %v", status, err)
			}
		})
	}
}
