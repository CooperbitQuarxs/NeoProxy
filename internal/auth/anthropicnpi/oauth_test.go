package anthropicnpi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type captureRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}
type captureTransport struct {
	target *url.URL
	base   http.RoundTripper
	urls   []string
}

func (r *captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.urls = append(r.urls, request.URL.String())
	clone := request.Clone(request.Context())
	clone.Host = request.URL.Host
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return r.base.RoundTrip(clone)
}

func TestClaudeOAuthCaptureParity(t *testing.T) {
	data, err := os.ReadFile("testdata/oauth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		AuthURL          string           `json:"authURL"`
		Requests         []captureRequest `json:"requests"`
		LegacyCredential map[string]any   `json:"legacyCredential"`
		LegacyRequests   []captureRequest `json:"legacyRequests"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var exchange map[string]string
	if err = json.Unmarshal(fixture.Requests[0].Body, &exchange); err != nil {
		t.Fatal(err)
	}
	session := &Session{State: exchange["state"], Verifier: exchange["code_verifier"]}
	wantURL, _ := url.Parse(fixture.AuthURL)
	gotURL, _ := url.Parse(session.AuthURL())
	if gotURL.Scheme != wantURL.Scheme || gotURL.Host != wantURL.Host || gotURL.Path != wantURL.Path || !reflect.DeepEqual(gotURL.Query(), wantURL.Query()) {
		t.Fatalf("authorize parameters differ: %s", session.AuthURL())
	}
	captured := make(chan captureRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{"host": r.Host}
		for name, values := range r.Header {
			headers[strings.ToLower(name)] = strings.Join(values, ", ")
		}
		if len(body) == 0 {
			body = []byte("null")
		}
		captured <- captureRequest{Method: r.Method, Headers: headers, Body: body}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/claude_cli/bootstrap" {
			_, _ = io.WriteString(w, `{"oauth_account":{"account_uuid":"22222222-2222-4222-8222-222222222222","account_email":"parity@example.invalid","organization_uuid":"44444444-4444-4444-8444-444444444444","organization_name":"Parity organization"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"sk-ant-oat-parity-fake","refresh_token":"refresh-parity-fake","expires_in":3600}`)
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	transport := &captureTransport{target: target, base: http.DefaultTransport}
	client := &Client{HTTP: &http.Client{Transport: transport}}
	before := time.Now()
	credential, err := client.Exchange(context.Background(), session, "code-parity-fake#"+session.State)
	after := time.Now()
	if err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, credential.Expire)
	if err != nil {
		t.Fatal(err)
	}
	if expires.Before(before.Add(55*time.Minute)) || expires.After(after.Add(55*time.Minute)) {
		t.Fatalf("stored expiry did not subtract five minutes: %s", credential.Expire)
	}
	if credential.AccountUUID != "22222222-2222-4222-8222-222222222222" || credential.OrganizationUUID != "44444444-4444-4444-8444-444444444444" || credential.Email != "parity@example.invalid" {
		t.Fatalf("bootstrap identity mismatch: account=%s org=%s email=%s", credential.AccountUUID, credential.OrganizationUUID, credential.Email)
	}
	refreshed, err := client.Refresh(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.OrganizationUUID != credential.OrganizationUUID {
		t.Fatal("refresh lost stored organization")
	}
	for index, want := range fixture.Requests {
		got := <-captured
		if got.Method != want.Method || transport.urls[index] != want.URL {
			t.Errorf("request %d method/URL differs", index)
		}
		if !reflect.DeepEqual(got.Headers, want.Headers) {
			t.Errorf("request %d headers\ngot %#v\nwant %#v", index, got.Headers, want.Headers)
		}
		var a, b any
		_ = json.Unmarshal(got.Body, &a)
		_ = json.Unmarshal(want.Body, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("request %d body\ngot %s\nwant %s", index, got.Body, want.Body)
		}
	}
	if RefreshLead != time.Minute {
		t.Fatal("refresh lead must be sixty seconds")
	}
	legacy, err := CredentialFromAuth(&coreauth.Auth{Provider: "claude", Prefix: "team", Metadata: fixture.LegacyCredential})
	if err != nil {
		t.Fatal(err)
	}
	legacy.InstallID = "persistent-install-fake"
	refreshedLegacy, err := client.Refresh(context.Background(), legacy)
	if err != nil {
		t.Fatal(err)
	}
	if refreshedLegacy.Prefix != "team" || refreshedLegacy.IDToken != "legacy-id-fake" || refreshedLegacy.InstallID != legacy.InstallID || refreshedLegacy.OrganizationUUID != legacy.OrganizationUUID || refreshedLegacy.OrganizationName != "Legacy organization" || !reflect.DeepEqual(refreshedLegacy.DeviceIDs, legacy.DeviceIDs) {
		t.Fatal("refresh lost legacy routing, identity, installation, or CLI metadata")
	}
	stored, err := CredentialMetadata(refreshedLegacy)
	if err != nil {
		t.Fatal(err)
	}
	if stored["owner_id"] != "owner-parity" || stored["access_token"] != refreshedLegacy.AccessToken || stored["neopi_expiry_skewed"] != true {
		t.Fatal("refresh failed to retain ownership or update authoritative token metadata")
	}
	for index, want := range fixture.LegacyRequests {
		got := <-captured
		if got.Method != want.Method || transport.urls[len(fixture.Requests)+index] != want.URL || !reflect.DeepEqual(got.Headers, want.Headers) {
			t.Errorf("legacy refresh request %d method/URL/headers differ: %#v", index, got)
		}
		var a, b any
		_ = json.Unmarshal(got.Body, &a)
		_ = json.Unmarshal(want.Body, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("legacy refresh request %d body got %s want %s", index, got.Body, want.Body)
		}
	}
}

func TestClaudeCredentialStorage(t *testing.T) {
	credential := &Credential{AccessToken: "fake", RefreshToken: "fake", AccountUUID: "account", InstallID: "install"}
	path := t.TempDir() + "/credential.json"
	if err := credential.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err = json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["type"] != "claude" || stored["prefix"] != "" {
		t.Fatalf("credential changed official routing or blank prefix: %#v", stored)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions %o", info.Mode().Perm())
	}
	generated, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if len(generated.State) != 32 || len(generated.Verifier) != 128 {
		t.Fatal("state or verifier entropy length differs from NeoPi")
	}
}

func TestClaudeVersionAdoption(t *testing.T) {
	wireVersionState.Lock()
	previous := wireVersionState.adopted
	wireVersionState.adopted = ""
	wireVersionState.Unlock()
	defer func() { wireVersionState.Lock(); wireVersionState.adopted = previous; wireVersionState.Unlock() }()
	t.Setenv("PI_AI_CLAUDE_CODE_VERSION", "")
	if !AdoptRequiredVersion("claude_code_version_too_old: version 2.1.281 or newer is required") {
		t.Fatal("did not adopt required newer version")
	}
	if WireVersion() != "2.1.281" || AdoptRequiredVersion("claude_code_version_too_old: version 2.1.281 or newer is required") {
		t.Fatal("version negotiation repeats")
	}
	t.Setenv("PI_AI_CLAUDE_CODE_VERSION", "2.1.280")
	if AdoptRequiredVersion("claude_code_version_too_old: version 3.0.0 or newer is required") {
		t.Fatal("explicit version override was changed")
	}
}
