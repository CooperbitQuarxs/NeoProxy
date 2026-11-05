package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/snowflake"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// An account's configured endpoint is the endpoint Snowflake's transport
// places and checks: a raw PAT is sent there without any SNOWFLAKE_ACCOUNT,
// and a signed-in account refuses an endpoint it does not own before sending.
func TestSnowflakeAccountEndpoint(t *testing.T) {
	t.Setenv("SNOWFLAKE_ACCOUNT", "")
	t.Setenv("SNOWFLAKE_PAT", "")
	requests, path, bearer := 0, "", ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		path, bearer = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","model":"openai-gpt-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	exec := NewNeoPiCompletionsExecutor("snowflake", &config.Config{}, NeoPiCompletionsProfile{})
	payload := []byte(`{"model":"openai-gpt-5.1","messages":[{"role":"user","content":"hi"}]}`)
	send := func(auth *cliproxyauth.Auth) error {
		_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "openai-gpt-5.1", Payload: payload},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
		return err
	}
	base := server.URL + "/api/v2/cortex/v1"
	if err := send(&cliproxyauth.Auth{Provider: "snowflake", Attributes: map[string]string{"api_key": "pat", "base_url": base}}); err != nil {
		t.Fatalf("PAT on its configured endpoint: %v", err)
	}
	if path != "/api/v2/cortex/v1/chat/completions" || bearer != "Bearer pat" {
		t.Fatalf("sent %s with %q, want the configured endpoint with the PAT", path, bearer)
	}
	signedIn := &cliproxyauth.Auth{Provider: "snowflake", Attributes: map[string]string{"base_url": base}, Metadata: map[string]any{
		"type": "snowflake", "access_token": "oauth-access", "refresh_token": "oauth-refresh",
		"expired": "2030-01-01T00:00:00Z", "enterprise_url": "https://myorg-myaccount.snowflakecomputing.com",
	}}
	if err := send(signedIn); err == nil || requests != 1 {
		t.Fatalf("signed-in account on a foreign endpoint: err %v after %d requests, want a refusal before sending", err, requests)
	}
}

// A record without a refresh token keeps its own access token while another
// record of the same account has a refresh in flight (NeoPi returns each
// refreshless credential itself).
func TestSnowflakeRefreshlessRecordsDoNotShareFlights(t *testing.T) {
	const account = "https://myorg-myaccount.snowflakecomputing.com"
	expires := time.Now().Add(time.Hour).UTC()
	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _, _ = snowflakeRefreshGroup.Do(account+"\x00", func() (any, error) {
			close(started)
			<-release
			return snowflake.Tokens{Access: "user-A", Expires: expires}, nil
		})
	}()
	<-started
	exec := NewNeoPiCompletionsExecutor("snowflake", &config.Config{}, NeoPiCompletionsProfile{})
	auth := &cliproxyauth.Auth{Provider: "snowflake", Metadata: map[string]any{
		"type": "snowflake", "access_token": "user-B", "refresh_token": "",
		"expired": expires.Add(-time.Minute).Format(time.RFC3339Nano), "enterprise_url": account,
	}}
	type outcome struct {
		auth *cliproxyauth.Auth
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		refreshed, err := exec.Refresh(context.Background(), auth)
		done <- outcome{refreshed, err}
	}()
	var got outcome
	select {
	case got = <-done:
		close(release)
	case <-time.After(5 * time.Second):
		close(release)
		got = <-done
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if access := got.auth.Metadata["access_token"]; access != "user-B" {
		t.Fatalf("access token = %v, want the record's own user-B", access)
	}
}

// A signed-in Snowflake account sends its OAuth access token to the account it
// signed in at, on both transports, ahead of any key or SNOWFLAKE_* setting.
func TestSnowflakeSignedInAccountRouting(t *testing.T) {
	t.Setenv("SNOWFLAKE_ACCOUNT", "env-org-env-account")
	t.Setenv("SNOWFLAKE_PAT", "env-pat")
	exec := NewNeoPiCompletionsExecutor("snowflake", &config.Config{}, NeoPiCompletionsProfile{})
	auth := &cliproxyauth.Auth{Provider: "snowflake", Metadata: map[string]any{
		"type": "snowflake", "access_token": "oauth-access", "refresh_token": "oauth-refresh",
		"expired": "2030-01-01T00:00:00Z", "enterprise_url": "https://myorg-myaccount.snowflakecomputing.com",
	}}
	if got := exec.apiKey(auth); got != "oauth-access" {
		t.Fatalf("bearer = %q, want the OAuth access token", got)
	}
	rows := 0
	for _, info := range registry.NeoPiCompletionsBundledModels("snowflake", "snowflake") {
		rows++
		routed := exec.accountRow(auth, info.NeoPi)
		want := "https://myorg-myaccount.snowflakecomputing.com/api/v2/cortex"
		if info.NeoPi.API == "openai-completions" {
			want += "/v1"
		}
		if routed.BaseURL != want {
			t.Errorf("%s (%s): endpoint %q, want %q", info.ID, info.NeoPi.API, routed.BaseURL, want)
		}
	}
	if rows == 0 {
		t.Fatal("no bundled Snowflake rows")
	}
}
