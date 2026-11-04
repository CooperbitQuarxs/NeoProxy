// Package zai implements Z.ai's GLM Coding Plan sign-in.
//
// It mirrors ZCode's desktop "Individual Plan" browser sign-in: an
// authorization-code flow (no PKCE) against chat.z.ai, a JSON token exchange
// that yields a short-lived OAuth access token, and Z.ai's business APIs that
// provision a durable "id.secret" API key. Only that key is kept; it is used as
// a plain bearer key on the GLM Coding Plan endpoint and never needs refreshing.
package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// ClientID is ZCode's public OAuth client.
	ClientID = "client_P8X5CMWmlaRO9gyO-KSqtg"
	// AuthorizeURL is Z.ai's authorization endpoint.
	AuthorizeURL = "https://chat.z.ai/api/oauth/authorize"
	// TokenURL exchanges an authorization code for the short-lived OAuth token.
	TokenURL = "https://zcode.z.ai/api/v1/oauth/token"
	// RedirectURI is the only redirect Z.ai accepts for this client: ZCode's
	// desktop scheme. Loopback redirects are rejected, so the person pastes the
	// zcode:// address (or its code) back into NeoProxy.
	RedirectURI = "zcode://zai-auth/callback"
	// BizBaseURL hosts the business APIs that provision API keys.
	BizBaseURL = "https://api.z.ai"
	// CodingPlanBaseURL is the OpenAI-compatible GLM Coding Plan endpoint. The
	// general https://api.z.ai/api/paas/v4 endpoint bills pay-as-you-go balance
	// instead of the plan.
	CodingPlanBaseURL = "https://api.z.ai/api/coding/paas/v4"
	// KeyName names the API key NeoProxy provisions, so signing in never touches
	// ZCode's or NeoPi's own keys and signing in again reuses it.
	KeyName = "neoproxy"
)

// Service performs the sign-in exchanges. Base URLs are fields so tests can
// point them at a fake upstream.
type Service struct {
	client       *http.Client
	TokenURL     string
	BizBaseURL   string
	ModelsBase   string
	AuthorizeURL string
}

// NewService returns a Service using client (a 30s-timeout client when nil).
func NewService(client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{client: client, TokenURL: TokenURL, BizBaseURL: BizBaseURL, ModelsBase: CodingPlanBaseURL, AuthorizeURL: AuthorizeURL}
}

// AuthorizationURL builds the page the person signs in on.
func (s *Service) AuthorizationURL(state string) string {
	q := url.Values{}
	q.Set("client_id", ClientID)
	q.Set("response_type", "code")
	q.Set("redirect_uri", RedirectURI)
	q.Set("state", state)
	return s.AuthorizeURL + "?" + q.Encode()
}

// Account identifies the signed-in Z.ai user.
type Account struct {
	Email string
	ID    string
}

// ExchangeCode trades an authorization code for the short-lived OAuth token.
func (s *Service) ExchangeCode(ctx context.Context, code, state string) (string, Account, error) {
	body := map[string]string{"provider": "zai", "code": code, "redirect_uri": RedirectURI, "state": state}
	var data struct {
		Zai struct {
			AccessToken string `json:"access_token"`
		} `json:"zai"`
		User struct {
			Email string          `json:"email"`
			ID    json.RawMessage `json:"id"`
		} `json:"user"`
	}
	if err := s.do(ctx, http.MethodPost, s.TokenURL, "", body, "token exchange", &data); err != nil {
		return "", Account{}, err
	}
	token := strings.TrimSpace(data.Zai.AccessToken)
	if token == "" {
		return "", Account{}, errors.New("z.ai token exchange returned no access token")
	}
	return token, Account{Email: strings.TrimSpace(data.User.Email), ID: rawID(data.User.ID)}, nil
}

// MintAPIKey provisions (or reuses) NeoProxy's durable API key from the OAuth
// token: business login, default organization and project, find or create the
// key named KeyName, then read its full secret.
func (s *Service) MintAPIKey(ctx context.Context, oauthToken string) (string, error) {
	var login struct {
		AccessToken  string `json:"access_token"`
		AccessToken2 string `json:"accessToken"`
	}
	if err := s.do(ctx, http.MethodPost, s.BizBaseURL+"/api/auth/z/login", "", map[string]string{"token": oauthToken}, "business login", &login); err != nil {
		return "", err
	}
	biz := strings.TrimSpace(firstNonEmpty(login.AccessToken, login.AccessToken2))
	if biz == "" {
		return "", errors.New("z.ai business login returned no access token")
	}
	var customer struct {
		Organizations []struct {
			OrganizationID string `json:"organizationId"`
			IsDefault      bool   `json:"isDefault"`
			Projects       []struct {
				ProjectID string `json:"projectId"`
				IsDefault bool   `json:"isDefault"`
			} `json:"projects"`
		} `json:"organizations"`
	}
	if err := s.do(ctx, http.MethodGet, s.BizBaseURL+"/api/biz/customer/getCustomerInfo", biz, nil, "customer lookup", &customer); err != nil {
		return "", err
	}
	orgIndex, projectID := -1, ""
	for i, org := range customer.Organizations {
		if org.IsDefault || orgIndex < 0 {
			orgIndex = i
		}
		if org.IsDefault {
			break
		}
	}
	if orgIndex >= 0 {
		for i, p := range customer.Organizations[orgIndex].Projects {
			if p.IsDefault || i == 0 {
				projectID = strings.TrimSpace(p.ProjectID)
			}
			if p.IsDefault {
				break
			}
		}
	}
	if orgIndex < 0 || strings.TrimSpace(customer.Organizations[orgIndex].OrganizationID) == "" || projectID == "" {
		return "", errors.New("z.ai key provisioning failed: no organization or project on the account")
	}
	keysURL := s.BizBaseURL + "/api/biz/v1/organization/" + url.PathEscape(strings.TrimSpace(customer.Organizations[orgIndex].OrganizationID)) + "/projects/" + url.PathEscape(projectID) + "/api_keys"
	var listed json.RawMessage
	if err := s.do(ctx, http.MethodGet, keysURL, biz, nil, "api key list", &listed); err != nil {
		return "", err
	}
	apiKey := ""
	for _, k := range keyRecords(listed) {
		if k.Name == KeyName {
			apiKey = strings.TrimSpace(k.APIKey)
			break
		}
	}
	if apiKey == "" {
		var created keyRecord
		if err := s.do(ctx, http.MethodPost, keysURL, biz, map[string]string{"name": KeyName}, "api key create", &created); err != nil {
			return "", err
		}
		apiKey = strings.TrimSpace(created.APIKey)
	}
	if apiKey == "" {
		return "", errors.New("z.ai key provisioning returned no api key")
	}
	// List entries mask the secret; the copy endpoint always returns it whole.
	var copied struct {
		SecretKey string `json:"secretKey"`
	}
	if err := s.do(ctx, http.MethodGet, keysURL+"/copy/"+url.PathEscape(apiKey), biz, nil, "api key copy", &copied); err != nil {
		return "", err
	}
	secret := strings.TrimSpace(copied.SecretKey)
	if secret == "" {
		return "", errors.New("z.ai key provisioning returned no secret")
	}
	return apiKey + "." + secret, nil
}

// ListModels returns the chat model ids the key can use on the Coding Plan endpoint.
func (s *Service) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.ModelsBase, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("z.ai model list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("z.ai model list: HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("z.ai model list: %w", err)
	}
	models := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return nil, errors.New("z.ai lists no models for this key")
	}
	return models, nil
}

type keyRecord struct {
	Name   string `json:"name"`
	APIKey string `json:"apiKey"`
}

// keyRecords accepts a bare array or the wrapper shapes Z.ai uses for lists.
func keyRecords(raw json.RawMessage) []keyRecord {
	var list []keyRecord
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var wrapped map[string]json.RawMessage
	if json.Unmarshal(raw, &wrapped) != nil {
		return nil
	}
	for _, field := range []string{"list", "keys", "apiKeys", "records"} {
		if inner, ok := wrapped[field]; ok && json.Unmarshal(inner, &list) == nil {
			return list
		}
	}
	return nil
}

// do sends one JSON request and unwraps Z.ai's {code,msg,data,success}
// envelope into out. The token endpoint signals success with code 0, the
// business APIs with code 200 / success true. Error text never includes
// response bodies, which can carry tokens or keys.
func (s *Service) do(ctx context.Context, method, target, bearer string, body any, operation string, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("z.ai %s: %w", operation, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("z.ai %s: %w", operation, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("z.ai %s: HTTP %d", operation, resp.StatusCode)
	}
	data, err := unwrapEnvelope(raw)
	if err != nil {
		return fmt.Errorf("z.ai %s failed: %w", operation, err)
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("z.ai %s: unexpected response", operation)
	}
	return nil
}

func unwrapEnvelope(raw []byte) (json.RawMessage, error) {
	var env struct {
		Code    json.RawMessage `json:"code"`
		Msg     string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
		Success *bool           `json:"success"`
	}
	if json.Unmarshal(raw, &env) != nil || (env.Code == nil && env.Success == nil) {
		return raw, nil
	}
	if (env.Success != nil && !*env.Success) || !successCode(env.Code) {
		if env.Msg != "" {
			return nil, errors.New(env.Msg)
		}
		return nil, fmt.Errorf("code %s", string(env.Code))
	}
	if env.Data == nil {
		return raw, nil
	}
	return env.Data, nil
}

func successCode(code json.RawMessage) bool {
	if code == nil || string(code) == "null" {
		return true
	}
	text := strings.Trim(string(code), `"`)
	n, err := strconv.Atoi(text)
	return err == nil && (n == 0 || n == 200)
}

func rawID(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
