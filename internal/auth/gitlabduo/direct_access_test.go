package gitlabduo

import (
	"bytes"
	"compress/gzip"
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

// gzipAnswer answers every request with body gzip-compressed, as GitLab may
// when the request advertises the encodings NeoPi's fetch does.
type gzipAnswer struct{ body string }

func (g gzipAnswer) RoundTrip(req *http.Request) (*http.Response, error) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = io.WriteString(writer, g.body)
	_ = writer.Close()
	return &http.Response{StatusCode: http.StatusOK, Request: req,
		Header: http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}},
		Body:   io.NopCloser(&compressed)}, nil
}

// NeoPi's fetch decodes a compressed answer to the encodings it advertises;
// the token exchange, the refresh and the direct-access mint read it as JSON.
func TestGitLabDuoReadsCompressedResponses(t *testing.T) {
	ClearDirectAccessCache()
	defer ClearDirectAccessCache()
	t.Setenv("GITLAB_CLIENT_ID", "")
	token := gzipAnswer{`{"access_token":"access","refresh_token":"refresh","expires_in":7200,"created_at":1800000000}`}
	session := &Session{ClientID: ClientID(), RedirectURI: "http://localhost:8080/callback", Verifier: "verifier"}
	if credential, err := session.Exchange(context.Background(), &http.Client{Transport: token}, "code"); err != nil || credential.Access != "access" {
		t.Fatalf("exchange = %+v, %v", credential, err)
	}
	if credential, err := Refresh(context.Background(), &http.Client{Transport: token}, "stored"); err != nil || credential.Access != "access" {
		t.Fatalf("refresh = %+v, %v", credential, err)
	}
	mint := gzipAnswer{`{"token":"direct","headers":{"x-gitlab-realm":"saas"}}`}
	access, err := GetDirectAccess(context.Background(), &http.Client{Transport: mint}, "gitlab")
	if err != nil || access.Token != "direct" || access.Headers.Get("X-Gitlab-Realm") != "saas" {
		t.Fatalf("direct access = %+v, %v", access, err)
	}
}
