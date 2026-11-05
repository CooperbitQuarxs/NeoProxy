package githubcopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth clients: public github.com signs in with the minimal-grant OpenCode
// app; GitHub Enterprise instances run their own OAuth registry and keep
// the GitHub-owned Copilot CLI client.
const (
	openCodeClientID   = "Ov23li8tweQw6odWQebz"
	copilotCLIClientID = "Ov23ctDVkRmgkPke0Mmm"
	oauthScope         = "read:user"
	oauthUserAgent     = "copilot-developer-action/0.0.1"
)

const (
	initialPollMultiplier  = 1.2
	slowDownPollMultiplier = 1.4
)

// farFuture is FAR_FUTURE_MS: GitHub OAuth tokens are long-lived, so a
// credential never needs a refresh.
const farFuture = 10 * 365.25 * 24 * time.Hour

// Credential is NeoPi's GitHub Copilot OAuth credential: the GitHub token
// (used directly as the Copilot bearer), the Enterprise domain it was issued
// on and the plan-specific API endpoint.
type Credential struct {
	AccessToken   string
	EnterpriseURL string
	APIEndpoint   string
	Expires       time.Time
}

// DeviceCode is the device authorization response.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	Interval        int
	ExpiresIn       int
	// domain is the GitHub host the flow runs on; enterprise is the
	// credential's Enterprise domain.
	domain, enterprise string
}

// Error is an OAuthError: a sign-in failure with its HTTP status when one
// was answered.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// StatusCode reports the HTTP status the failure carried, 0 when none.
func (e *Error) StatusCode() int { return e.Status }

// Auth runs the GitHub Copilot sign-in on one HTTP client.
type Auth struct {
	client *http.Client
	now    func() time.Time
	wait   func(context.Context, time.Duration) error
	// githubAPI is https://api.github.com; tests point it at a loopback
	// server.
	githubAPI string
	// deviceBase builds a domain's OAuth base URL; tests point it at a
	// loopback server.
	deviceBase func(domain string) string
}

// NewAuth binds the flows to client.
func NewAuth(client *http.Client) *Auth {
	return &Auth{client: client, now: time.Now, wait: Wait, githubAPI: "https://api.github.com",
		deviceBase: func(domain string) string { return "https://" + domain }}
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

func clientID(domain string) string {
	if IsPublicGitHubHost(domain) {
		return openCodeClientID
	}
	return copilotCLIClientID
}

// encodeForm is URLSearchParams in insertion order.
func encodeForm(pairs ...string) string {
	var b strings.Builder
	for i := 0; i+1 < len(pairs); i += 2 {
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(pairs[i]))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(pairs[i+1]))
	}
	return b.String()
}

// postOAuth is fetchJson against a GitHub OAuth endpoint.
func (a *Auth) postOAuth(ctx context.Context, endpoint, body string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", oauthUserAgent)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), data)}
	}
	var parsed map[string]any
	if err = json.Unmarshal(data, &parsed); err != nil {
		return nil, &Error{Message: "Invalid device code response"}
	}
	return parsed, nil
}

// StartDeviceFlow is loginGitHubCopilot's first half: the Enterprise domain
// the user named (blank for github.com) and the device code on its host.
func (a *Auth) StartDeviceFlow(ctx context.Context, enterpriseInput string) (*DeviceCode, error) {
	trimmed := strings.TrimSpace(enterpriseInput)
	domain := NormalizeDomain(enterpriseInput)
	if trimmed != "" && domain == "" {
		return nil, &Error{Message: "Invalid GitHub Enterprise URL/domain"}
	}
	enterprise := NormalizeEnterpriseDomain(domain)
	if domain == "" || IsPublicGitHubHost(domain) {
		domain = "github.com"
	}
	data, err := a.postOAuth(ctx, a.deviceBase(domain)+"/login/device/code", encodeForm("client_id", clientID(domain), "scope", oauthScope))
	if err != nil {
		return nil, err
	}
	code := &DeviceCode{domain: domain, enterprise: enterprise}
	var ok1, ok2, ok3 bool
	code.DeviceCode, ok1 = data["device_code"].(string)
	code.UserCode, ok2 = data["user_code"].(string)
	code.VerificationURI, ok3 = data["verification_uri"].(string)
	interval, ok4 := data["interval"].(float64)
	expires, ok5 := data["expires_in"].(float64)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 {
		return nil, &Error{Message: "Invalid device code response fields"}
	}
	code.Interval, code.ExpiresIn = int(interval), int(expires)
	return code, nil
}

// PollForToken is pollForGitHubAccessToken followed by the plan endpoint
// discovery: the signed-in credential.
func (a *Auth) PollForToken(ctx context.Context, code *DeviceCode) (*Credential, error) {
	token, err := a.pollAccessToken(ctx, code)
	if err != nil {
		return nil, err
	}
	return &Credential{AccessToken: token, EnterpriseURL: code.enterprise, APIEndpoint: a.DiscoverAPIEndpoint(ctx, token), Expires: a.now().Add(farFuture)}, nil
}

func (a *Auth) pollAccessToken(ctx context.Context, code *DeviceCode) (string, error) {
	deadline := a.now().Add(time.Duration(code.ExpiresIn) * time.Second)
	interval := max(time.Second, time.Duration(code.Interval)*time.Second)
	multiplier := initialPollMultiplier
	slowDowns := 0
	endpoint := a.deviceBase(code.domain) + "/login/oauth/access_token"
	for a.now().Before(deadline) {
		remaining := deadline.Sub(a.now())
		delay := min(time.Duration(math.Ceil(float64(interval)*multiplier/float64(time.Millisecond)))*time.Millisecond, remaining)
		if err := a.wait(ctx, delay); err != nil {
			return "", &Error{Message: "Login cancelled"}
		}
		data, err := a.postOAuth(ctx, endpoint, encodeForm("client_id", clientID(code.domain), "device_code", code.DeviceCode,
			"grant_type", "urn:ietf:params:oauth:grant-type:device_code"))
		if err != nil {
			return "", err
		}
		if token, ok := data["access_token"].(string); ok {
			return token, nil
		}
		failure, ok := data["error"].(string)
		if !ok {
			continue
		}
		switch failure {
		case "authorization_pending":
			continue
		case "slow_down":
			slowDowns++
			if next, ok := data["interval"].(float64); ok && next > 0 {
				interval = max(time.Second, time.Duration(next)*time.Second)
			} else {
				interval = max(time.Second, interval+5*time.Second)
			}
			multiplier = slowDownPollMultiplier
			continue
		}
		suffix := ""
		if description, _ := data["error_description"].(string); description != "" {
			suffix = ": " + description
		}
		return "", &Error{Message: "Device flow failed: " + failure + suffix}
	}
	if slowDowns > 0 {
		return "", &Error{Message: "Device flow timed out after one or more slow_down responses. This is often caused by clock drift in WSL or VM environments. Please sync or restart the VM clock and try again."}
	}
	return "", &Error{Message: "Device flow timed out"}
}

// DiscoverAPIEndpoint is discoverGitHubCopilotApiEndpoint: the plan-specific
// Copilot API endpoint GitHub advertises for token, empty when unknown.
func (a *Auth) DiscoverAPIEndpoint(ctx context.Context, token string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.githubAPI+"/copilot_internal/user", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("User-Agent", CLIUserAgent)
	resp, err := a.client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var parsed struct {
		Endpoints map[string]any `json:"endpoints"`
	}
	if errDecode := json.NewDecoder(resp.Body).Decode(&parsed); errDecode != nil {
		return ""
	}
	endpoint, _ := parsed.Endpoints["api"].(string)
	return NormalizeAPIEndpoint(endpoint)
}

// EnableModels is enableAllGitHubCopilotModels: each model's policy is set to
// enabled, five at a time, as the chat surface unless integrationID pins
// another; a denied chat identity retries once as the CLI. It reports each
// model's outcome to progress when set.
func (a *Auth) EnableModels(ctx context.Context, credential *Credential, modelIDs []string, integrationID string, progress func(model string, ok bool)) {
	base := credential.APIEndpoint
	if base == "" {
		base = BaseURL(credential.EnterpriseURL)
	}
	identity := NormalizeIntegrationID(integrationID)
	for start := 0; start < len(modelIDs); start += 5 {
		var group sync.WaitGroup
		for _, id := range modelIDs[start:min(start+5, len(modelIDs))] {
			group.Add(1)
			go func(id string) {
				defer group.Done()
				ok := a.enableModel(ctx, base, credential.AccessToken, id, identity)
				if progress != nil {
					progress(id, ok)
				}
			}(id)
		}
		group.Wait()
	}
}

func (a *Auth) enableModel(ctx context.Context, base, token, id, identity string) bool {
	chosen := identity
	if chosen == "" {
		chosen = ChatIntegrationID
	}
	resp, err := a.postPolicy(ctx, base, token, id, chosen)
	if err != nil {
		return false
	}
	if identity == "" && chosen == ChatIntegrationID && identityDenied(resp) {
		_ = resp.Body.Close()
		if resp, err = a.postPolicy(ctx, base, token, id, CLIIntegrationID); err != nil {
			return false
		}
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (a *Auth) postPolicy(ctx context.Context, base, token, id, integrationID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/models/"+id+"/policy", bytes.NewReader([]byte(`{"state":"enabled"}`)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for name, values := range APIHeaders() {
		req.Header[name] = values
	}
	req.Header.Set("Copilot-Integration-Id", integrationID)
	req.Header.Set("Openai-Intent", "chat-policy")
	req.Header.Set("X-Initiator", "user")
	req.Header.Set("X-Interaction-Type", "chat-policy")
	return a.client.Do(req)
}

// identityDenied is isCopilotIdentityDenied: HTTP 403, or a 400 carrying
// code model_not_supported. It reads the body, so the caller retries or
// closes it.
func identityDenied(resp *http.Response) bool {
	if resp.StatusCode == http.StatusForbidden {
		return true
	}
	if resp.StatusCode != http.StatusBadRequest {
		return false
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil {
		return false
	}
	var parsed struct {
		Error struct {
			Code any `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(data, &parsed) == nil && parsed.Error.Code == "model_not_supported"
}
