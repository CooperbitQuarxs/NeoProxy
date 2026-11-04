// Package anthropicnpi implements the experimental NeoPi Anthropic subscription
// flow independently of the existing claude provider. Wire constants and token
// projection follow NeoPi's rules/auth/anthropic.kdl and oauth-code engine.
package anthropicnpi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	Provider     = "anthropic-npi"
	AuthorizeURL = "https://claude.ai/oauth/authorize"
	TokenURL     = "https://api.anthropic.com/v1/oauth/token"
	BootstrapURL = "https://api.anthropic.com/api/claude_cli/bootstrap?entrypoint=cli&model=claude-opus-4-8"
	RedirectURI  = "http://localhost:54545/callback"
	ClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	Scope        = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	CodeVersion  = "2.1.280"
	SDKVersion   = "0.112.1"
	ExpirySkew   = 5 * time.Minute
	RefreshLead  = time.Minute
)

// Session owns one PKCE verifier; neither the verifier nor the exchanged code is logged.
type Session struct {
	State    string
	Verifier string
}

func NewSession() (*Session, error) {
	var state [16]byte
	var verifier [96]byte
	if _, err := rand.Read(state[:]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(verifier[:]); err != nil {
		return nil, err
	}
	return &Session{State: hex.EncodeToString(state[:]), Verifier: base64.RawURLEncoding.EncodeToString(verifier[:])}, nil
}

func (s *Session) AuthURL() string {
	challenge := sha256.Sum256([]byte(s.Verifier))
	params := url.Values{
		"client_id": {ClientID}, "response_type": {"code"}, "redirect_uri": {RedirectURI},
		"scope": {Scope}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"}, "state": {s.State}, "code": {"true"},
	}
	return AuthorizeURL + "?" + params.Encode()
}

type Credential struct {
	Type                 string         `json:"type"`
	Prefix               string         `json:"prefix"`
	AccessToken          string         `json:"access_token"`
	RefreshToken         string         `json:"refresh_token"`
	Expire               string         `json:"expired"`
	LastRefresh          string         `json:"last_refresh"`
	Email                string         `json:"email,omitempty"`
	AccountUUID          string         `json:"account_uuid,omitempty"`
	OrganizationUUID     string         `json:"organization_uuid,omitempty"`
	OrganizationName     string         `json:"organization_name,omitempty"`
	ActiveOrganizationID string         `json:"activeOrganizationId,omitempty"`
	InstallID            string         `json:"neopi_install_id"`
	Metadata             map[string]any `json:"-"`
}

func (c *Credential) SetMetadata(meta map[string]any) { c.Metadata = meta }

func (c *Credential) SaveTokenToFile(path string) error {
	c.Type = Provider
	c.Prefix = Provider
	data, err := misc.MergeMetadata(c, c.Metadata)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".anthropic-npi-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(encoded); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (c *Credential) FileName() string {
	identity := sha256.Sum256([]byte(c.AccountUUID + "\x00" + c.OrganizationUUID + "\x00" + c.Email))
	return Provider + "-" + hex.EncodeToString(identity[:16]) + ".json"
}

type Client struct{ HTTP *http.Client }

func NewClient(cfg *config.Config, proxyURL string) *Client {
	var sdk config.SDKConfig
	if cfg != nil {
		sdk = cfg.SDKConfig
	}
	if proxyURL != "" {
		sdk.ProxyURL = proxyURL
	}
	return &Client{HTTP: util.SetProxy(&sdk, &http.Client{Timeout: 30 * time.Second})}
}

type tokenResponse struct {
	Access  string   `json:"access_token"`
	Refresh string   `json:"refresh_token"`
	Expires *float64 `json:"expires_in"`
	Account struct {
		UUID  string `json:"uuid"`
		Email string `json:"email_address"`
	} `json:"account"`
	Organization struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"organization"`
}

func (c *Client) token(ctx context.Context, body any, refresh bool) (*Credential, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	fetchHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	if refresh {
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", "anthropic-sdk-typescript/"+SDKVersion+" userOAuthProvider")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic-npi token transport: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("anthropic-npi token endpoint returned HTTP %d", resp.StatusCode)
	}
	var token tokenResponse
	if err = decodeJSON(resp, &token); err != nil {
		return nil, fmt.Errorf("anthropic-npi token endpoint returned invalid JSON")
	}
	if token.Access == "" || token.Expires == nil {
		return nil, fmt.Errorf("anthropic-npi token response is missing access_token or expires_in")
	}
	now := time.Now()
	credential := &Credential{Type: Provider, Prefix: Provider, AccessToken: token.Access, RefreshToken: token.Refresh,
		Expire:      now.Add(time.Duration(*token.Expires*float64(time.Second)) - ExpirySkew).UTC().Format(time.RFC3339Nano),
		LastRefresh: now.UTC().Format(time.RFC3339Nano), Email: token.Account.Email, AccountUUID: token.Account.UUID}
	if !refresh {
		credential.OrganizationUUID = token.Organization.UUID
		credential.OrganizationName = token.Organization.Name
	}
	c.bootstrap(ctx, credential, !refresh)
	return credential, nil
}

func (c *Client) Exchange(ctx context.Context, session *Session, code string) (*Credential, error) {
	state := session.State
	if before, after, found := strings.Cut(code, "#"); found {
		code = before
		if after != "" {
			state = after
		}
	}
	body := struct {
		GrantType   string `json:"grant_type"`
		ClientID    string `json:"client_id"`
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
		Verifier    string `json:"code_verifier"`
		State       string `json:"state"`
	}{"authorization_code", ClientID, code, RedirectURI, session.Verifier, state}
	return c.token(ctx, body, false)
}

// Refresh deliberately makes one token request: a lost response may already
// have rotated the refresh token. The executor serializes calls per credential.
func (c *Client) Refresh(ctx context.Context, previous *Credential) (*Credential, error) {
	if previous.RefreshToken == "" {
		return nil, fmt.Errorf("anthropic-npi refresh token is missing")
	}
	body := struct {
		GrantType      string `json:"grant_type"`
		ClientID       string `json:"client_id"`
		RefreshToken   string `json:"refresh_token"`
		OrganizationID string `json:"organization_id,omitempty"`
	}{"refresh_token", ClientID, previous.RefreshToken, previous.ActiveOrganizationID}
	credential, err := c.token(ctx, body, true)
	if err != nil {
		return nil, err
	}
	if credential.RefreshToken == "" {
		credential.RefreshToken = previous.RefreshToken
	}
	if credential.AccountUUID == "" {
		credential.AccountUUID = previous.AccountUUID
	}
	if credential.Email == "" {
		credential.Email = previous.Email
	}
	credential.OrganizationUUID = previous.OrganizationUUID
	credential.OrganizationName = previous.OrganizationName
	credential.InstallID = previous.InstallID
	credential.ActiveOrganizationID = previous.ActiveOrganizationID
	return credential, nil
}

func (c *Client) bootstrap(ctx context.Context, credential *Credential, includeOrg bool) {
	if credential.AccountUUID != "" && credential.Email != "" && (!includeOrg || credential.OrganizationUUID != "") {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BootstrapURL, nil)
	if err != nil {
		return
	}
	fetchHeaders(req)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-code/"+WireVersion())
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	var body struct {
		Account struct {
			UUID             string `json:"account_uuid"`
			Email            string `json:"account_email"`
			OrganizationUUID string `json:"organization_uuid"`
			OrganizationName string `json:"organization_name"`
		} `json:"oauth_account"`
	}
	if decodeJSON(resp, &body) != nil {
		return
	}
	if credential.AccountUUID == "" {
		credential.AccountUUID = body.Account.UUID
	}
	if credential.Email == "" {
		credential.Email = body.Account.Email
	}
	if includeOrg {
		if credential.OrganizationUUID == "" {
			credential.OrganizationUUID = body.Account.OrganizationUUID
		}
		if credential.OrganizationName == "" {
			credential.OrganizationName = body.Account.OrganizationName
		}
	}
}
