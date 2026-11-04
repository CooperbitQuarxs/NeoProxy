// Package kimi is NeoPi's Kimi Code sign-in (auth rule kimi-code): the device
// authorization grant against auth.kimi.com and the refresh-token grant, both
// carrying the kimi-fingerprint headers.
package kimi

import (
	"context"
	"encoding/base64"
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
	"golang.org/x/sync/singleflight"
)

const (
	// ClientID is the Kimi CLI's public OAuth client.
	ClientID = "17e5f671-d194-4dfb-9706-5516cb48c098"
	// OAuthBaseURL hosts the device and token endpoints.
	OAuthBaseURL = "https://auth.kimi.com"
	// DeviceAuthorizationURL starts the device grant.
	DeviceAuthorizationURL = OAuthBaseURL + "/api/oauth/device_authorization"
	// TokenURL serves the device and refresh grants.
	TokenURL = OAuthBaseURL + "/api/oauth/token"
	// APIBaseURL is the Kimi Code Anthropic Messages base (the OpenAI base
	// without its /v1 suffix).
	APIBaseURL = "https://api.kimi.com/coding"
	// ModelsURL is Kimi Code's model discovery endpoint.
	ModelsURL = APIBaseURL + "/v1/models"
	// DeviceCodeGrantType is the OAuth2 device authorization grant (RFC 8628).
	DeviceCodeGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	// providerID names the provider in NeoPi's messages.
	providerID = "kimi-code"

	defaultPollInterval = 5 * time.Second
	minimumPollInterval = time.Second
	slowDownIncrement   = 5 * time.Second
	requestTimeout      = 30 * time.Second
	// expiryMargin is how much earlier than the server's expires_in NeoPi
	// treats a token as expired.
	expiryMargin = 5 * time.Minute
)

// DeviceCodeResponse is the device authorization response.
type DeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Credential is NeoPi's stored OAuth credential.
type Credential struct {
	AccessToken  string
	RefreshToken string
	Expires      time.Time
	AccountID    string
}

// Auth runs the sign-in and refresh grants.
type Auth struct {
	httpClient *http.Client
	authDir    string
	now        func() time.Time
	// wait sleeps between polls; tests record the requested waits.
	wait func(ctx context.Context, delay time.Duration) error
}

var refreshGroup singleflight.Group

// NewAuth uses the configured proxy (or proxyURL when set) and the config's
// auth directory for the device id.
func NewAuth(cfg *config.Config, proxyURL string) *Auth {
	var sdkCfg config.SDKConfig
	authDir := ""
	effective := strings.TrimSpace(proxyURL)
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		authDir = cfg.AuthDir
		if effective == "" {
			effective = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effective
	return NewAuthWithHTTPClient(util.SetProxy(&sdkCfg, &http.Client{}), authDir)
}

// NewAuthWithHTTPClient sends every request through client.
func NewAuthWithHTTPClient(client *http.Client, authDir string) *Auth {
	return &Auth{httpClient: client, authDir: authDir, now: time.Now, wait: sleepContext}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// formField is one URLSearchParams entry; order is preserved on the wire.
type formField struct{ name, value string }

func encodeForm(fields ...formField) string {
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		escape := func(value string) string {
			value = strings.ReplaceAll(url.QueryEscape(value), "~", "%7E")
			return strings.ReplaceAll(value, "%2A", "*")
		}
		parts = append(parts, escape(field.name)+"="+escape(field.value))
	}
	return strings.Join(parts, "&")
}

// postForm is postTokenRequest: a form POST with Bun's defaults and the
// fingerprint headers.
func (a *Auth) postForm(ctx context.Context, endpoint string, fields ...formField) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encodeForm(fields...)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	for name, values := range CommonHeaders(a.authDir) {
		req.Header[name] = values
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, nil, fmt.Errorf("Timed out waiting for %s", endpoint)
		}
		return 0, nil, fmt.Errorf("%s request to %s failed: %w", providerID, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = decoded.Close() }()
	body, err := io.ReadAll(io.LimitReader(decoded, 1<<20))
	return resp.StatusCode, body, err
}

// StartDeviceFlow requests a device code.
func (a *Auth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	status, body, err := a.postForm(ctx, DeviceAuthorizationURL, formField{"client_id", ClientID})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("%s device authorization failed: %d %s", providerID, status, strings.TrimSpace(string(body)))
	}
	var deviceCode DeviceCodeResponse
	_ = json.Unmarshal(body, &deviceCode)
	if deviceCode.UserCode == "" || deviceCode.DeviceCode == "" || deviceCode.VerificationURI == "" {
		return nil, fmt.Errorf("%s device authorization response missing required fields", providerID)
	}
	return &deviceCode, nil
}

// PollForToken is pollOAuthDeviceCodeFlow: the first poll is immediate,
// slow_down adds five seconds, and the code's expiry ends it.
func (a *Auth) PollForToken(ctx context.Context, deviceCode *DeviceCodeResponse) (*Credential, error) {
	if deviceCode == nil {
		return nil, errors.New("kimi device code: response is nil")
	}
	interval := defaultPollInterval
	if deviceCode.Interval > 0 {
		interval = time.Duration(deviceCode.Interval) * time.Second
	}
	interval = max(interval, minimumPollInterval)
	var deadline time.Time
	if deviceCode.ExpiresIn > 0 {
		deadline = a.now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
	}
	expired := func() bool { return !deadline.IsZero() && !a.now().Before(deadline) }
	slowDowns := 0
	for !expired() {
		if ctx.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		pollCtx, cancel := ctx, context.CancelFunc(func() {})
		if !deadline.IsZero() {
			pollCtx, cancel = context.WithDeadline(ctx, deadline)
		}
		status, body, err := a.postForm(pollCtx, TokenURL,
			formField{"grant_type", DeviceCodeGrantType}, formField{"client_id", ClientID}, formField{"device_code", deviceCode.DeviceCode})
		cancel()
		if ctx.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		if err != nil {
			if expired() {
				break
			}
			return nil, err
		}
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		errorCode, hasError := payload["error"]
		if status >= 200 && status < 300 && !hasError {
			return a.credential(body, "")
		}
		if expired() {
			break
		}
		switch errorCode {
		case "authorization_pending":
		case "slow_down":
			slowDowns++
			interval = max(minimumPollInterval, interval+slowDownIncrement)
		case "expired_token":
			return nil, fmt.Errorf("%s device code expired; restart the login", providerID)
		case "access_denied":
			return nil, fmt.Errorf("%s device authorization was denied", providerID)
		default:
			detail := ""
			if description, ok := payload["error_description"].(string); ok {
				detail = description
			} else if code, ok := errorCode.(string); ok {
				detail = code
			}
			return nil, errors.New(strings.TrimSpace(fmt.Sprintf("%s device token request failed: %d %s", providerID, status, detail)))
		}
		wait := interval
		if !deadline.IsZero() {
			if wait = min(wait, deadline.Sub(a.now())); wait <= 0 {
				break
			}
		}
		if a.wait(ctx, wait) != nil {
			return nil, errors.New("Login cancelled")
		}
	}
	if slowDowns > 0 {
		return nil, errors.New("Device flow timed out after one or more slow_down responses. This is often caused by clock drift in WSL or VM environments. Please sync or restart the VM clock and try again.")
	}
	return nil, errors.New("Device flow timed out")
}

// Refresh is the auth rule's refresh-token grant. Concurrent refreshes of one
// token share a flight.
func (a *Auth) Refresh(ctx context.Context, refreshToken string) (*Credential, error) {
	result, err, _ := refreshGroup.Do(refreshToken, func() (any, error) {
		ctx := context.WithoutCancel(ctx)
		status, body, err := a.postForm(ctx, TokenURL, formField{"grant_type", "refresh_token"}, formField{"client_id", ClientID}, formField{"refresh_token", refreshToken})
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, &RefreshError{Status: status, Message: fmt.Sprintf("%s token refresh failed: %d %s", providerID, status, jsPrefix(string(body), 500))}
		}
		return a.credential(body, refreshToken)
	})
	if err != nil {
		return nil, err
	}
	return result.(*Credential), nil
}

// RefreshError is a non-2xx refresh-token grant.
type RefreshError struct {
	Status  int
	Message string
}

func (e *RefreshError) Error() string { return e.Message }

// credential is mapCredentials for the kimi-code rule: access, refresh (the
// stored one when not rotated), expiry five minutes early, and the account id
// from the access token's user_id or sub claim.
func (a *Auth) credential(body []byte, previousRefresh string) (*Credential, error) {
	var tokens struct {
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		ExpiresIn    *float64 `json:"expires_in"`
	}
	if json.Unmarshal(body, &tokens) != nil || tokens.AccessToken == "" {
		return nil, fmt.Errorf("%s token response missing access_token", providerID)
	}
	if tokens.ExpiresIn == nil {
		return nil, fmt.Errorf("%s token response missing expires_in", providerID)
	}
	refresh := tokens.RefreshToken
	if refresh == "" {
		refresh = previousRefresh
	}
	expires := a.now().Add(time.Duration(*tokens.ExpiresIn*float64(time.Second)) - expiryMargin)
	return &Credential{AccessToken: tokens.AccessToken, RefreshToken: refresh, Expires: expires, AccountID: AccountID(tokens.AccessToken)}, nil
}

// AccountID reads the user_id, else sub, claim of a JWT access token.
func AccountID(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	for _, name := range []string{"user_id", "sub"} {
		if value, ok := claims[name].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// jsPrefix is String.prototype.slice(0, n) over UTF-16 code units.
func jsPrefix(text string, n int) string {
	units := 0
	for index, r := range text {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > n {
			return text[:index]
		}
		units += width
	}
	return text
}
