package codexwire

import (
	"strings"
	"testing"
)

// Vectors are Bun.hash(value).toString(36) from Bun 1.4.4, covering every
// Wyhash length branch (empty, <4, <=16, <48, exactly 48, multi-round).
func TestBunHashMatchesBun(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"call:1#🦊", "3reemsusm81v"},
		{"", "27k1wwwhf13t"},
		{"a", "mcie3cqm6cz5"},
		{"abc", "1g45uqqks6lu"},
		{"abcd", "13w92m5xe651v"},
		{"0123456789abcdef", "2yrjmtoe4yjah"},
		{"0123456789abcdef0", "3u7gojn9s87lc"},
		{strings.Repeat("x", 47), "16b8axjufujzc"},
		{strings.Repeat("x", 48), "2aqe7o80phxou"},
		{strings.Repeat("x", 49), "3pc7qi655mct1"},
		{strings.Repeat("y", 97), "rcm6ic4kwh07"},
		{"The quick brown fox jumps over the lazy dog, then naps for 1234567890 seconds.", "2155ais5cnpew"},
		{"empty", "2qpgyfjaphmay"},
	} {
		if got := bunHash36(tc.input); got != tc.want {
			t.Errorf("bunHash36(%q) = %s, Bun = %s", tc.input, got, tc.want)
		}
	}
}
