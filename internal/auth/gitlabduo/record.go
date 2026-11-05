package gitlabduo

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

// Metadata is the record fields a sign-in is stored under: NeoPi's OAuth
// credential, its expiry as an RFC 3339 instant.
func Metadata(credential Credential) map[string]any {
	return map[string]any{
		"type":          Provider,
		"access_token":  credential.Access,
		"refresh_token": credential.Refresh,
		"expired":       credential.Expires.UTC().Format(time.RFC3339Nano),
	}
}

// FileName names a sign-in's record after a digest of its refresh token (its
// access token when it has none): GitLab's token answer names no account.
func FileName(credential Credential) string {
	seed := credential.Refresh
	if seed == "" {
		seed = credential.Access
	}
	digest := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%s-%x.json", Provider, digest[:8])
}

// RecordCredential reads a stored sign-in back from record metadata.
func RecordCredential(metadata map[string]any) (Credential, bool) {
	text := func(key string) string {
		value, _ := metadata[key].(string)
		return strings.TrimSpace(value)
	}
	credential := Credential{Access: text("access_token"), Refresh: text("refresh_token")}
	if credential.Access == "" {
		return Credential{}, false
	}
	if expired, err := time.Parse(time.RFC3339Nano, text("expired")); err == nil {
		credential.Expires = expired
	}
	return credential, true
}

// BaseURL is the address a personal access token (NeoPi's GITLAB_TOKEN) is
// added under as an account key: the GitLab instance it authenticates at.
const BaseURL = "https://gitlab.com"

// IsBaseURL reports whether an account key's base URL is GitLab's, so the
// key is served as GitLab Duo.
func IsBaseURL(base string) bool {
	return strings.TrimRight(strings.TrimSpace(base), "/") == BaseURL
}
