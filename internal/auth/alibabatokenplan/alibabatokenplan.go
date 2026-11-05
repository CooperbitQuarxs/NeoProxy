// Package alibabatokenplan is NeoPi's QwenCloud Token Plan: the credential
// bundle (catalog/src/wire/alibaba-token-plan.ts), the regional key entry
// (registry/oauth/alibaba-token-plan.ts) and the console usage report
// (usage/alibaba-token-plan.ts).
//
// The Token Plan ships as two regionally separate products whose keys are not
// interchangeable: International (Singapore) and China (Beijing).
package alibabatokenplan

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/neopikeys"
)

const (
	// InternationalBaseURL is the default (Singapore) endpoint.
	InternationalBaseURL = "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1"
	// ChinaBaseURL is the Beijing endpoint (百炼 Token Plan).
	ChinaBaseURL = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
	// Provider is the catalogue and NeoPi provider id.
	Provider = "alibaba-token-plan"
	// Label names the provider in key-validation failures.
	Label = "QwenCloud Token Plan"
)

// Credential is NeoPi's stored Token Plan key: a bare sk-... token, or a JSON
// bundle {"token","cookie","baseUrl"}.
type Credential struct {
	Token string
	// Cookie is the console Cookie header that enables quota reporting.
	Cookie string
	// BaseURL is the region the key authenticates against; empty means the
	// International endpoint.
	BaseURL string
}

var tokenPattern = regexp.MustCompile(`^sk-[A-Za-z0-9._~+/-]+={0,2}$`)

// Parse is parseAlibabaTokenPlanCredential.
func Parse(value string) (Credential, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Credential{}, false
	}
	if !strings.HasPrefix(value, "{") {
		return Credential{Token: value}, tokenPattern.MatchString(value)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(value), &fields) != nil {
		return Credential{}, false
	}
	token, ok := bundleString(fields, "token")
	if !ok || !tokenPattern.MatchString(strings.TrimSpace(token)) {
		return Credential{}, false
	}
	cookie, ok := bundleString(fields, "cookie")
	if !ok {
		return Credential{}, false
	}
	baseURL, ok := bundleString(fields, "baseUrl")
	if !ok {
		return Credential{}, false
	}
	return Credential{Token: strings.TrimSpace(token), Cookie: strings.TrimSpace(cookie), BaseURL: strings.TrimSpace(baseURL)}, true
}

// bundleString reads a bundle field that must be a string when present; an
// absent field is empty and a null or non-string one is invalid, as in NeoPi.
func bundleString(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, present := fields[name]
	if !present {
		return "", true
	}
	var text string
	if string(raw) == "null" || json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

// NormalizeBaseURL is a region's address as NeoPi compares it: trimmed and
// without trailing slashes.
func NormalizeBaseURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

// IsChina reports whether baseURL is the China (Beijing) endpoint.
func IsChina(baseURL string) bool {
	return NormalizeBaseURL(baseURL) == ChinaBaseURL
}

// ValidateKey is the sign-in's key check: the key must list models at its
// region's /models endpoint.
func ValidateKey(ctx context.Context, client *http.Client, baseURL, key string) error {
	return neopikeys.ValidateModelsEndpoint(ctx, client, Label, NormalizeBaseURL(baseURL)+"/models", key)
}

var cookiePrefix = regexp.MustCompile(`(?i)^Cookie:\s*`)

// CookieError is the sign-in's ConfigurationError for a malformed console cookie.
type CookieError struct{ Host string }

func (e *CookieError) Error() string {
	return fmt.Sprintf("Invalid QwenCloud Cookie header. Copy the complete Cookie request header from the %s usage request, not a single cookie value.", e.Host)
}

// NormalizeCookie is the sign-in's optional cookie prompt: surrounding space and
// a leading "Cookie:" are dropped, and a non-empty value must hold at least one
// complete name=value pair. An empty cookie skips quota reporting.
func NormalizeCookie(raw, baseURL string) (string, error) {
	cookie := strings.TrimSpace(cookiePrefix.ReplaceAllString(strings.TrimSpace(raw), ""))
	if cookie == "" {
		return "", nil
	}
	for _, segment := range strings.Split(cookie, ";") {
		if at := strings.Index(segment, "="); at > 0 && strings.TrimSpace(segment[:at]) != "" && strings.TrimSpace(segment[at+1:]) != "" {
			return cookie, nil
		}
	}
	host := "cs-data.qwencloud.com"
	if IsChina(baseURL) {
		host = "bailian-cs.console.aliyun.com"
	}
	return "", &CookieError{Host: host}
}
