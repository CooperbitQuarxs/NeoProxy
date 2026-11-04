package anthropicnpi

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiid"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// CredentialFromAuth reads both existing Claude files and NeoPi-native imports.
// It never rotates tokens, rewrites expiry, or mutates the shared Auth record.
func CredentialFromAuth(record *coreauth.Auth) (*Credential, error) {
	if record == nil {
		return nil, fmt.Errorf("claude credential is missing")
	}
	credential := &Credential{Type: Provider}
	if stored, ok := record.Storage.(*Credential); ok && stored != nil {
		*credential = *stored
	}
	data, err := json.Marshal(record.Metadata)
	if err != nil {
		return nil, fmt.Errorf("decode Claude credential: %w", err)
	}
	if err = json.Unmarshal(data, credential); err != nil {
		return nil, fmt.Errorf("decode Claude credential: %w", err)
	}
	credential.Prefix = record.Prefix
	credential.Metadata = record.Metadata
	if credential.OrganizationName == "" {
		credential.OrganizationName = credential.OrgName
	}
	if record.Attributes != nil {
		credential.APIKey = strings.TrimSpace(record.Attributes["api_key"])
	}
	if credential.APIKey == "" {
		credential.APIKey, _ = record.Metadata["api_key"].(string)
		credential.APIKey = strings.TrimSpace(credential.APIKey)
	}
	// NeoPi accepts OAuth tokens in configured API-key slots as well as in
	// persisted OAuth records (utils/anthropic-auth.ts). Explicit access_token
	// fields remain OAuth even when fake/local tokens have no Anthropic prefix.
	if strings.Contains(credential.APIKey, "sk-ant-oat") {
		credential.AccessToken = credential.APIKey
		credential.APIKey = ""
	}
	if credential.AccessToken == "" && credential.APIKey == "" {
		baseURL := strings.TrimSpace(record.Attributes["base_url"])
		if baseURL == "" {
			baseURL, _ = record.Metadata["base_url"].(string)
			baseURL = strings.TrimSpace(baseURL)
		}
		if baseURL != "" {
			// NeoPi's sentinel selects the configured-header-only gateway path;
			// it is not an upstream credential and must never be sent as one.
			credential.APIKey = "N/A"
			return credential, nil
		}
		return nil, fmt.Errorf("claude credential is missing access_token or api_key")
	}
	return credential, nil
}

// RefreshTime applies NeoPi's five-minute expiry skew and one-minute refresh
// lead once. Old Claude files store raw expiry; new files mark the skew already
// included in expired. Imported NeoPi files must carry that same marker.
func RefreshTime(credential *Credential) (time.Time, error) {
	if credential == nil || credential.Expire == "" {
		return time.Time{}, nil
	}
	expires, err := time.Parse(time.RFC3339Nano, credential.Expire)
	if err != nil {
		return time.Time{}, err
	}
	if !credential.ExpirySkewed {
		expires = expires.Add(-ExpirySkew)
	}
	return expires.Add(-RefreshLead), nil
}

// EnsureInstallID supplies the shared NeoPi installation identity, including for
// legacy and refresh-locked credentials that carry none of their own.
func EnsureInstallID(authDir string, credential *Credential) error {
	if credential == nil {
		return fmt.Errorf("claude credential is missing")
	}
	if strings.TrimSpace(credential.InstallID) != "" {
		return nil
	}
	id, err := neopiid.InstallID(authDir)
	if err != nil {
		return err
	}
	credential.InstallID = id
	return nil
}

// FileName preserves the established organization/account-hashed Claude names.
func (c *Credential) FileName() string {
	return claude.CredentialFileName(c.Email, c.OrganizationUUID, c.AccountUUID)
}

// CredentialMetadata keeps arbitrary ownership/configuration metadata while
// making freshly exchanged token and identity fields authoritative.
func CredentialMetadata(c *Credential) (map[string]any, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	fields := make(map[string]any)
	if err = json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	merged := make(map[string]any, len(c.Metadata)+len(fields))
	for key, value := range c.Metadata {
		merged[key] = value
	}
	for key, value := range fields {
		merged[key] = value
	}
	return merged, nil
}
