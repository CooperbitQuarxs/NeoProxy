package neopool

// SetCatalogBaseURLForTest points catalogue API-key providers at a fake upstream.
func SetCatalogBaseURLForTest(fn func(string) string) (restore func()) {
	previous := catalogBaseURL
	catalogBaseURL = fn
	return func() { catalogBaseURL = previous }
}
