package xai

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
	"unicode/utf16"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

// XAIAuth runs NeoPi's xai-oauth auth rule (catalog auth/xai-oauth.kdl through
// the registry engines device-code.ts and refresh.ts): RFC 8628 device login
// against auth.x.ai, OIDC-discovered token endpoint, userinfo identity, and
// refresh-token grants.
type XAIAuth struct {
	httpClient *http.Client
	// minPollInterval is MINIMUM_DEVICE_FLOW_INTERVAL_MS; tests lower it.
	minPollInterval time.Duration
	now             func() time.Time
	// sleep waits between polls; tests record the requested waits.
	sleep func(ctx context.Context, delay time.Duration) error
}

var xaiRefreshGroup singleflight.Group

// NewXAIAuth creates an xAI OAuth helper using config proxy settings.
func NewXAIAuth(cfg *config.Config) *XAIAuth {
	return NewXAIAuthWithProxyURL(cfg, "")
}

// NewXAIAuthWithProxyURL creates an xAI OAuth helper with an explicit proxy URL.
func NewXAIAuthWithProxyURL(cfg *config.Config, proxyURL string) *XAIAuth {
	effectiveProxyURL := strings.TrimSpace(proxyURL)
	var sdkCfg config.SDKConfig
	if cfg != nil {
		sdkCfg = cfg.SDKConfig
		if effectiveProxyURL == "" {
			effectiveProxyURL = strings.TrimSpace(cfg.ProxyURL)
		}
	}
	sdkCfg.ProxyURL = effectiveProxyURL
	return NewXAIAuthWithHTTPClient(util.SetProxy(&sdkCfg, &http.Client{}))
}

// NewXAIAuthWithHTTPClient creates an xAI OAuth helper that sends every
// discovery, device and token request through client.
func NewXAIAuthWithHTTPClient(client *http.Client) *XAIAuth {
	return &XAIAuth{httpClient: client, minPollInterval: minimumPollInterval, now: time.Now}
}

func (a *XAIAuth) clock() time.Time {
	if a.now != nil {
		return a.now()
	}
	return time.Now()
}

func (a *XAIAuth) wait(ctx context.Context, delay time.Duration) error {
	if a.sleep != nil {
		return a.sleep(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ValidateOAuthEndpoint is validateXAIEndpoint: the discovered endpoint must
// be HTTPS on x.ai or a subdomain, since it receives every refresh token.
func ValidateOAuthEndpoint(rawURL string, field string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return "", fmt.Errorf("Invalid xAI %s: %s", field, rawURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || (host != "x.ai" && !strings.HasSuffix(host, ".x.ai")) {
		return "", fmt.Errorf("Invalid xAI %s: %s", field, rawURL)
	}
	return rawURL, nil
}

// Discover is xaiOAuthDiscovery: the OIDC document's validated token endpoint.
// The device endpoint is fixed by the auth rule, not discovered.
func (a *XAIAuth) Discover(ctx context.Context) (*Discovery, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DiscoveryURL, nil)
	if err != nil {
		return nil, fmt.Errorf("xAI OIDC discovery failed: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	status, body, err := a.fetch(req)
	if err != nil {
		return nil, fmt.Errorf("xAI OIDC discovery failed: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("xAI OIDC discovery returned status %d.", status)
	}
	var payload map[string]any
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("xAI OIDC discovery returned invalid JSON: %w", err)
	}
	if payload == nil {
		return nil, errors.New("xAI OIDC discovery response was not a JSON object.")
	}
	tokenEndpoint, _ := payload["token_endpoint"].(string)
	tokenEndpoint = strings.TrimSpace(tokenEndpoint)
	if tokenEndpoint == "" {
		return nil, errors.New("xAI OIDC discovery response was missing token_endpoint.")
	}
	if _, err = ValidateOAuthEndpoint(tokenEndpoint, "token_endpoint"); err != nil {
		return nil, err
	}
	return &Discovery{DeviceAuthorizationEndpoint: DeviceAuthorizationURL, TokenEndpoint: tokenEndpoint}, nil
}

// fetch sends req with Bun fetch's identity (its default Accept, encodings
// and user agent where the rule sets none) and returns the decoded body.
func (a *XAIAuth) fetch(req *http.Request) (int, []byte, error) {
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", "Bun/1.3.14")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer closeBody(resp)
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return 0, nil, err
	}
	body, err := io.ReadAll(decoded)
	return resp.StatusCode, body, err
}

// formField is one URLSearchParams entry; order is preserved on the wire.
type formField struct{ name, value string }

// encodeForm is URLSearchParams.toString: insertion order, form escaping.
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

// postForm is postTokenRequest: a form POST with the rule's headers. Unlike a
// refresh or exchange, a device-auth call returns non-2xx bodies to the caller.
func (a *XAIAuth) postForm(ctx context.Context, endpoint string, accept bool, fields ...formField) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encodeForm(fields...)))
	if err != nil {
		return 0, nil, err
	}
	if accept {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	status, body, err := a.fetch(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return 0, nil, fmt.Errorf("Timed out waiting for %s", endpoint)
		}
		return 0, nil, fmt.Errorf("%s request to %s failed: %w", providerID, endpoint, err)
	}
	return status, body, nil
}

// StartDeviceFlow requests a device code, then resolves the token endpoint the
// poll will use, in NeoPi's order.
func (a *XAIAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	status, body, err := a.postForm(ctx, DeviceAuthorizationURL, true, formField{"client_id", ClientID}, formField{"scope", Scope})
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

// WaitForAuthorization polls the device grant and resolves the identity.
func (a *XAIAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*AuthBundle, error) {
	tokenData, err := a.PollForToken(ctx, deviceCode)
	if err != nil {
		return nil, err
	}
	return &AuthBundle{
		TokenData:     *tokenData,
		LastRefresh:   a.clock().UTC().Format(time.RFC3339),
		BaseURL:       DefaultAPIBaseURL,
		TokenEndpoint: deviceCode.TokenEndpoint,
	}, nil
}

// PollForToken is pollOAuthDeviceCodeFlow over the device grant: the first
// poll is immediate, slow_down adds five seconds, and the code's expiry ends it.
func (a *XAIAuth) PollForToken(ctx context.Context, deviceCode *DeviceCodeResponse) (*TokenData, error) {
	if deviceCode == nil {
		return nil, errors.New("xai device code: response is nil")
	}
	// The token endpoint is resolved once, after the code is shown, as
	// createDeviceCodeLogin does before polling.
	if deviceCode.TokenEndpoint == "" {
		discovery, err := a.Discover(ctx)
		if err != nil {
			return nil, err
		}
		deviceCode.TokenEndpoint = discovery.TokenEndpoint
	}
	minInterval := a.minPollInterval
	if minInterval <= 0 {
		minInterval = minimumPollInterval
	}
	interval := defaultPollInterval
	if deviceCode.Interval > 0 {
		interval = time.Duration(deviceCode.Interval) * time.Second
	}
	interval = max(interval, minInterval)
	var deadline time.Time
	if deviceCode.ExpiresIn > 0 {
		deadline = a.clock().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
	}
	expired := func() bool { return !deadline.IsZero() && !a.clock().Before(deadline) }
	slowDowns := 0
	for !expired() {
		if ctx.Err() != nil {
			return nil, errors.New("Login cancelled")
		}
		pollCtx, cancel := ctx, context.CancelFunc(func() {})
		if !deadline.IsZero() {
			pollCtx, cancel = context.WithDeadline(ctx, deadline)
		}
		status, body, err := a.postForm(pollCtx, deviceCode.TokenEndpoint, true,
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
			return a.credentials(ctx, body, "")
		}
		if expired() {
			break
		}
		switch errorCode {
		case "authorization_pending":
		case "slow_down":
			slowDowns++
			interval = max(minInterval, interval+slowDownIncrement)
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
			wait = min(wait, deadline.Sub(a.clock()))
			if wait <= 0 {
				break
			}
		}
		if errSleep := a.wait(ctx, wait); errSleep != nil {
			return nil, errors.New("Login cancelled")
		}
	}
	if slowDowns > 0 {
		return nil, errors.New("Device flow timed out after one or more slow_down responses. This is often caused by clock drift in WSL or VM environments. Please sync or restart the VM clock and try again.")
	}
	return nil, errors.New("Device flow timed out")
}

// RefreshTokens is the auth rule's refresh-token grant against the freshly
// discovered token endpoint. Concurrent refreshes of one token share a flight.
func (a *XAIAuth) RefreshTokens(ctx context.Context, refreshToken string) (*TokenData, error) {
	result, err, _ := xaiRefreshGroup.Do(refreshToken, func() (interface{}, error) {
		return a.refresh(context.WithoutCancel(ctx), refreshToken)
	})
	if err != nil {
		return nil, err
	}
	return result.(*TokenData), nil
}

func (a *XAIAuth) refresh(ctx context.Context, refreshToken string) (*TokenData, error) {
	discovery, err := a.Discover(ctx)
	if err != nil {
		return nil, err
	}
	status, body, err := a.postForm(ctx, discovery.TokenEndpoint, false,
		formField{"grant_type", "refresh_token"}, formField{"client_id", ClientID}, formField{"refresh_token", refreshToken})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &RefreshError{Status: status, Message: fmt.Sprintf("%s token refresh failed: %d %s", providerID, status, jsSlice(string(body), 500))}
	}
	return a.credentials(ctx, body, refreshToken)
}

// RefreshError is a non-2xx refresh-token grant.
type RefreshError struct {
	Status  int
	Message string
}

func (e *RefreshError) Error() string { return e.Message }

// credentials is mapCredentials followed by applyUserinfo: the access token,
// the rotated (or kept) refresh token, expires_in less the five-minute skew,
// the access token's sub claim, then userinfo's email and sub.
func (a *XAIAuth) credentials(ctx context.Context, body []byte, previousRefresh string) (*TokenData, error) {
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	access := scalarString(payload["access_token"])
	if access == "" {
		return nil, fmt.Errorf("%s token response missing access token: %s", providerID, jsSlice(string(body), 500))
	}
	seconds, ok := payload["expires_in"].(float64)
	if !ok {
		return nil, fmt.Errorf("%s token response missing expires_in", providerID)
	}
	refresh := scalarString(payload["refresh_token"])
	if refresh == "" {
		refresh = previousRefresh
	}
	expires := a.clock().Add(time.Duration(seconds * float64(time.Second)))
	data := &TokenData{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    scalarString(payload["token_type"]),
		ExpiresIn:    int(seconds),
		Expire:       expires.UTC().Format(time.RFC3339),
		Subject:      scalarString(jwtClaims(access)["sub"]),
	}
	a.applyUserinfo(ctx, data)
	return data, nil
}

// applyUserinfo is the rule's userinfo GET; failures leave identity unset.
func (a *XAIAuth) applyUserinfo(ctx context.Context, data *TokenData) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, UserinfoURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+data.AccessToken)
	status, body, err := a.fetch(req)
	if err != nil || status < 200 || status >= 300 {
		return
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return
	}
	if email := scalarString(payload["email"]); email != "" {
		data.Email = email
	}
	if sub := scalarString(payload["sub"]); sub != "" {
		data.Subject = sub
	}
}

// CreateTokenStorage converts an auth bundle into persistable storage.
func (a *XAIAuth) CreateTokenStorage(bundle *AuthBundle) *TokenStorage {
	if bundle == nil {
		return nil
	}
	baseURL := bundle.BaseURL
	if baseURL == "" {
		baseURL = DefaultAPIBaseURL
	}
	return &TokenStorage{
		Type:          "xai",
		AccessToken:   bundle.TokenData.AccessToken,
		RefreshToken:  bundle.TokenData.RefreshToken,
		TokenType:     bundle.TokenData.TokenType,
		ExpiresIn:     bundle.TokenData.ExpiresIn,
		Expire:        bundle.TokenData.Expire,
		LastRefresh:   bundle.LastRefresh,
		Email:         bundle.TokenData.Email,
		Subject:       bundle.TokenData.Subject,
		BaseURL:       baseURL,
		RedirectURI:   bundle.RedirectURI,
		TokenEndpoint: bundle.TokenEndpoint,
		AuthKind:      "oauth",
	}
}

// scalarString is the engine's scalarString: non-empty strings and finite numbers.
func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return fmt.Sprint(typed)
	}
	return ""
}

// jwtClaims decodes a three-part JWT payload without verification.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}

// jsSlice is String.prototype.slice(0, n) over UTF-16 code units.
func jsSlice(value string, n int) string {
	units := utf16.Encode([]rune(value))
	if len(units) <= n {
		return value
	}
	return string(utf16.Decode(units[:n]))
}

func closeBody(resp *http.Response) {
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("xai oauth: close response body error: %v", errClose)
	}
}
