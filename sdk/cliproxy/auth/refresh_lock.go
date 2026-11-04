package auth

import (
	"errors"
	"strings"
	"time"
)

// Metadata keys written on credentials copied out of a NeoPi vault. They live in
// the auth file itself, so the lock survives restarts, reloads and file watchers.
const (
	MetadataRefreshLocked = "neo_refresh_locked"
	MetadataImportedFrom  = "neo_imported_from"
)

// RefreshLockedMessage is the operator-facing state of an imported credential
// whose access token can no longer be renewed by NeoProxy.
const RefreshLockedMessage = "Imported from NeoPi; refresh locked. Re-import or sign in separately."

// ErrRefreshLocked is returned instead of refreshing a refresh-locked credential.
var ErrRefreshLocked = errors.New(RefreshLockedMessage)

// ImportProvenance records where an imported credential came from.
type ImportProvenance struct {
	Profile      string
	CredentialID string
	// ImportedAt is epoch milliseconds of the latest import or re-import.
	ImportedAt int64
}

// IsRefreshLocked reports whether NeoProxy must never use or rotate the
// credential's refresh token. NeoPi remains the only party that refreshes it.
func IsRefreshLocked(a *Auth) bool {
	if a == nil || a.Metadata == nil {
		return false
	}
	locked, ok := parseBoolAny(a.Metadata[MetadataRefreshLocked])
	return ok && locked
}

// ImportedFromNeoPi returns the provenance of a credential imported from a NeoPi vault.
func ImportedFromNeoPi(a *Auth) (ImportProvenance, bool) {
	if a == nil || a.Metadata == nil {
		return ImportProvenance{}, false
	}
	raw, ok := a.Metadata[MetadataImportedFrom].(map[string]any)
	if !ok {
		return ImportProvenance{}, false
	}
	if source, _ := raw["source"].(string); source != "neopi" {
		return ImportProvenance{}, false
	}
	p := ImportProvenance{}
	p.Profile, _ = raw["profile"].(string)
	p.CredentialID, _ = raw["credential_id"].(string)
	if at, okAt := parseIntAny(raw["imported_at"]); okAt {
		p.ImportedAt = int64(at)
	}
	if strings.TrimSpace(p.Profile) == "" || strings.TrimSpace(p.CredentialID) == "" {
		return ImportProvenance{}, false
	}
	return p, true
}

// RefreshLockExpired reports whether a refresh-locked credential's access token
// has expired, so it cannot serve requests until it is re-imported.
func RefreshLockExpired(a *Auth, now time.Time) bool {
	if !IsRefreshLocked(a) {
		return false
	}
	expiry, ok := a.ExpirationTime()
	return ok && !expiry.IsZero() && !expiry.After(now)
}
