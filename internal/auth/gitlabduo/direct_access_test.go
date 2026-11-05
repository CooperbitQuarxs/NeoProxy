package gitlabduo

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type mintCounter struct{ tokens []string }

func (m *mintCounter) RoundTrip(req *http.Request) (*http.Response, error) {
	m.tokens = append(m.tokens, req.Header.Get("Authorization"))
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Request: req,
		Body: io.NopCloser(strings.NewReader(`{"token":"direct","headers":{"x-gitlab-realm":"saas"}}`))}, nil
}

// NeoPi's direct-access cache (getDirectAccessToken): a token minted for a
// GitLab token serves that token for 25 minutes and is minted again after;
// another GitLab token has its own; a sign-in or refresh drops them all.
func TestDirectAccessCacheMatchesNeoPi(t *testing.T) {
	ClearDirectAccessCache()
	defer ClearDirectAccessCache()
	start := time.UnixMilli(1_800_000_000_000)
	clock := start
	defer func(restore func() time.Time) { now = restore }(now)
	now = func() time.Time { return clock }
	minter := &mintCounter{}
	client := &http.Client{Transport: minter}
	get := func(token string) {
		t.Helper()
		access, err := GetDirectAccess(context.Background(), client, token)
		if err != nil {
			t.Fatal(err)
		}
		if access.Token != "direct" || access.Headers.Get("X-Gitlab-Realm") != "saas" {
			t.Fatalf("direct access = %+v", access)
		}
	}
	get("a")
	clock = start.Add(directAccessTTL - time.Millisecond)
	get("a")
	if len(minter.tokens) != 1 {
		t.Fatalf("minted %d times within the TTL, want 1", len(minter.tokens))
	}
	get("b")
	if len(minter.tokens) != 2 || minter.tokens[1] != "Bearer b" {
		t.Fatalf("a second GitLab token minted %v, want its own mint", minter.tokens)
	}
	clock = start.Add(directAccessTTL + time.Millisecond)
	get("a")
	if len(minter.tokens) != 3 {
		t.Fatalf("minted %d times after the TTL, want 3", len(minter.tokens))
	}
	ClearDirectAccessCache()
	get("a")
	if len(minter.tokens) != 4 {
		t.Fatalf("minted %d times after a clear, want 4", len(minter.tokens))
	}
}
