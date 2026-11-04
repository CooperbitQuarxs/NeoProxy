// Package nous implements Nous Research Portal's OAuth device sign-in, token
// refresh and model discovery. Client id, scope, endpoints and the refresh
// header match Nous's own Hermes client, which NeoPi's extension also uses.
package nous

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

const (
	// Provider is the engine provider (auth-file "type") and catalogue id.
	Provider = "nous-portal"
	// PortalURL hosts the OAuth endpoints and the verification page.
	PortalURL = "https://portal.nousresearch.com"
	// InferenceURL is the OpenAI-compatible inference API.
	InferenceURL = "https://inference-api.nousresearch.com/v1"
	// ClientID and Scope are those of Nous's Hermes client.
	ClientID = "hermes-cli"
	Scope    = "inference:invoke"

	deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
	// userAgent avoids the bot protection that rejects Go's default agent.
	userAgent = "NeoProxy"
)

// DeviceCode is an issued device authorization.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	ExpiresIn       int
	Interval        int
}

// Token is an access token with its refresh token and absolute expiry.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// Client talks to the portal and inference API.
type Client struct {
	http *http.Client
}

// NewClient builds a proxy-aware client. proxyURL overrides cfg's proxy.
func NewClient(cfg *config.Config, proxyURL string) *Client {
	var sdk config.SDKConfig
	if cfg != nil {
		sdk = cfg.SDKConfig
		if strings.TrimSpace(proxyURL) == "" {
			proxyURL = cfg.ProxyURL
		}
	}
	sdk.ProxyURL = strings.TrimSpace(proxyURL)
	return &Client{http: util.SetProxy(&sdk, &http.Client{Timeout: 30 * time.Second})}
}

// SetHTTPClient replaces the transport (executors pass their proxy-aware one).
func (c *Client) SetHTTPClient(client *http.Client) {
	if client != nil {
		c.http = client
	}
}

func (c *Client) postForm(ctx context.Context, path string, fields url.Values, header http.Header) (int, map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, PortalURL+path, strings.NewReader(fields.Encode()))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body, nil
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func positive(m map[string]any, key string, fallback int) int {
	if n, ok := m[key].(float64); ok && n > 0 {
		return int(n)
	}
	return fallback
}

func portalError(status int, body map[string]any, what string) error {
	msg := fmt.Sprintf("%s (HTTP %d)", what, status)
	if code := str(body, "error"); code != "" {
		msg += ": " + code
	}
	if desc := firstNonEmpty(str(body, "error_description"), str(body, "message")); desc != "" {
		msg += ": " + desc
	}
	return errors.New(msg)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// RequestDeviceCode starts a device authorization.
func (c *Client) RequestDeviceCode(ctx context.Context) (*DeviceCode, error) {
	status, body, err := c.postForm(ctx, "/api/oauth/device/code", url.Values{"client_id": {ClientID}, "scope": {Scope}}, nil)
	if err != nil {
		return nil, fmt.Errorf("nous: device code request: %w", err)
	}
	if status != http.StatusOK {
		return nil, portalError(status, body, "nous: device code request failed")
	}
	verification := firstNonEmpty(str(body, "verification_uri_complete"), str(body, "verification_uri"))
	parsed, errParse := url.Parse(verification)
	if errParse != nil || parsed.Scheme != "https" || parsed.Hostname() != "portal.nousresearch.com" {
		return nil, errors.New("nous: the portal returned an unexpected verification URL")
	}
	dc := &DeviceCode{DeviceCode: str(body, "device_code"), UserCode: str(body, "user_code"), VerificationURL: parsed.String(),
		ExpiresIn: positive(body, "expires_in", 600), Interval: positive(body, "interval", 5)}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, errors.New("nous: the portal returned an incomplete device code")
	}
	return dc, nil
}

// WaitForToken polls until the person approves, the code expires or ctx ends.
func (c *Client) WaitForToken(ctx context.Context, dc *DeviceCode) (*Token, error) {
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	interval := min(dc.Interval, 30)
	for time.Now().Before(deadline) {
		status, body, err := c.postForm(ctx, "/api/oauth/token", url.Values{"grant_type": {deviceGrant}, "client_id": {ClientID}, "device_code": {dc.DeviceCode}}, nil)
		if err != nil {
			return nil, fmt.Errorf("nous: token poll: %w", err)
		}
		if status == http.StatusOK {
			return tokenFrom(body, "")
		}
		switch str(body, "error") {
		case "authorization_pending":
		case "slow_down":
			interval = min(interval+1, 30)
		default:
			return nil, portalError(status, body, "nous: device authorization failed")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
	}
	return nil, errors.New("nous: device authorization expired")
}

// Refresh exchanges a refresh token; the portal reads it from a header.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (*Token, error) {
	status, body, err := c.postForm(ctx, "/api/oauth/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {ClientID}},
		http.Header{"X-Nous-Refresh-Token": {refreshToken}})
	if err != nil {
		return nil, fmt.Errorf("nous: token refresh: %w", err)
	}
	if status != http.StatusOK {
		return nil, portalError(status, body, "nous: token refresh failed")
	}
	return tokenFrom(body, refreshToken)
}

// tokenFrom mirrors NeoPi: a missing refresh token keeps the previous one, and
// the expiry is brought forward by up to two minutes of skew.
func tokenFrom(body map[string]any, previousRefresh string) (*Token, error) {
	t := &Token{AccessToken: str(body, "access_token"), RefreshToken: firstNonEmpty(str(body, "refresh_token"), previousRefresh)}
	if t.AccessToken == "" || t.RefreshToken == "" {
		return nil, errors.New("nous: the token response omitted access_token or refresh_token")
	}
	ttl := positive(body, "expires_in", 3600)
	t.ExpiresAt = time.Now().Add(time.Duration(ttl-min(120, ttl/10)) * time.Second)
	return t, nil
}

// ListModels returns the text-capable model ids the token can use.
func (c *Client) ListModels(ctx context.Context, accessToken string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, InferenceURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nous: model list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nous: model list answered HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID           string `json:"id"`
			Architecture struct {
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("nous: model list: %w", err)
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		// Same filter as NeoPi: skip batch aliases and models without text output.
		if m.ID == "" || strings.HasSuffix(m.ID, ":batch") {
			continue
		}
		if mods := m.Architecture.OutputModalities; len(mods) > 0 && !contains(mods, "text") {
			continue
		}
		out = append(out, m.ID)
	}
	return out, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
