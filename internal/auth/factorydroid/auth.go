package factorydroid

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"golang.org/x/sync/singleflight"
)

// The compiled `auth "factory-droid"` rule: WorkOS's public client, its
// device and token endpoints, each request bounded at 15 seconds.
const (
	ClientID               = "client_01HNM792M5G5G1A2THWPXKFMXB"
	DeviceAuthorizationURL = "https://api.workos.com/user_management/authorize/device"
	TokenURL               = "https://api.workos.com/user_management/authenticate"
	deviceCodeGrantType    = "urn:ietf:params:oauth:grant-type:device_code"
	requestTimeout         = 15 * time.Second
	// fallbackExpiry is the rule's `expires "jwt" fallback-ms`: a token
	// without an exp claim lives a day.
	fallbackExpiry = 24 * time.Hour
	// BunUserAgent is the user agent Bun's fetch supplies when NeoPi's
	// request names none.
	BunUserAgent = "Bun/1.3.14"
)

// RFC 8628 polling as pollOAuthDeviceCodeFlow paces it.
const (
	defaultPollInterval = 5 * time.Second
	minimumPollInterval = time.Second
	slowDownIncrement   = 5 * time.Second
)

// DeviceCode is the device authorization response's fields the rule reads.
type DeviceCode struct {
	UserCode                string `json:"user_code"`
	DeviceCode              string `json:"device_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                float64
	ExpiresIn               float64
}

// URL is where the user approves the code: the complete URI when WorkOS
// gives one.
func (d *DeviceCode) URL() string {
	if d.VerificationURIComplete != "" {
		return d.VerificationURIComplete
	}
	return d.VerificationURI
}

// Credential is NeoPi's Factory OAuth credential: the WorkOS tokens, the
// account identity, the WorkOS organization the grant is scoped to and the
// Factory account scope (organization, residency and inference region).
type Credential struct {
	AccessToken          string
	RefreshToken         string
	Expires              time.Time
	Email                string
	AccountID            string
	OrgID                string
	ActiveOrganizationID string
	Region               string
	InferenceRegion      string
}

// Error is an OAuthError: a sign-in or refresh failure with its HTTP status
// when the provider answered with one.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// StatusCode reports the provider's status for the conductor's 401
// handling; zero when none was received.
func (e *Error) StatusCode() int { return e.Status }

// Auth runs the Factory sign-in and refresh on one HTTP client.
type Auth struct {
	client *http.Client
	now    func() time.Time
	wait   func(context.Context, time.Duration) error
	// Hosts the requests go to; tests point them at a loopback server.
	deviceURL, tokenURL string
	apiBaseURL          func(region string) string
}

// NewAuth binds the flows to client.
func NewAuth(client *http.Client) *Auth {
	return &Auth{client: client, now: time.Now, wait: Wait, deviceURL: DeviceAuthorizationURL, tokenURL: TokenURL, apiBaseURL: registry.FactoryDroidAPIBaseURL}
}

// Wait sleeps for delay unless ctx ends first.
func Wait(ctx context.Context, delay time.Duration) error {
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

func encodeForm(fields []formField) string {
	escape := func(value string) string {
		value = strings.ReplaceAll(url.QueryEscape(value), "~", "%7E")
		return strings.ReplaceAll(value, "%2A", "*")
	}
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, escape(field.name)+"="+escape(field.value))
	}
	return strings.Join(parts, "&")
}

// do sends one Factory or WorkOS request under its own 15-second deadline
// and reads the decoded body.
func (a *Auth) do(ctx context.Context, req *http.Request) (int, []byte, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, nil, err
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

// postToken is postTokenRequest for the rule's form requests: the declared
// Accept header beside Bun's defaults.
func (a *Auth) postToken(ctx context.Context, endpoint, kind string, fields ...formField) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(encodeForm(fields)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", BunUserAgent)
	status, body, err := a.do(ctx, req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, nil, &Error{Message: "Timed out waiting for " + endpoint}
		}
		return 0, nil, &Error{Message: fmt.Sprintf("%s request to %s failed: %v", Provider, endpoint, err)}
	}
	if (status < 200 || status >= 300) && kind != "device-auth" {
		label := "token exchange"
		if kind == "token-refresh" {
			label = "token refresh"
		}
		return status, body, &Error{Status: status, Message: fmt.Sprintf("%s %s failed: %d %s", Provider, label, status, jsPrefix(string(body), 500))}
	}
	return status, body, nil
}

// StartDeviceFlow requests a device code.
func (a *Auth) StartDeviceFlow(ctx context.Context) (*DeviceCode, error) {
	status, body, err := a.postToken(ctx, a.deviceURL, "device-auth", formField{"client_id", ClientID})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, &Error{Status: status, Message: fmt.Sprintf("%s device authorization failed: %d %s", Provider, status, jsBody(body))}
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	code := &DeviceCode{
		UserCode:                stringAt(raw, "user_code"),
		DeviceCode:              stringAt(raw, "device_code"),
		VerificationURI:         stringAt(raw, "verification_uri"),
		VerificationURIComplete: stringAt(raw, "verification_uri_complete"),
	}
	code.Interval, _ = raw["interval"].(float64)
	code.ExpiresIn, _ = raw["expires_in"].(float64)
	if code.UserCode == "" || code.DeviceCode == "" || code.VerificationURI == "" {
		return nil, &Error{Message: Provider + " device authorization response missing required fields"}
	}
	return code, nil
}

// PollForToken is pollOAuthDeviceCodeFlow over the token endpoint, then the
// rule's credential map and its after-exchange hook.
func (a *Auth) PollForToken(ctx context.Context, code *DeviceCode) (*Credential, error) {
	if code == nil {
		return nil, errors.New("factory-droid device code: response is nil")
	}
	interval := defaultPollInterval
	if code.Interval != 0 && !math.IsNaN(code.Interval) && !math.IsInf(code.Interval, 0) {
		interval = time.Duration(math.Floor(code.Interval*1000)) * time.Millisecond
	}
	interval = max(interval, minimumPollInterval)
	var deadline time.Time
	if code.ExpiresIn != 0 && !math.IsNaN(code.ExpiresIn) && !math.IsInf(code.ExpiresIn, 0) {
		deadline = a.now().Add(time.Duration(code.ExpiresIn * float64(time.Second)))
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
		status, body, err := a.postToken(pollCtx, a.tokenURL, "device-auth",
			formField{"grant_type", deviceCodeGrantType}, formField{"client_id", ClientID}, formField{"device_code", code.DeviceCode})
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
			credential, err := a.credential(body, nil)
			if err != nil {
				return nil, err
			}
			return a.attachRegion(ctx, credential, body, nil, "login")
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
			return nil, &Error{Message: Provider + " device code expired; restart the login"}
		case "access_denied":
			return nil, &Error{Message: Provider + " device authorization was denied"}
		default:
			detail := ""
			if description, ok := payload["error_description"].(string); ok {
				detail = description
			} else if text, ok := errorCode.(string); ok {
				detail = text
			}
			return nil, &Error{Message: strings.TrimSpace(fmt.Sprintf("%s device token request failed: %d %s", Provider, status, detail))}
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
		return nil, &Error{Message: "Device flow timed out after one or more slow_down responses. This is often caused by clock drift in WSL or VM environments. Please sync or restart the VM clock and try again."}
	}
	return nil, &Error{Message: "Device flow timed out"}
}

var refreshGroup singleflight.Group

// Refresh is the rule's refresh-token grant, scoped to the WorkOS
// organization the credential selected, then its after-refresh hook.
// Concurrent refreshes of one token share a flight. Identity the response
// omits is left for the caller to merge from the stored record.
func (a *Auth) Refresh(ctx context.Context, stored *Credential) (*Credential, error) {
	if stored == nil {
		return nil, errors.New("factory-droid refresh: credential is nil")
	}
	key := stored.RefreshToken + "\x00" + stored.ActiveOrganizationID
	result, err, _ := refreshGroup.Do(key, func() (any, error) {
		return a.refresh(context.WithoutCancel(ctx), stored)
	})
	if err != nil {
		return nil, err
	}
	return result.(*Credential), nil
}

func (a *Auth) refresh(ctx context.Context, stored *Credential) (*Credential, error) {
	fields := []formField{{"grant_type", "refresh_token"}, {"client_id", ClientID}}
	if stored.RefreshToken != "" {
		fields = append(fields, formField{"refresh_token", stored.RefreshToken})
	}
	if stored.ActiveOrganizationID != "" {
		fields = append(fields, formField{"organization_id", stored.ActiveOrganizationID})
	}
	_, body, err := a.postToken(ctx, a.tokenURL, "token-refresh", fields...)
	if err != nil {
		return nil, err
	}
	credential, err := a.credential(body, stored)
	if err != nil {
		return nil, err
	}
	return a.attachRegion(ctx, credential, body, stored, "refresh")
}

// credential is mapCredentials for the rule: the access token, the refresh
// token (else the stored one), expiry from the access token's exp claim
// (else a day), and identity from the response or the token's claims.
func (a *Auth) credential(body []byte, previous *Credential) (*Credential, error) {
	var raw any
	if json.Unmarshal(body, &raw) != nil {
		raw = string(body)
	}
	access := scalarAt(raw, "access_token")
	if access == "" {
		excerpt := string(body)
		if _, ok := raw.(string); !ok {
			encoded, _ := json.Marshal(raw)
			excerpt = string(encoded)
		}
		return nil, &Error{Message: fmt.Sprintf("%s token response missing access token: %s", Provider, jsPrefix(excerpt, 500))}
	}
	claims := jwtClaims(access)
	credential := &Credential{AccessToken: access}
	if exp, ok := claims["exp"].(float64); ok && !math.IsNaN(exp) && !math.IsInf(exp, 0) {
		credential.Expires = time.UnixMilli(int64(exp * 1000))
	} else {
		credential.Expires = a.now().Add(fallbackExpiry)
	}
	credential.RefreshToken = scalarAt(raw, "refresh_token")
	if credential.RefreshToken == "" && previous != nil {
		credential.RefreshToken = previous.RefreshToken
	}
	credential.Email = firstNonEmpty(scalarAt(raw, "user.email"), scalar(claims["email"]))
	credential.AccountID = firstNonEmpty(scalarAt(raw, "user.id"), scalar(claims["sub"]))
	credential.OrgID = scalar(claims["external_org_id"])
	return credential, nil
}

// attachRegion is attachFactoryDroidRegion: it resolves the Factory
// organization, residency and inference region for the new bearer and,
// when nothing names an organization, scopes the grant to the account's
// WorkOS organization.
func (a *Auth) attachRegion(ctx context.Context, credential *Credential, body []byte, stored *Credential, phase string) (*Credential, error) {
	if ctx.Err() != nil {
		return nil, errors.New("Login cancelled")
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	if _, ok := raw["refresh_token"].(string); !ok || credential.RefreshToken == "" {
		return nil, &Error{Message: "Factory token response missing refresh token"}
	}
	if stored == nil {
		stored = &Credential{}
	}
	selectedOrg, _ := raw["organization_id"].(string)
	if _, ok := raw["organization_id"].(string); !ok {
		selectedOrg = stored.ActiveOrganizationID
	}
	factoryOrgChanged := credential.OrgID != "" && stored.OrgID != "" && credential.OrgID != stored.OrgID
	sameOrg := !factoryOrgChanged && (selectedOrg == "" || stored.ActiveOrganizationID == "" || selectedOrg == stored.ActiveOrganizationID)
	identity := *credential
	identity.ActiveOrganizationID = selectedOrg
	identity.Region, identity.InferenceRegion = "", ""
	if sameOrg {
		if identity.OrgID == "" {
			identity.OrgID = stored.OrgID
		}
		identity.Region, identity.InferenceRegion = stored.Region, stored.InferenceRegion
	}
	whoami, err := a.readWhoami(ctx, &identity, phase)
	if err != nil {
		return nil, err
	}
	if credential.OrgID == "" && !whoami.accepted && selectedOrg == "" {
		if workosOrg := a.lookupWorkOSOrganization(ctx, credential, identity.Region); workosOrg != "" {
			scopedStored := identity
			scopedStored.ActiveOrganizationID = workosOrg
			scoped, errScoped := a.refresh(ctx, &scopedStored)
			if errScoped != nil && ctx.Err() != nil {
				return nil, errors.New("Login cancelled")
			}
			if errScoped == nil && scoped != nil {
				if phase == "refresh" {
					return scoped, nil
				}
				confirmed, err := a.readWhoami(ctx, scoped, phase)
				if err != nil {
					return nil, err
				}
				if confirmed.failure != nil {
					return nil, confirmed.failure
				}
				resolved := *confirmed.identity
				resolved.Email = firstNonEmpty(resolved.Email, credential.Email)
				resolved.AccountID = firstNonEmpty(resolved.AccountID, credential.AccountID)
				return &resolved, nil
			}
		}
	}
	if whoami.failure != nil {
		return nil, whoami.failure
	}
	return whoami.identity, nil
}

type whoamiResult struct {
	identity *Credential
	accepted bool
	failure  error
}

// readWhoami reads /api/cli/whoami for the identity's bearer. accepted is
// true only when Factory answered with an organization; a sign-in left
// without one carries the error to report.
func (a *Auth) readWhoami(ctx context.Context, identity *Credential, phase string) (whoamiResult, error) {
	unresolved := func() error {
		if phase == "login" && identity.OrgID == "" {
			return &Error{Message: "Factory login did not resolve an organization"}
		}
		return nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, a.apiBaseURL(identity.Region)+"/api/cli/whoami", nil)
	if err != nil {
		return whoamiResult{}, err
	}
	setBunGetHeaders(req, identity.AccessToken)
	if identity.OrgID != "" {
		req.Header.Set("X-Factory-Org-Id", identity.OrgID)
	}
	status, body, err := a.do(reqCtx, req)
	if err != nil {
		if ctx.Err() != nil {
			return whoamiResult{}, errors.New("Login cancelled")
		}
		if phase == "login" {
			return whoamiResult{}, &Error{Message: fmt.Sprintf("%s request failed: %v", Provider, err)}
		}
		return whoamiResult{identity: identity, failure: unresolved()}, nil
	}
	ok := status >= 200 && status < 300
	if !ok && phase == "login" {
		return whoamiResult{identity: identity, failure: &Error{Status: status, Message: fmt.Sprintf("Factory identity check failed (%d): %s", status, string(body))}}, nil
	}
	if ok {
		var parsed map[string]any
		if json.Unmarshal(body, &parsed) == nil && parsed != nil {
			orgID := identity.OrgID
			if value, _ := parsed["orgId"].(string); value != "" {
				orgID = value
			}
			var carried *Credential
			if orgID == "" || identity.OrgID == "" || orgID == identity.OrgID {
				carried = identity
			}
			region := ""
			if value, _ := parsed["region"].(string); value == "eu" || value == "global" {
				region = value
			} else if carried != nil {
				region = carried.Region
			}
			inference := ""
			if value, _ := parsed["inferenceRegion"].(string); value == "global" || value == "eu" || value == "us" {
				inference = value
			} else {
				carriedInference := ""
				if carried != nil {
					carriedInference = carried.InferenceRegion
				}
				inference = registry.FactoryDroidInferenceRegion(region, carriedInference)
			}
			resolved := *identity
			resolved.OrgID, resolved.Region, resolved.InferenceRegion = orgID, region, inference
			result := whoamiResult{identity: &resolved, accepted: orgID != ""}
			if orgID == "" {
				result.failure = unresolved()
			}
			return result, nil
		}
	}
	return whoamiResult{identity: identity, failure: unresolved()}, nil
}

// lookupWorkOSOrganization reads the WorkOS organization a bearer without an
// organization claim belongs to from /api/cli/org; empty on any failure.
func (a *Auth) lookupWorkOSOrganization(ctx context.Context, credential *Credential, region string) string {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, a.apiBaseURL(region)+"/api/cli/org", nil)
	if err != nil {
		return ""
	}
	setBunGetHeaders(req, credential.AccessToken)
	status, body, err := a.do(reqCtx, req)
	if err != nil || status < 200 || status >= 300 {
		return ""
	}
	var parsed struct {
		WorkOSOrgIDs []any `json:"workosOrgIds"`
	}
	if json.Unmarshal(body, &parsed) != nil || len(parsed.WorkOSOrgIDs) == 0 {
		return ""
	}
	orgID, _ := parsed.WorkOSOrgIDs[0].(string)
	return orgID
}

// setBunGetHeaders sets a bearer GET's headers as NeoPi's fetch sends them.
func setBunGetHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", BunUserAgent)
}

// jwtClaims decodes a JWT payload without verification (decodeJwtPayload).
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(decoded, &claims) != nil {
		return nil
	}
	return claims
}

// scalarAt is scalarString(jsonPath(body, path)).
func scalarAt(body any, path string) string {
	current := body
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[segment]
	}
	return scalar(current)
}

// scalar is scalarString: a non-empty string, or a finite number as
// JavaScript prints it.
func scalar(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return ""
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}

func stringAt(raw map[string]any, key string) string {
	value, _ := raw[key].(string)
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// jsBody is the device-auth failure's body as NeoPi prints it: the text
// when it is not JSON, else its JSON encoding.
func jsBody(body []byte) string {
	var parsed any
	if len(body) == 0 || json.Unmarshal(body, &parsed) != nil {
		return string(body)
	}
	encoded, _ := json.Marshal(parsed)
	return string(encoded)
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
