package neopiimport

import "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/alibabatokenplan"

// tokenPlanKey is a QwenCloud Token Plan credential as NeoPi stores it: a bare
// sk-... key for the default International endpoint, or a JSON bundle
// {"token","cookie","baseUrl"} when the person also pasted a console cookie
// (quota reporting) or chose another region. NeoProxy keeps the token, the
// region and the cookie, which only the Token Plan usage report reads.
type tokenPlanKey struct {
	token   string
	cookie  string
	baseURL string
}

// parseTokenPlanKey mirrors NeoPi's parseAlibabaTokenPlanCredential.
func parseTokenPlanKey(value string) (tokenPlanKey, bool) {
	credential, ok := alibabatokenplan.Parse(value)
	return tokenPlanKey{token: credential.Token, cookie: credential.Cookie, baseURL: alibabatokenplan.NormalizeBaseURL(credential.BaseURL)}, ok
}
