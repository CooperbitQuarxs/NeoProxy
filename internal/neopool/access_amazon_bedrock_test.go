package neopool

import (
	"net/http"
	"testing"
)

// An Amazon Bedrock key is added without a check (NeoPi's auth rule has an
// environment hook and no validate hook), empty included, and keeps the AWS
// regional host the executor re-points per request.
func TestAmazonBedrockKeyIsAddedUnchecked(t *testing.T) {
	original := accessHTTPClient
	t.Cleanup(func() { accessHTTPClient = original })
	accessHTTPClient = &http.Client{Transport: refuseTransport{t}}
	info, ok := catalogProvider("amazon-bedrock")
	if !ok || info.APIKey == nil || !info.APIKey.KeyOptional || info.APIKey.EditableBaseURL {
		t.Fatalf("catalog entry = %+v", info)
	}
	for _, key := range []string{"ABSK-pasted-key", ""} {
		base, models, routes, err := validateCatalogKey(t.Context(), info, info.APIKey.BaseURL, regionChoice{}, key)
		if err != nil || base != "https://bedrock-runtime.us-east-1.amazonaws.com" || models != nil || routes != nil {
			t.Fatalf("key %q: validateCatalogKey = %q, %v, %v, %v", key, base, models, routes, err)
		}
	}
}
