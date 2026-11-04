package neopiimport

import (
	"encoding/json"
	"regexp"
	"strings"
)

// tokenPlanKey is a QwenCloud Token Plan credential as NeoPi stores it: a bare
// sk-... key for the default International endpoint, or a JSON bundle
// {"token","cookie","baseUrl"} when the person also pasted a console cookie
// (quota reporting) or chose another region. NeoProxy uses the token and region;
// the console cookie is a short-lived browser session and is not imported.
type tokenPlanKey struct {
	token   string
	baseURL string
}

var tokenPlanTokenPattern = regexp.MustCompile(`^sk-[A-Za-z0-9._~+/-]+={0,2}$`)

// parseTokenPlanKey mirrors NeoPi's parseAlibabaTokenPlanCredential.
func parseTokenPlanKey(value string) (tokenPlanKey, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "{") {
		return tokenPlanKey{token: value}, tokenPlanTokenPattern.MatchString(value)
	}
	var bundle struct {
		Token   *string `json:"token"`
		Cookie  any     `json:"cookie"`
		BaseURL any     `json:"baseUrl"`
	}
	if json.Unmarshal([]byte(value), &bundle) != nil || bundle.Token == nil {
		return tokenPlanKey{}, false
	}
	if _, ok := bundle.Cookie.(string); bundle.Cookie != nil && !ok {
		return tokenPlanKey{}, false
	}
	baseURL, ok := bundle.BaseURL.(string)
	if bundle.BaseURL != nil && !ok {
		return tokenPlanKey{}, false
	}
	token := strings.TrimSpace(*bundle.Token)
	if !tokenPlanTokenPattern.MatchString(token) {
		return tokenPlanKey{}, false
	}
	return tokenPlanKey{token: token, baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/")}, true
}
