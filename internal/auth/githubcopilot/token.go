package githubcopilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
)

// TokenStorage is a GitHub Copilot sign-in on disk: NeoPi's OAuth
// credential, whose GitHub token is both access and refresh, with the
// Enterprise domain and plan endpoint it is served from.
type TokenStorage struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// Expired is the RFC 3339 instant NeoPi records: far in the future, the
	// GitHub token being long-lived.
	Expired       string `json:"expired,omitempty"`
	EnterpriseURL string `json:"enterprise_url,omitempty"`
	APIEndpoint   string `json:"api_endpoint,omitempty"`
	Type          string `json:"type"`
	// Metadata carries extra fields merged into the saved JSON.
	Metadata map[string]any `json:"-"`
}

// SetMetadata sets the extra fields saved beside the credential.
func (ts *TokenStorage) SetMetadata(meta map[string]any) { ts.Metadata = meta }

// NewTokenStorage stores a credential.
func NewTokenStorage(credential *Credential) *TokenStorage {
	return &TokenStorage{
		AccessToken:   credential.AccessToken,
		RefreshToken:  credential.AccessToken,
		Expired:       credential.Expires.UTC().Format(time.RFC3339),
		EnterpriseURL: credential.EnterpriseURL,
		APIEndpoint:   credential.APIEndpoint,
		Type:          Provider,
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
		"refresh_token": credential.AccessToken,
		"expired":       credential.Expires.UTC().Format(time.RFC3339),
	}
	if credential.EnterpriseURL != "" {
		metadata["enterprise_url"] = credential.EnterpriseURL
	}
	if credential.APIEndpoint != "" {
		metadata["api_endpoint"] = credential.APIEndpoint
	}
	return metadata
}

// RecordCredential reads a stored credential back from record metadata:
// the GitHub token (access, else refresh, as NeoPi's refresh hook copies one
// to the other), the Enterprise domain and the plan endpoint, normalized.
func RecordCredential(metadata map[string]any) (*Credential, bool) {
	text := func(key string) string {
		value, _ := metadata[key].(string)
		return value
	}
	token := text("access_token")
	if token == "" {
		token = text("refresh_token")
	}
	if token == "" {
		return nil, false
	}
	credential := &Credential{AccessToken: token, EnterpriseURL: NormalizeEnterpriseDomain(text("enterprise_url")),
		APIEndpoint: NormalizeAPIEndpoint(text("api_endpoint"))}
	if expired, err := time.Parse(time.RFC3339, text("expired")); err == nil {
		credential.Expires = expired
	}
	return credential, true
}
