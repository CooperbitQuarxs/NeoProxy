package kimi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	log "github.com/sirupsen/logrus"
)

// Type is the auth-file type and engine provider of a Kimi Code sign-in.
const Type = "kimi"

// KimiTokenStorage is a Kimi Code sign-in on disk: NeoPi's credential
// (access, refresh, expiry already five minutes early, account id).
type KimiTokenStorage struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	// Expired is the RFC 3339 instant NeoPi treats the access token as expired.
	Expired   string `json:"expired,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	Type      string `json:"type"`
	// Metadata carries extra fields merged into the saved JSON.
	Metadata map[string]any `json:"-"`
}

// SetMetadata sets the extra fields saved beside the credential.
func (ts *KimiTokenStorage) SetMetadata(meta map[string]any) { ts.Metadata = meta }

// NewTokenStorage stores a credential.
func NewTokenStorage(credential *Credential) *KimiTokenStorage {
	return &KimiTokenStorage{
		AccessToken:  credential.AccessToken,
		RefreshToken: credential.RefreshToken,
		Expired:      credential.Expires.UTC().Format(time.RFC3339),
		AccountID:    credential.AccountID,
		Type:         Type,
	}
}

// SaveTokenToFile writes the storage as JSON.
func (ts *KimiTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = Type
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0o700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}
	data, err := misc.MergeMetadata(ts, ts.Metadata)
	if err != nil {
		return fmt.Errorf("failed to merge metadata: %w", err)
	}
	f, err := os.Create(authFilePath)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("kimi token storage: close token file error: %v", errClose)
		}
	}()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(data); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}
