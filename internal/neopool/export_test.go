package neopool

import "net/http"

// SetCatalogBaseURLForTest points catalogue API-key providers at a fake upstream.
func SetCatalogBaseURLForTest(fn func(string) string) (restore func()) {
	previous := catalogBaseURL
	catalogBaseURL = fn
	return func() { catalogBaseURL = previous }
}

// SetCatalogRegionsForTest points providers with regional endpoints at fakes.
func SetCatalogRegionsForTest(fn func(string) []string) (restore func()) {
	previous := catalogRegions
	catalogRegions = fn
	return func() { catalogRegions = previous }
}

// SetAccessTransportForTest routes key checks through rt, so a test can see
// which real provider endpoint a check addressed.
func SetAccessTransportForTest(rt http.RoundTripper) (restore func()) {
	previous := accessHTTPClient.Transport
	accessHTTPClient.Transport = rt
	return func() { accessHTTPClient.Transport = previous }
}
