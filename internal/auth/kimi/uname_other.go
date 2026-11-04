//go:build !unix

package kimi

// unameReleaseVersion has no portable source outside Unix; the fingerprint
// then falls back to "unknown" fields as NeoPi's sanitizer does.
func unameReleaseVersion() (string, string) { return "", "" }
