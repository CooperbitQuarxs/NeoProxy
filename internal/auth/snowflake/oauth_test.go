package snowflake

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const testAccount = "https://myorg-myaccount.snowflakecomputing.com"

// fakeToken answers every request with status and body, recording the last
// request; no request leaves the process.
type fakeToken struct {
	status int
	body   string
	header http.Header
	calls  int
	last   *http.Request
	form   url.Values
}

func (f *fakeToken) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	f.last = r
	raw, _ := io.ReadAll(r.Body)
	f.form, _ = url.ParseQuery(string(raw))
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	header := f.header
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(f.body)), Request: r}, nil
}

func fixedClock(t *testing.T) time.Time {
	t.Helper()
	at := time.UnixMilli(1_800_000_000_000)
	previous := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = previous })
	return at
}

// The sign-in binds PKCE, state, the root loopback redirect and the account,
// and exchanges the code for a credential with its real expiry.
func TestSignInBindsPKCEStateRedirectAndAccount(t *testing.T) {
	at := fixedClock(t)
	session, err := NewSession("myorg-myaccount", CallbackPort)
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(session.AuthURL())
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	if got := authorize.Scheme + "://" + authorize.Host; got != testAccount || authorize.Path != "/oauth/authorize" {
		t.Fatalf("authorize = %s", authorize)
	}
	for key, want := range map[string]string{"client_id": "LOCAL_APPLICATION", "scope": "refresh_token", "response_type": "code", "code_challenge_method": "S256", "redirect_uri": "http://127.0.0.1:54551/", "state": session.State} {
		if query.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, query.Get(key), want)
		}
	}
	fake := &fakeToken{body: `{"access_token":"a","refresh_token":"r","expires_in":600}`}
	tokens, err := session.Exchange(context.Background(), &http.Client{Transport: fake}, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if fake.last.URL.String() != testAccount+"/oauth/token-request" || fake.last.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("token request = %s %v", fake.last.URL, fake.last.Header)
	}
	for key, want := range map[string]string{"grant_type": "authorization_code", "code": "abc", "client_id": "LOCAL_APPLICATION", "redirect_uri": query.Get("redirect_uri")} {
		if fake.form.Get(key) != want {
			t.Errorf("form %s = %q, want %q", key, fake.form.Get(key), want)
		}
	}
	if fake.form.Has("client_secret") {
		t.Error("form carries a client_secret")
	}
	digest := sha256.Sum256([]byte(fake.form.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(digest[:]) != query.Get("code_challenge") {
		t.Error("code_verifier does not match the code_challenge")
	}
	if tokens.Access != "a" || tokens.Refresh != "r" || !tokens.Expires.Equal(at.Add(600*time.Second)) {
		t.Fatalf("tokens = %+v", tokens)
	}
}

// A sign-in without a refresh grant keeps the real expiry and an empty refresh.
func TestSignInWithoutRefreshGrant(t *testing.T) {
	at := fixedClock(t)
	session := &Session{AccountURL: testAccount, RedirectURI: "http://127.0.0.1:54551/"}
	tokens, err := session.Exchange(context.Background(), &http.Client{Transport: &fakeToken{body: `{"access_token":"a","expires_in":60}`}}, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.Refresh != "" || !tokens.Expires.Equal(at.Add(time.Minute)) {
		t.Fatalf("tokens = %+v", tokens)
	}
}

// Token request failures carry the status but never the server's answer,
// which may reflect the authorization code or a token.
func TestTokenErrorsOmitServerAnswer(t *testing.T) {
	session := &Session{AccountURL: testAccount}
	_, err := session.Exchange(context.Background(), &http.Client{Transport: &fakeToken{status: 400, body: `{"error_description":"sentinel-secret"}`}}, "abc")
	var oauthErr *OAuthError
	if !errors.As(err, &oauthErr) || oauthErr.Status != 400 || err.Error() != "Snowflake token exchange failed (400)" {
		t.Fatalf("error = %v", err)
	}
	_, err = Refresh(context.Background(), &http.Client{Transport: &fakeToken{status: 400, body: `{"error":"invalid_grant","error_description":"sentinel-secret"}`}}, testAccount, Tokens{Refresh: "old"})
	if !errors.As(err, &oauthErr) || oauthErr.Status != 400 || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("refresh error = %v", err)
	}
}

// A token without a refresh grant stays usable until it expires, then needs a
// new sign-in; neither case touches the network.
func TestRefreshlessToken(t *testing.T) {
	at := fixedClock(t)
	fake := &fakeToken{}
	client := &http.Client{Transport: fake}
	valid := Tokens{Access: "still-valid", Expires: at.Add(30 * time.Second)}
	if got, err := Refresh(context.Background(), client, testAccount, valid); err != nil || got != valid {
		t.Fatalf("valid refreshless token = %+v, %v", got, err)
	}
	if _, err := Refresh(context.Background(), client, testAccount, Tokens{Access: "old", Expires: at.Add(-time.Second)}); err == nil {
		t.Fatal("expired refreshless token refreshed")
	}
	if fake.calls != 0 {
		t.Fatalf("network used %d times", fake.calls)
	}
}

// Refresh keeps the old grant unless a new one is issued.
func TestRefreshRetainsOrRotatesGrant(t *testing.T) {
	at := fixedClock(t)
	for _, rotated := range []string{"", "rotated-refresh"} {
		body := `{"access_token":"new-access","expires_in":60`
		if rotated != "" {
			body += `,"refresh_token":"` + rotated + `"`
		}
		fake := &fakeToken{body: body + "}"}
		tokens, err := Refresh(context.Background(), &http.Client{Transport: fake}, testAccount, Tokens{Access: "old-access", Refresh: "old-refresh"})
		if err != nil {
			t.Fatal(err)
		}
		want := rotated
		if want == "" {
			want = "old-refresh"
		}
		if tokens.Access != "new-access" || tokens.Refresh != want || !tokens.Expires.Equal(at.Add(time.Minute)) {
			t.Errorf("tokens = %+v", tokens)
		}
		if fake.form.Get("grant_type") != "refresh_token" || fake.form.Get("refresh_token") != "old-refresh" || fake.form.Get("client_id") != "LOCAL_APPLICATION" {
			t.Errorf("form = %v", fake.form)
		}
	}
}

// A refresh for a missing or foreign account fails before any request.
func TestRefreshRejectsAccountBeforeNetwork(t *testing.T) {
	for _, account := range []string{"", "https://evil.test"} {
		fake := &fakeToken{}
		if _, err := Refresh(context.Background(), &http.Client{Transport: fake}, account, Tokens{Refresh: "old"}); err == nil {
			t.Errorf("%q: refreshed", account)
		}
		if fake.calls != 0 {
			t.Errorf("%q: network used", account)
		}
	}
}

// Malformed token answers are refused, as NeoPi's parseTokenResponse refuses them.
func TestInvalidTokenAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"invalid JSON":          "not-json",
		"null":                  "null",
		"empty access":          `{"access_token":"","expires_in":600}`,
		"non-string access":     `{"access_token":1,"expires_in":600}`,
		"non-numeric lifetime":  `{"access_token":"a","expires_in":"600"}`,
		"non-positive lifetime": `{"access_token":"a","expires_in":0}`,
		"overflow lifetime":     `{"access_token":"a","expires_in":1e308}`,
		"invalid refresh":       `{"access_token":"a","expires_in":600,"refresh_token":null}`,
	} {
		_, err := Refresh(context.Background(), &http.Client{Transport: &fakeToken{body: body}}, testAccount, Tokens{Refresh: "old"})
		var oauthErr *OAuthError
		if !errors.As(err, &oauthErr) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
}

// Token requests never follow a redirect (NeoPi's redirect: "error").
func TestTokenRequestRefusesRedirect(t *testing.T) {
	fake := &fakeToken{status: http.StatusFound, header: http.Header{"Location": {"https://evil.test/collect"}}}
	if _, err := Refresh(context.Background(), &http.Client{Transport: fake}, testAccount, Tokens{Refresh: "old"}); err == nil {
		t.Fatal("redirect followed")
	}
	if fake.calls != 1 {
		t.Fatalf("requests = %d", fake.calls)
	}
}
