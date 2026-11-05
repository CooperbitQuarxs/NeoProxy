package snowflake

import (
	"strings"
	"time"
)

// RefreshLead is DEFAULT_REFRESH_SKEW_MS: how long before expiry NeoPi's auth
// broker refreshes a credential.
const RefreshLead = 5 * time.Minute

// Metadata is the stored record of a signed-in account (NeoPi's
// OAuthCredentials {access, refresh, expires, enterpriseUrl}).
func Metadata(tokens Tokens, accountURL string) map[string]any {
	return map[string]any{
		"type":           Provider,
		"access_token":   tokens.Access,
		"refresh_token":  tokens.Refresh,
		"expired":        tokens.Expires.UTC().Format(time.RFC3339Nano),
		"enterprise_url": accountURL,
	}
}

// RecordTokens reads a signed-in account's tokens and account from its
// record; ok is false for a record without an OAuth access token.
func RecordTokens(metadata map[string]any) (tokens Tokens, account string, ok bool) {
	access, _ := metadata["access_token"].(string)
	if strings.TrimSpace(access) == "" {
		return Tokens{}, "", false
	}
	refresh, _ := metadata["refresh_token"].(string)
	account, _ = metadata["enterprise_url"].(string)
	tokens = Tokens{Access: access, Refresh: refresh}
	if expired, _ := metadata["expired"].(string); expired != "" {
		if at, err := time.Parse(time.RFC3339Nano, expired); err == nil {
			tokens.Expires = at
		}
	}
	return tokens, account, true
}

// RecordCredential is a signed-in account's request credential: its access
// token, bound to its account as NeoPi's structured key binds it.
func RecordCredential(metadata map[string]any) (Credential, bool, error) {
	tokens, account, ok := RecordTokens(metadata)
	if !ok {
		return Credential{}, false, nil
	}
	accountURL, err := NormalizeAccountURL(account)
	if err != nil {
		return Credential{}, false, ErrInvalidCredential
	}
	return Credential{Token: strings.TrimSpace(tokens.Access), AccountURL: accountURL}, true, nil
}
