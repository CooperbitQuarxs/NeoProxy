package factorydroid

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
)

// RefreshSkew is OAUTH_REFRESH_SKEW_MS: NeoPi refreshes a credential once it
// is within a minute of expiring.
const RefreshSkew = time.Minute

// TokenStorage is a Factory sign-in on disk: NeoPi's OAuth credential, the
// WorkOS organization its grant is scoped to and the Factory account scope.
type TokenStorage struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// Expired is the RFC 3339 instant the access token expires.
	Expired              string `json:"expired,omitempty"`
	Email                string `json:"email,omitempty"`
	AccountID            string `json:"account_id,omitempty"`
	OrgID                string `json:"org_id,omitempty"`
	ActiveOrganizationID string `json:"active_organization_id,omitempty"`
	Region               string `json:"region,omitempty"`
	InferenceRegion      string `json:"inference_region,omitempty"`
	Type                 string `json:"type"`
	// Metadata carries extra fields merged into the saved JSON.
	Metadata map[string]any `json:"-"`
}

// SetMetadata sets the extra fields saved beside the credential.
func (ts *TokenStorage) SetMetadata(meta map[string]any) { ts.Metadata = meta }

// NewTokenStorage stores a credential.
func NewTokenStorage(credential *Credential) *TokenStorage {
	return &TokenStorage{
		AccessToken:          credential.AccessToken,
		RefreshToken:         credential.RefreshToken,
		Expired:              credential.Expires.UTC().Format(time.RFC3339),
		Email:                credential.Email,
		AccountID:            credential.AccountID,
		OrgID:                credential.OrgID,
		ActiveOrganizationID: credential.ActiveOrganizationID,
		Region:               credential.Region,
		InferenceRegion:      credential.InferenceRegion,
		Type:                 Provider,
	}
}

// SaveTokenToFile writes the storage as JSON.
func (ts *TokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = Provider
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}
	data, err := misc.MergeMetadata(ts, ts.Metadata)
	if err != nil {
		return fmt.Errorf("failed to merge metadata: %w", err)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to encode credential: %w", err)
	}
	return os.WriteFile(authFilePath, encoded, 0o600)
}

// Metadata is the record fields a credential is stored under.
func Metadata(credential *Credential) map[string]any {
	metadata := map[string]any{
		"type":          Provider,
		"access_token":  credential.AccessToken,
		"refresh_token": credential.RefreshToken,
		"expired":       credential.Expires.UTC().Format(time.RFC3339),
	}
	for key, value := range map[string]string{
		"email":                  credential.Email,
		"account_id":             credential.AccountID,
		"org_id":                 credential.OrgID,
		"active_organization_id": credential.ActiveOrganizationID,
		"region":                 credential.Region,
		"inference_region":       credential.InferenceRegion,
	} {
		if value != "" {
			metadata[key] = value
		}
	}
	return metadata
}

// RecordCredential reads a stored credential back from record metadata.
func RecordCredential(metadata map[string]any) (*Credential, bool) {
	text := func(key string) string {
		value, _ := metadata[key].(string)
		return value
	}
	credential := &Credential{
		AccessToken:          text("access_token"),
		RefreshToken:         text("refresh_token"),
		Email:                text("email"),
		AccountID:            text("account_id"),
		OrgID:                text("org_id"),
		ActiveOrganizationID: text("active_organization_id"),
		Region:               text("region"),
		InferenceRegion:      text("inference_region"),
	}
	if credential.AccessToken == "" {
		return nil, false
	}
	if expired, err := time.Parse(time.RFC3339, text("expired")); err == nil {
		credential.Expires = expired
	}
	return credential, true
}

// Merge is mergeRefreshedCredential: the refreshed tokens and identity, with
// what the refresh omitted kept from the stored credential. Organization
// scope carries over only while the organization is unchanged
// (mergeRefreshedOrganizationScope).
func Merge(stored, refreshed *Credential) *Credential {
	merged := *refreshed
	merged.Email = firstNonEmpty(refreshed.Email, stored.Email)
	merged.AccountID = firstNonEmpty(refreshed.AccountID, stored.AccountID)
	sameOrg := (refreshed.OrgID == "" || refreshed.OrgID == stored.OrgID) &&
		(refreshed.ActiveOrganizationID == "" || refreshed.ActiveOrganizationID == stored.ActiveOrganizationID)
	merged.OrgID, merged.Region, merged.InferenceRegion = refreshed.OrgID, refreshed.Region, refreshed.InferenceRegion
	if sameOrg {
		merged.OrgID = firstNonEmpty(refreshed.OrgID, stored.OrgID)
		merged.Region = firstNonEmpty(refreshed.Region, stored.Region)
		merged.InferenceRegion = firstNonEmpty(refreshed.InferenceRegion, stored.InferenceRegion)
	}
	merged.ActiveOrganizationID = firstNonEmpty(refreshed.ActiveOrganizationID, stored.ActiveOrganizationID)
	return &merged
}
