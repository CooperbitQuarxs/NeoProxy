package util

import "regexp"

var (
	oauthDefinitiveFailure  = regexp.MustCompile(`(?i)invalid_grant|invalid_token|unauthorized_client|\brevoked\b|refresh[\s\v\p{Zs}\x{2028}\x{2029}\x{feff}_]?token[^\r\n\x{2028}\x{2029}]*expired`)
	oauthTransientFailure   = regexp.MustCompile(`(?i)timeout|network|fetch failed|ECONN(?:REFUSED|RESET)|ETIMEDOUT|EAI_AGAIN|socket hang up|\b(?:408|425|429|5\d{2})\b|rate[^\r\n\x{2028}\x{2029}]?limit|too many requests|temporar|unavailable|forbidden|permission_denied|cloudflare|captcha`)
	oauthUnauthorizedStatus = regexp.MustCompile(`\b401\b`)
)

// IsDefinitiveOAuthRefreshFailure mirrors NeoPi's grant-lifetime classifier.
// Explicit revoked/invalid grants win; a bare 401 is definitive only without
// evidence of a transient transport, rate-limit or policy failure.
func IsDefinitiveOAuthRefreshFailure(message string) bool {
	return oauthDefinitiveFailure.MatchString(message) || oauthUnauthorizedStatus.MatchString(message) && !oauthTransientFailure.MatchString(message)
}
