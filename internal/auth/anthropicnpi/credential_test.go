package anthropicnpi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRefreshTimeLegacyAndSkewedExpiry(t *testing.T) {
	rawExpiry := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	legacy := &Credential{Expire: rawExpiry.Format(time.RFC3339Nano)}
	ported := &Credential{Expire: rawExpiry.Add(-ExpirySkew).Format(time.RFC3339Nano), ExpirySkewed: true}
	for _, credential := range []*Credential{legacy, ported} {
		original := credential.Expire
		for range 2 {
			due, err := RefreshTime(credential)
			if err != nil || !due.Equal(rawExpiry.Add(-6*time.Minute)) {
				t.Fatalf("refresh schedule = %s, %v", due, err)
			}
			if credential.Expire != original {
				t.Fatal("loading a credential rewrote its expiry")
			}
		}
	}
}

func TestLegacyInstallIdentityPersistsWithoutCredentialRewrite(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "claude-existing.json")
	original := []byte(`{"type":"claude","access_token":"fake","neo_refresh_locked":true}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	const count = 16
	ids := make(chan string, count)
	errorsCh := make(chan error, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			record := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{"access_token": "fake", coreauth.MetadataRefreshLocked: true}}
			credential, err := CredentialFromAuth(record)
			if err == nil {
				err = EnsureInstallID(authDir, credential)
			}
			if err != nil {
				errorsCh <- err
				return
			}
			ids <- credential.InstallID
		}()
	}
	group.Wait()
	close(ids)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		} else if id != first {
			t.Fatal("concurrent legacy credentials acquired different installation identities")
		}
	}
	if first == "" {
		t.Fatal("missing installation identity")
	}
	credential := &Credential{}
	if err := EnsureInstallID(authDir, credential); err != nil || credential.InstallID != first {
		t.Fatalf("installation identity did not survive reload: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(original) {
		t.Fatal("installation migration rewrote a refresh-locked credential")
	}
	info, err := os.Stat(filepath.Join(authDir, ".claude-neopi-install-id"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("installation identity permissions: %v", err)
	}
}

type forbiddenAuthTransport struct{ t *testing.T }

func (transport forbiddenAuthTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.t.Error("refresh-locked credential attempted a network request")
	return nil, errors.New("network forbidden")
}

func TestClientRefreshRespectsImportedRefreshLock(t *testing.T) {
	client := &Client{HTTP: &http.Client{Transport: forbiddenAuthTransport{t}}}
	credential := &Credential{RefreshToken: "never-rotate-fake", Metadata: map[string]any{coreauth.MetadataRefreshLocked: true}}
	if _, err := client.Refresh(context.Background(), credential); !errors.Is(err, coreauth.ErrRefreshLocked) {
		t.Fatalf("refresh lock error = %v", err)
	}
}

func TestCredentialFromAuthAPIKeyAndLegacyMetadata(t *testing.T) {
	for _, prefix := range []string{"", "team"} {
		record := &coreauth.Auth{Provider: "claude", Prefix: prefix, Metadata: map[string]any{"access_token": "fake-oauth", "id_token": "fake-id", "org_name": "Legacy org", "claude_device_ids": []string{"device-fake"}, "owner_id": "owner-fake"}}
		credential, err := CredentialFromAuth(record)
		if err != nil || credential.Prefix != prefix || credential.OrganizationName != "Legacy org" || credential.IDToken != "fake-id" || len(credential.DeviceIDs) != 1 {
			t.Fatalf("legacy projection = %+v, %v", credential, err)
		}
	}
	credential, err := CredentialFromAuth(&coreauth.Auth{Attributes: map[string]string{"api_key": "fake-api-key"}})
	if err != nil || credential.APIKey != "fake-api-key" || credential.AccessToken != "" {
		t.Fatalf("API key projection = %+v, %v", credential, err)
	}
	for _, record := range []*coreauth.Auth{
		{Attributes: map[string]string{"base_url": "https://gateway.example.invalid", "header:Authorization": "configured-fake"}},
		{Metadata: map[string]any{"base_url": "https://gateway.example.invalid"}},
	} {
		credential, err := CredentialFromAuth(record)
		if err != nil || credential.APIKey != "N/A" || credential.AccessToken != "" {
			t.Fatalf("keyless gateway projection = %+v, %v", credential, err)
		}
	}
	if _, err := CredentialFromAuth(&coreauth.Auth{}); err == nil {
		t.Fatal("unconfigured credential was accepted")
	}
	for _, record := range []*coreauth.Auth{
		{Attributes: map[string]string{"api_key": "sk-ant-oat-configured-fake"}},
		{Metadata: map[string]any{"api_key": "sk-ant-oat-configured-fake"}},
	} {
		credential, err := CredentialFromAuth(record)
		if err != nil || credential.APIKey != "" || credential.AccessToken != "sk-ant-oat-configured-fake" {
			t.Fatalf("configured OAuth key projection = %+v, %v", credential, err)
		}
	}
}
