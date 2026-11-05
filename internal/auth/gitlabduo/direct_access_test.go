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
	session := Duo.newSession("http://localhost:8080/callback", "state", []byte("verifier"))
	if credential, err := session.Exchange(context.Background(), &http.Client{Transport: token}, "code"); err != nil || credential.Access != "access" {
		t.Fatalf("exchange = %+v, %v", credential, err)
	}
	if credential, err := Duo.Refresh(context.Background(), &http.Client{Transport: token}, "stored"); err != nil || credential.Access != "access" {
		t.Fatalf("refresh = %+v, %v", credential, err)
	}
	mint := gzipAnswer{`{"token":"direct","headers":{"x-gitlab-realm":"saas"}}`}
	access, err := GetDirectAccess(context.Background(), &http.Client{Transport: mint}, "gitlab")
	if err != nil || access.Token != "direct" || access.Headers.Get("X-Gitlab-Realm") != "saas" {
		t.Fatalf("direct access = %+v, %v", access, err)
	}
}

// NeoPi spreads the minted headers into a Headers object, so values coerce
// as JavaScript String does and an array container spreads its indices.
func TestGitLabDuoMintedHeadersCoerceAsNeoPi(t *testing.T) {
	for name, tc := range map[string]struct {
		headers string
		want    map[string]string
	}{
		"values":    {`{"object":{"a":1},"array":["a","b",null],"number":7,"flag":true,"none":null}`, map[string]string{"Object": "[object Object]", "Array": "a,b,", "Number": "7", "Flag": "true", "None": "null"}},
		"container": {`["first","second"]`, map[string]string{"0": "first", "1": "second"}},
		"numbers":   {`{"exponent":1e3,"negative-zero":-0,"decimal":1.0,"large":1e21,"small":1.5e-7,"fraction":0.000001,"overflow":1e400,"negative-overflow":-1e400}`, map[string]string{"Exponent": "1000", "Negative-Zero": "0", "Decimal": "1", "Large": "1e+21", "Small": "1.5e-7", "Fraction": "0.000001", "Overflow": "Infinity", "Negative-Overflow": "-Infinity"}},
	} {
		ClearDirectAccessCache()
		access, err := GetDirectAccess(context.Background(), &http.Client{Transport: gzipAnswer{`{"token":"direct","headers":` + tc.headers + `}`}}, "gitlab-"+name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(access.Headers) != len(tc.want) {
			t.Fatalf("%s: headers = %v, NeoPi sends %v", name, access.Headers, tc.want)
		}
		for key, value := range tc.want {
			if got := access.Headers.Get(key); got != value {
				t.Fatalf("%s: header %s = %q, NeoPi sends %q", name, key, got, value)
			}
		}
	}
	ClearDirectAccessCache()
}
