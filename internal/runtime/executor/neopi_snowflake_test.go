package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

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
