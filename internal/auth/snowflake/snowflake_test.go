package snowflake

import (
	"encoding/json"
	"os"
	"testing"
)

// snowflakeAccounts is testdata/snowflake_accounts.json: NeoPi's OWN
// normalizeSnowflakeAccountUrl over account inputs, and
// snowflakeTransport.prepareRequest over endpoint, key and SNOWFLAKE_ACCOUNT
// combinations, each with its result or the error NeoPi throws.
type snowflakeAccounts struct {
	Placeholder string `json:"placeholder"`
	Normalize   []struct {
		Input string `json:"input"`
		URL   string `json:"url"`
		Error string `json:"error"`
	} `json:"normalize"`
	Transport []struct {
		Base       string  `json:"base"`
		APIKey     string  `json:"apiKey"`
		EnvAccount *string `json:"envAccount"`
		BaseURL    string  `json:"baseUrl"`
		Token      *string `json:"token"`
		Error      string  `json:"error"`
	} `json:"transport"`
}

// Account identifiers, account URLs and Snowsight links normalize as NeoPi
// normalizes them, with NeoPi's error for each input it refuses.
func TestNormalizeAccountURLMatchesNeoPi(t *testing.T) {
	capture := loadSnowflakeAccounts(t)
	if capture.Placeholder != Placeholder {
		t.Fatalf("placeholder = %q, NeoPi %q", Placeholder, capture.Placeholder)
	}
	for _, c := range capture.Normalize {
		got, err := NormalizeAccountURL(c.Input)
		switch {
		case c.Error != "" && (err == nil || err.Error() != c.Error):
			t.Errorf("%q: got %q, %v; NeoPi throws %q", c.Input, got, err, c.Error)
		case c.Error == "" && (err != nil || got != c.URL):
			t.Errorf("%q: got %q, %v; NeoPi %q", c.Input, got, err, c.URL)
		}
	}
}

// The request endpoint and bearer follow snowflakeTransport.prepareRequest for
// every endpoint, PAT or structured OAuth key, and SNOWFLAKE_ACCOUNT.
func TestTransportMatchesNeoPi(t *testing.T) {
	capture := loadSnowflakeAccounts(t)
	for _, c := range capture.Transport {
		env := ""
		if c.EnvAccount != nil {
			env = *c.EnvAccount
		}
		credential, ok, err := ParseCredential(c.APIKey)
		base, token := c.Base, ""
		if err == nil && ok {
			base, err = BaseURL(c.Base, credential, env)
			token = credential.Token
		}
		name := c.Base + " | " + c.APIKey + " | " + env
		if c.Error != "" {
			if err == nil || err.Error() != c.Error {
				t.Errorf("%s: got %q, %v; NeoPi throws %q", name, base, err, c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v; NeoPi sends to %q", name, err, c.BaseURL)
			continue
		}
		// NeoPi leaves the request untouched without a credential.
		wantToken := c.APIKey
		if c.Token != nil {
			wantToken = *c.Token
		}
		if !ok {
			token = c.APIKey
		}
		if base != c.BaseURL || token != wantToken {
			t.Errorf("%s: %q with %q; NeoPi %q with %q", name, base, token, c.BaseURL, wantToken)
		}
	}
}

func loadSnowflakeAccounts(t *testing.T) snowflakeAccounts {
	t.Helper()
	raw, err := os.ReadFile("testdata/snowflake_accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture snowflakeAccounts
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Normalize) == 0 || len(capture.Transport) == 0 {
		t.Fatal("capture holds no cases")
	}
	return capture
}
