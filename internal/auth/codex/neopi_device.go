package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	codexDeviceUserCodeURL = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	codexDeviceTokenURL    = "https://auth.openai.com/api/accounts/deviceauth/token"
	codexDeviceRedirectURI = "https://auth.openai.com/deviceauth/callback"
)

// DeviceAuthorization carries one pending NeoPi-style headless login.
type DeviceAuthorization struct {
	URL          string
	UserCode     string
	deviceAuthID string
	pollInterval time.Duration
}

// StartDevice obtains the browser code without opening a local callback listener.
func (o *CodexAuth) StartDevice(ctx context.Context) (*DeviceAuthorization, error) {
	status, data, err := o.requestNeoPiAuth(ctx, codexDeviceUserCodeURL, "application/json", `{"client_id":"`+ClientID+`"}`)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Device authorization initiation failed: %d", status)
	}
	var response struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		Interval     any    `json:"interval"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Codex device authorization: %w", err)
	}
	if response.DeviceAuthID == "" || response.UserCode == "" {
		return nil, fmt.Errorf("Device authorization response missing required fields")
	}
	seconds := 5.0
	switch interval := response.Interval.(type) {
	case float64:
		seconds = interval
	case string:
		// NeoPi uses parseInt(value, 10) || 5, retaining a numeric prefix.
		text := strings.TrimSpace(interval)
		end := 0
		if len(text) > 0 && (text[0] == '+' || text[0] == '-') {
			end++
		}
		for end < len(text) && text[end] >= '0' && text[end] <= '9' {
			end++
		}
		if parsed, errParse := strconv.ParseFloat(text[:end], 64); errParse == nil && parsed != 0 {
			seconds = parsed
		}
	}
	return &DeviceAuthorization{URL: "https://auth.openai.com/codex/device", UserCode: response.UserCode, deviceAuthID: response.DeviceAuthID, pollInterval: time.Duration((seconds + 3) * float64(time.Second))}, nil
}

// WaitForDevice follows NeoPi's initial wait, safety margin and 120-poll limit.
func (o *CodexAuth) WaitForDevice(ctx context.Context, session *DeviceAuthorization) (*CodexAuthBundle, error) {
	return o.waitForDevice(ctx, session, func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	})
}

func (o *CodexAuth) waitForDevice(ctx context.Context, session *DeviceAuthorization, pause func(context.Context, time.Duration) error) (*CodexAuthBundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
	}{session.deviceAuthID, session.UserCode})
	if err != nil {
		return nil, fmt.Errorf("encode Codex device poll: %w", err)
	}
	body := string(payload)
	for poll := 0; poll < 120; poll++ {
		delay := session.pollInterval
		if poll == 0 {
			delay = min(delay, 5*time.Second)
		}
		if err = pause(ctx, delay); err != nil || ctx.Err() != nil {
			return nil, fmt.Errorf("Device authorization cancelled")
		}
		status, data, errRequest := o.requestNeoPiAuth(ctx, codexDeviceTokenURL, "application/json", body)
		if errRequest != nil {
			return nil, errRequest
		}
		if status == http.StatusForbidden || status == http.StatusNotFound {
			continue
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("Device token polling failed: %d", status)
		}
		var response struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if err = json.Unmarshal(data, &response); err != nil {
			return nil, fmt.Errorf("decode Codex device poll: %w", err)
		}
		if response.AuthorizationCode == "" || response.CodeVerifier == "" {
			return nil, fmt.Errorf("Device token response missing authorization_code or code_verifier")
		}
		return o.exchangeDeviceCode(ctx, response.AuthorizationCode, response.CodeVerifier)
	}
	return nil, fmt.Errorf("Device authorization timed out — user did not complete login in time")
}

func (o *CodexAuth) exchangeDeviceCode(ctx context.Context, code, verifier string) (*CodexAuthBundle, error) {
	form := codexForm([2]string{"grant_type", "authorization_code"}, [2]string{"client_id", ClientID}, [2]string{"code", code}, [2]string{"code_verifier", verifier}, [2]string{"redirect_uri", codexDeviceRedirectURI})
	status, data, err := o.requestNeoPiAuth(ctx, TokenURL, "application/x-www-form-urlencoded", form)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Token exchange failed: %s", formatCodexDeviceTokenError(status, data))
	}
	var token struct {
		Access    string   `json:"access_token"`
		Refresh   string   `json:"refresh_token"`
		ID        string   `json:"id_token"`
		ExpiresIn *float64 `json:"expires_in"`
	}
	if err = json.Unmarshal(data, &token); err != nil {
		return nil, fmt.Errorf("decode Codex device token: %w", err)
	}
	if token.Access == "" || token.Refresh == "" || token.ExpiresIn == nil {
		return nil, fmt.Errorf("Token response missing required fields")
	}
	account, email, plan := codexTokenProfile(token.Access, token.ID)
	if account == "" && email == "" {
		return nil, fmt.Errorf("Failed to extract account identity from token")
	}
	now := time.Now()
	return &CodexAuthBundle{TokenData: CodexTokenData{AccessToken: token.Access, RefreshToken: token.Refresh, IDToken: token.ID, AccountID: account, Email: email, PlanType: plan, Expire: now.Add(time.Duration(*token.ExpiresIn * float64(time.Second))).Format(time.RFC3339Nano)}, LastRefresh: now.Format(time.RFC3339Nano)}, nil
}

func formatCodexDeviceTokenError(status int, data []byte) string {
	text := strings.TrimSpace(codexTokenErrorExcerpt(data))
	if text == "" {
		return strconv.Itoa(status)
	}
	var body map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &body) == nil && body != nil {
		detail, description := describeCodexTokenEndpointValue(body["error"]), describeCodexTokenEndpointValue(body["error_description"])
		if detail != "" && description != "" && detail != description {
			text = detail + ": " + description
		} else if detail != "" {
			text = detail
		} else if description != "" {
			text = description
		} else if message := describeCodexTokenEndpointValue(body["message"]); message != "" {
			text = message
		}
	}
	return fmt.Sprintf("%d %s", status, text)
}

func describeCodexTokenEndpointValue(raw json.RawMessage) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	case map[string]any:
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		first := func(keys ...string) json.RawMessage {
			for _, key := range keys {
				if field := fields[key]; len(field) > 0 && string(field) != "null" {
					return field
				}
			}
			return nil
		}
		code := describeCodexTokenEndpointValue(first("code", "error"))
		message := describeCodexTokenEndpointValue(first("message", "error_description", "description"))
		if code != "" && message != "" && code != message {
			return code + ": " + message
		}
		if code != "" {
			return code
		}
		if message != "" {
			return message
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) == nil {
			return compact.String()
		}
	}
	return ""
}
