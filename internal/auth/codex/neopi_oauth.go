package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// codexForm matches URLSearchParams ordering and its form percent-encode set.
func codexForm(fields ...[2]string) string {
	var out strings.Builder
	for i, field := range fields {
		if i > 0 {
			out.WriteByte('&')
		}
		for j, value := range field {
			if j > 0 {
				out.WriteByte('=')
			}
			value = strings.ReplaceAll(url.QueryEscape(value), "~", "%7E")
			out.WriteString(strings.ReplaceAll(value, "%2A", "*"))
		}
	}
	return out.String()
}

func codexFetchHeaders(request *http.Request) {
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	request.Header.Set("Connection", "keep-alive")
	request.Header.Set("User-Agent", "Bun/1.3.14")
}

// requestNeoPiAuth applies the source fetch identity and credential-acquisition deadline.
func (o *CodexAuth) requestNeoPiAuth(ctx context.Context, endpoint, contentType, payload string) (int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, codexRefreshTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("create Codex auth request: %w", err)
	}
	codexFetchHeaders(request)
	request.Header.Set("Content-Type", contentType)
	response, err := o.httpClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("Codex auth request failed: %w", err)
	}
	body, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return 0, nil, fmt.Errorf("decode Codex auth response: %w", err)
	}
	defer func() {
		if errClose := body.Close(); errClose != nil {
			log.WithError(errClose).Warn("closing Codex auth response")
		}
	}()
	data, err := io.ReadAll(body)
	if err != nil {
		return 0, nil, fmt.Errorf("read Codex auth response: %w", err)
	}
	return response.StatusCode, data, nil
}

func (o *CodexAuth) requestNeoPiToken(ctx context.Context, form, previousRefresh string, refreshing bool) (*CodexTokenData, error) {
	status, data, err := o.requestNeoPiAuth(ctx, TokenURL, "application/x-www-form-urlencoded", form)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		phase := "exchange"
		if refreshing {
			phase = "refresh"
		}
		return nil, fmt.Errorf("openai-codex token %s failed: %d %s", phase, status, codexTokenErrorExcerpt(data))
	}
	var token map[string]any
	if err = json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("decode Codex token response: %w", err)
	}
	access := codexCredentialString(token["access_token"])
	if access == "" {
		return nil, fmt.Errorf("openai-codex token response missing access token")
	}
	seconds, ok := token["expires_in"].(float64)
	if !ok {
		return nil, fmt.Errorf("openai-codex token response missing expires_in")
	}
	refresh := codexCredentialString(token["refresh_token"])
	if refresh == "" {
		refresh = previousRefresh
	}
	idToken, _ := token["id_token"].(string)
	account, email, plan := codexTokenProfile(access, idToken)
	if !refreshing && account == "" && email == "" {
		return nil, fmt.Errorf("Failed to extract account identity from token")
	}
	return &CodexTokenData{IDToken: idToken, AccessToken: access, RefreshToken: refresh, AccountID: account, Email: email, PlanType: plan, Expire: time.Now().Add(time.Duration(seconds * float64(time.Second))).Format(time.RFC3339Nano)}, nil
}

func codexCredentialString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return ""
	}
}

// Profile fields use nullish access-token precedence, not truthy fallback.
func codexTokenProfile(accessToken, idToken string) (accountID, email, plan string) {
	decode := func(token string) map[string]any {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			return nil
		}
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		if err != nil {
			return nil
		}
		var result map[string]any
		if json.Unmarshal(data, &result) != nil {
			return nil
		}
		return result
	}
	access, id := decode(accessToken), decode(idToken)
	field := func(namespace, key string) string {
		for _, claims := range []map[string]any{access, id} {
			nested, _ := claims[namespace].(map[string]any)
			if value := nested[key]; value != nil {
				text, _ := value.(string)
				return text
			}
		}
		return ""
	}
	return field("https://api.openai.com/auth", "chatgpt_account_id"), strings.ToLower(strings.TrimSpace(field("https://api.openai.com/profile", "email"))), strings.ToLower(strings.TrimSpace(field("https://api.openai.com/auth", "chatgpt_plan_type")))
}

func codexTokenErrorExcerpt(data []byte) string {
	// Error envelopes must not disclose credentials through callers' error logs.
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) == nil {
		redacted := false
		for _, name := range []string{"access_token", "refresh_token", "id_token"} {
			if _, exists := object[name]; exists {
				object[name] = json.RawMessage(`"[redacted]"`)
				redacted = true
			}
		}
		if redacted {
			if encoded, err := json.Marshal(object); err == nil {
				data = encoded
			}
		}
	}
	if len(data) > 500 {
		data = data[:500]
	}
	return string(data)
}
