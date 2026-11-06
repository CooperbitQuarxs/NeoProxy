package neopool

import (
	"errors"
	"net/http"
	"path/filepath"
	"testing"
)

type refuseTransport struct{ t *testing.T }

func (r refuseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("unexpected key probe %s %s", req.Method, req.URL)
	return nil, errors.New("no requests expected")
}

// A Bedrock Mantle key is added without a check (NeoPi's auth rule has no
// validate hook, and allows a missing key) and keeps the endpoint of the
// region the server's AWS settings select.
func TestBedrockMantleKeyIsAddedUncheckedAtTheServerRegion(t *testing.T) {
	original := accessHTTPClient
	t.Cleanup(func() { accessHTTPClient = original })
	accessHTTPClient = &http.Client{Transport: refuseTransport{t}}
	for _, name := range []string{"AWS_DEFAULT_REGION", "AWS_PROFILE", "AWS_SDK_LOAD_CONFIG"} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_REGION", "eu-north-1")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
	info, ok := catalogProvider("bedrock-mantle")
	if !ok || info.APIKey == nil || !info.APIKey.KeyOptional || info.APIKey.EditableBaseURL || info.dialect != "responses" {
		t.Fatalf("catalog entry = %+v", info)
	}
	for _, key := range []string{"ABSK-pasted-key", ""} {
		base, models, routes, err := validateCatalogKey(t.Context(), info, info.APIKey.BaseURL, regionChoice{}, key)
		if err != nil || base != "https://bedrock-mantle.eu-north-1.api.aws/openai/v1" || models != nil || routes != nil {
			t.Fatalf("key %q: validateCatalogKey = %q, %v, %v, %v", key, base, models, routes, err)
		}
	}
}
