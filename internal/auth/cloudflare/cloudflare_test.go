package cloudflare

import (
	"encoding/json"
	"os"
	"testing"
)

// cloudflareCapture is testdata/cloudflare.json: NeoPi's OWN
// parseCloudflareCredential, serializeCloudflareCredential and
// cloudflareAiGatewayTransport.prepareRequest over inputs, keys, endpoints
// and environment ids, each with its result or the error NeoPi throws.
type cloudflareCapture struct {
	GatewayBase string `json:"gatewayBase"`
	Parse       []struct {
		Input      string      `json:"input"`
		Credential *Credential `json:"credential"`
	} `json:"parse"`
	Serialize []struct {
		Credential Credential `json:"credential"`
		Serialized string     `json:"serialized"`
	} `json:"serialize"`
	Transport []struct {
		Base     string            `json:"base"`
		APIKey   string            `json:"apiKey"`
		Env      map[string]string `json:"env"`
		BaseURL  string            `json:"baseUrl"`
		Headers  map[string]string `json:"headers"`
		NoAuth   bool              `json:"noAuth"`
		Prepared bool              `json:"prepared"`
		Error    string            `json:"error"`
	} `json:"transport"`
}

func loadCloudflareCapture(t *testing.T) cloudflareCapture {
	t.Helper()
	raw, err := os.ReadFile("testdata/cloudflare.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture cloudflareCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Parse) == 0 || len(capture.Serialize) == 0 || len(capture.Transport) == 0 {
		t.Fatal("capture holds no cases")
	}
	if capture.GatewayBase != GatewayBase {
		t.Fatalf("gateway base = %q, NeoPi %q", GatewayBase, capture.GatewayBase)
	}
	return capture
}

// Credentials parse and serialize as NeoPi's wire helpers do, including
// String.prototype.trim's whitespace and JSON.stringify's field order.
func TestCredentialMatchesNeoPi(t *testing.T) {
	capture := loadCloudflareCapture(t)
	for _, c := range capture.Parse {
		got, ok := ParseCredential(c.Input)
		if c.Credential == nil {
			if ok {
				t.Errorf("parse %q = %+v, NeoPi refuses it", c.Input, got)
			}
			continue
		}
		if !ok || got != *c.Credential {
			t.Errorf("parse %q = %+v (%v), NeoPi %+v", c.Input, got, ok, *c.Credential)
		}
	}
	for _, c := range capture.Serialize {
		if got := Serialize(c.Credential); got != c.Serialized {
			t.Errorf("serialize %+v = %s, NeoPi %s", c.Credential, got, c.Serialized)
		}
	}
}

// The endpoint follows prepareRequest for every endpoint, key and
// environment: placeholders filled from the key, else the environment, with
// NeoPi's error when an id is missing, and the token sent as the gateway's
// bearer. A key NeoPi cannot parse leaves its request unprepared.
func TestTransportMatchesNeoPi(t *testing.T) {
	capture := loadCloudflareCapture(t)
	for _, c := range capture.Transport {
		credential, ok := ParseCredential(c.APIKey)
		if !ok {
			if c.Error != "" || c.Prepared {
				t.Errorf("%s with %q: NeoPi prepared it (%+v)", c.Base, c.APIKey, c)
			}
			continue
		}
		base, err := BaseURL(c.Base, credential, c.Env["CLOUDFLARE_ACCOUNT_ID"], c.Env["CLOUDFLARE_GATEWAY_ID"])
		if c.Error != "" {
			if err == nil || err.Error() != c.Error {
				t.Errorf("%s with %q %v: error %v, NeoPi %q", c.Base, c.APIKey, c.Env, err, c.Error)
			}
			continue
		}
		if err != nil || base != c.BaseURL {
			t.Errorf("%s with %q %v: %q (%v), NeoPi %q", c.Base, c.APIKey, c.Env, base, err, c.BaseURL)
		}
		if want := "Bearer " + credential.Token; !c.NoAuth || c.Headers["cf-aig-authorization"] != want {
			t.Errorf("%s with %q: NeoPi authorizes with %q (no auth %v), want %q", c.Base, c.APIKey, c.Headers["cf-aig-authorization"], c.NoAuth, want)
		}
	}
}
