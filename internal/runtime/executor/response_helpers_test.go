package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func assertStatusErr(t *testing.T, err error, want int) {
	t.Helper()
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != want {
		t.Fatalf("error = %v, want HTTP %d", err, want)
	}
}

func claudeCompactionCompletedResponse(t *testing.T, stream string) gjson.Result {
	t.Helper()
	for _, line := range strings.Split(stream, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		value := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		if value.Get("type").String() == "response.completed" {
			return value
		}
	}
	t.Fatalf("missing response.completed event in %s", stream)
	return gjson.Result{}
}
