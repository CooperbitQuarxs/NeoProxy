package neopool_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
)

// A Snowflake PAT is added for the account it names and stored as NeoPi's
// structured credential bound to that account; nothing is probed, and an
// account outside snowflakecomputing.com is refused with NeoPi's reason.
func TestSnowflakeKeyIsBoundToItsAccount(t *testing.T) {
	f := newAccessFixture(t)
	t.Cleanup(neopool.SetAccessTransportForTest(refuseTransport{t}))
	item := itemOf(t, f.json("owner-a", "POST", "/v8/neo/access/api-key", `{"provider":"snowflake","apiKey":"pat-0123456789abcd","account":"https://app.snowflake.com/MyOrg/My_Account"}`, 201))
	// The pool names the token's ending and its account, never the stored JSON.
	if want := "Snowflake Cortex key ending in abcd (myorg-my-account.snowflakecomputing.com)"; item.Label != want {
		t.Errorf("label = %q, want %q", item.Label, want)
	}
	cfg, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `api-key: '{"enterpriseUrl":"https://myorg-my-account.snowflakecomputing.com","token":"pat-0123456789abcd"}'`
	if !strings.Contains(string(cfg), want) {
		t.Fatalf("engine config lacks %s:\n%s", want, cfg)
	}

	for body, reason := range map[string]string{
		`{"provider":"snowflake","apiKey":"pat-123","account":"https://evil.example.com"}`: "Paste your Snowflake account identifier",
		`{"provider":"snowflake","apiKey":"pat-123"}`:                                      "Snowflake account is required",
	} {
		w := f.do("owner-a", "POST", "/v8/neo/access/api-key", []byte(body), "")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), reason) {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
}
