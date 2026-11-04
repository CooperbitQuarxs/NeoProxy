// Package zai is NeoPi's Z.ai "GLM Coding Plan · Sign in" (auth rule
// zai-coding-plan, hook zai-mint-key).
//
// It mirrors ZCode's desktop "Individual Plan" browser sign-in: an
// authorization-code flow (no PKCE) against chat.z.ai, a JSON token exchange
// that yields a short-lived OAuth access token, and Z.ai's business APIs that
// provision a durable "id.secret" API key. Only that key is kept; it is a
// plain bearer key that never needs refreshing.
package zai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
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
	// CodingPlanBaseURL is the GLM Coding Plan Chat Completions endpoint that
	// identifies a Z.ai account's openai-compatibility entry.
	CodingPlanBaseURL = "https://api.z.ai/api/coding/paas/v4"
	// KeyName is NeoPi's own key name, so signing in never mutates ZCode's
	// `zcode-api-key` and signing in again reuses the key.
	KeyName = "oh-my-pi"
	// authProvider names the auth rule in NeoPi's token-exchange errors.
	authProvider = "zai-coding-plan"
	// userAgent is Bun's fetch default, which NeoPi's sign-in requests carry.
	userAgent      = "Bun/1.3.14"
	requestTimeout = 30 * time.Second
)

// IsCodingPlanBaseURL reports whether an openai-compatibility entry's base URL
// is the GLM Coding Plan endpoint, which marks the entry as a Z.ai account.
func IsCodingPlanBaseURL(base string) bool {
	return strings.TrimRight(strings.TrimSpace(base), "/") == CodingPlanBaseURL
}

// Service performs the sign-in exchanges. Endpoints are fields so tests can
// point them at a fake upstream.
type Service struct {
	client       *http.Client
	TokenURL     string
	BizBaseURL   string
	AuthorizeURL string
}

// NewService returns a Service using client (http.DefaultClient when nil).
// Every request is bounded by NeoPi's 30 s sign-in timeout.
func NewService(client *http.Client) *Service {
	if client == nil {
		client = http.DefaultClient
	}
	return &Service{client: client, TokenURL: TokenURL, BizBaseURL: BizBaseURL, AuthorizeURL: AuthorizeURL}
}

// AuthorizationURL is generateAuthUrl: the standard authorize parameters in
// NeoPi's order.
func (s *Service) AuthorizationURL(state string) string {
	query := "client_id=" + url.QueryEscape(ClientID) + "&response_type=code&redirect_uri=" + url.QueryEscape(RedirectURI)
	if state != "" {
		query += "&state=" + url.QueryEscape(state)
	}
	return s.AuthorizeURL + "?" + query
}

// Account identifies the signed-in Z.ai user.
type Account struct {
	Email string
	ID    string
}

// ExchangeCode is exchangeToken's code grant and credential projection. A
// pasted `code#state` carries the state Z.ai echoed, which wins.
func (s *Service) ExchangeCode(ctx context.Context, code, state string) (string, Account, error) {
	if index := strings.IndexByte(code, '#'); index >= 0 {
		if fragment := code[index+1:]; fragment != "" {
			state = fragment
		}
		code = code[:index]
	}
	body := jsjson.Object(
		jsjson.Field{Name: "provider", Value: jsjson.Marshal("zai")},
		jsjson.Field{Name: "code", Value: jsjson.Marshal(code)},
		jsjson.Field{Name: "redirect_uri", Value: jsjson.Marshal(RedirectURI)},
		jsjson.Field{Name: "state", Value: jsjson.Marshal(state)},
	)
	status, raw, err := s.send(ctx, http.MethodPost, s.TokenURL, "", body)
	if err != nil {
		return "", Account{}, fmt.Errorf("%s request to %s failed: %w", authProvider, s.TokenURL, err)
	}
	if status < 200 || status >= 300 {
		return "", Account{}, fmt.Errorf("%s token exchange failed: %d %s", authProvider, status, jsPrefix(string(raw), 500))
	}
	token := gjson.GetBytes(raw, "data.zai.access_token")
	if token.Type != gjson.String || token.String() == "" {
		return "", Account{}, fmt.Errorf("%s token response missing access token: %s", authProvider, jsPrefix(string(raw), 500))
	}
	user := gjson.GetBytes(raw, "data.user")
	account := Account{Email: user.Get("email").String(), ID: user.Get("id").String()}
	return token.String(), account, nil
}

// MintAPIKey is the zai-mint-key hook: business login, default organization
// and project, find or create the key named KeyName, then read its secret.
func (s *Service) MintAPIKey(ctx context.Context, oauthToken string) (string, error) {
	data, err := s.call(ctx, http.MethodPost, s.BizBaseURL+"/api/auth/z/login", "", jsjson.Object(jsjson.Field{Name: "token", Value: jsjson.Marshal(oauthToken)}), "business login")
	if err != nil {
		return "", err
	}
	biz := trimmed(data.Get("access_token"))
	if biz == "" {
		biz = trimmed(data.Get("accessToken"))
	}
	if biz == "" {
		return "", errors.New("Z.ai business login returned no access token")
	}
	customer, err := s.call(ctx, http.MethodGet, s.BizBaseURL+"/api/biz/customer/getCustomerInfo", biz, nil, "customer lookup")
	if err != nil {
		return "", err
	}
	org := pickDefault(customer.Get("organizations"))
	project := pickDefault(org.Get("projects"))
	organizationID, projectID := trimmed(org.Get("organizationId")), trimmed(project.Get("projectId"))
	if organizationID == "" || projectID == "" {
		return "", errors.New("Z.ai key provisioning failed: no organization/project on account")
	}
	keysURL := s.BizBaseURL + "/api/biz/v1/organization/" + organizationID + "/projects/" + projectID + "/api_keys"
	listed, err := s.call(ctx, http.MethodGet, keysURL, biz, nil, "api key list")
	if err != nil {
		return "", err
	}
	var record gjson.Result
	for _, key := range keyArray(listed) {
		if key.Get("name").String() == KeyName {
			record = key
			break
		}
	}
	if !record.Exists() {
		if record, err = s.call(ctx, http.MethodPost, keysURL, biz, jsjson.Object(jsjson.Field{Name: "name", Value: jsjson.Marshal(KeyName)}), "api key create"); err != nil {
			return "", err
		}
	}
	apiKey := trimmed(record.Get("apiKey"))
	if apiKey == "" {
		return "", errors.New("Z.ai key provisioning returned no apiKey")
	}
	// List entries mask the secret; the copy endpoint always returns it whole.
	copied, err := s.call(ctx, http.MethodGet, keysURL+"/copy/"+jsEncodeURIComponent(apiKey), biz, nil, "api key copy")
	if err != nil {
		return "", err
	}
	secret := trimmed(copied.Get("secretKey"))
	if secret == "" {
		return "", errors.New("Z.ai key provisioning returned no secretKey")
	}
	return apiKey + "." + secret, nil
}

// call sends one business-API request (getJson/postJson) and unwraps Z.ai's
// {code, msg, data, success} envelope.
func (s *Service) call(ctx context.Context, method, target, bearer string, body []byte, operation string) (gjson.Result, error) {
	status, raw, err := s.send(ctx, method, target, bearer, body)
	if err != nil {
		return gjson.Result{}, err
	}
	if status < 200 || status >= 300 {
		return gjson.Result{}, fmt.Errorf("HTTP request failed. status=%d; url=%s; body=%s", status, target, raw)
	}
	if len(raw) == 0 {
		return gjson.Result{}, nil
	}
	if !gjson.ValidBytes(raw) {
		return gjson.Result{}, fmt.Errorf("Z.ai %s returned invalid JSON", operation)
	}
	return unwrapEnvelope(gjson.ParseBytes(raw), operation)
}

func (s *Service) send(ctx context.Context, method, target, bearer string, body []byte) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", userAgent)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	// An explicit Accept-Encoding disables Go's transparent decompression.
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	defer func() { _ = decoded.Close() }()
	raw, err := io.ReadAll(io.LimitReader(decoded, 1<<20))
	return resp.StatusCode, raw, err
}

// unwrapEnvelope is unwrapEnvelope: bodies with a code or success field must
// report success (code 0 or 200, success not false) and yield their data.
func unwrapEnvelope(body gjson.Result, operation string) (gjson.Result, error) {
	if !body.IsObject() || !body.Get("code").Exists() && !body.Get("success").Exists() {
		return body, nil
	}
	code := body.Get("code")
	if body.Get("success").Type == gjson.False || !successCode(code) {
		message := body.Get("msg").String()
		if message == "" {
			message = "code " + jsString(code)
		}
		return gjson.Result{}, fmt.Errorf("Z.ai %s failed: %s", operation, message)
	}
	if data := body.Get("data"); data.Exists() {
		return data, nil
	}
	return body, nil
}

func successCode(code gjson.Result) bool {
	switch code.Type {
	case gjson.Null:
		return true
	case gjson.Number:
		return code.Num == 0 || code.Num == 200
	case gjson.String:
		return code.Str == "0" || code.Str == "200"
	default:
		return !code.Exists()
	}
}

// jsString is String(value) for an envelope code.
func jsString(value gjson.Result) string {
	switch value.Type {
	case gjson.String:
		return value.Str
	case gjson.Number:
		return strconv.FormatFloat(value.Num, 'f', -1, 64)
	case gjson.True:
		return "true"
	case gjson.False:
		return "false"
	case gjson.Null:
		return "null"
	default:
		return "undefined"
	}
}

// keyArray is asKeyArray: a bare array or a list/keys/apiKeys/records wrapper.
func keyArray(value gjson.Result) []gjson.Result {
	if value.IsArray() {
		return value.Array()
	}
	if value.IsObject() {
		for _, field := range []string{"list", "keys", "apiKeys", "records"} {
			if inner := value.Get(field); inner.IsArray() {
				return inner.Array()
			}
		}
	}
	return nil
}

// pickDefault is `find(x => x?.isDefault) ?? first`.
func pickDefault(list gjson.Result) gjson.Result {
	items := list.Array()
	for _, item := range items {
		if item.Get("isDefault").Bool() {
			return item
		}
	}
	if len(items) > 0 {
		return items[0]
	}
	return gjson.Result{}
}

func trimmed(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.Str)
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

// jsEncodeURIComponent escapes like encodeURIComponent.
func jsEncodeURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.IndexByte("-_.!~*'()", b) >= 0 {
			out.WriteByte(b)
		} else {
			fmt.Fprintf(&out, "%%%02X", b)
		}
	}
	return out.String()
}
