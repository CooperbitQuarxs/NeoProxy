package neopool

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
