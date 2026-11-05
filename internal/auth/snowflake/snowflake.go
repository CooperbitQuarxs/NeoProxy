// Package snowflake ports NeoPi's Snowflake Cortex account handling
// (pi-ai registry/snowflake.ts): account identifier normalization, the
// structured OAuth credential, and the request endpoint rewrite that swaps
// the catalog's placeholder origin for the account.
package snowflake

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Provider is NeoPi's provider id.
const Provider = "snowflake"

// Placeholder is the origin catalog rows carry until a request swaps in the
// account URL. `.invalid` (RFC 6761) never resolves, so an unrewritten request
// fails before reaching any host.
const Placeholder = "https://snowflake-account.invalid"

const invalidAccountMessage = "Paste your Snowflake account identifier (orgname-accountname) or account URL"

// ErrInvalidCredential is a structured key that is not a usable OAuth credential.
var ErrInvalidCredential = errors.New("Invalid Snowflake credential; run /login snowflake again")

var (
	dnsLabel     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	legacyRegion = regexp.MustCompile(`^[a-z]{2}(?:-gov)?-[a-z]+-\d+$`)
	schemePrefix = regexp.MustCompile(`(?i)^([a-z][a-z0-9+.-]*)://`)
	httpsURL     = regexp.MustCompile(`(?i)^https://([^/?#]+)(.*)$`)
)

func isDNSLabel(label string) bool {
	return len(label) <= 63 && dnsLabel.MatchString(label)
}

// hostLabel is NeoPi's host spelling: lowercased, underscores as hyphens.
func hostLabel(value string) string {
	return strings.ReplaceAll(strings.ToLower(value), "_", "-")
}

// NormalizeAccountURL is normalizeSnowflakeAccountUrl: an account identifier,
// account URL or Snowsight link as the account's https origin, refusing hosts
// outside snowflakecomputing.com.
func NormalizeAccountURL(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", errors.New("Snowflake account is required: run /login snowflake or set SNOWFLAKE_ACCOUNT")
	}
	// Only `<scheme>://` is a scheme; `host:443` is a schemeless authority.
	scheme := schemePrefix.FindStringSubmatch(value)
	if scheme != nil && !strings.EqualFold(scheme[1], "https") {
		return "", errors.New("Snowflake account URL must use https")
	}
	if strings.Contains(value, `\`) {
		return "", errors.New(invalidAccountMessage)
	}
	var host string
	if scheme != nil {
		match := httpsURL.FindStringSubmatch(value)
		if match == nil {
			return "", errors.New(invalidAccountMessage)
		}
		// The raw authority is validated: URL parsing would repair some malformed inputs.
		host = strings.TrimSuffix(hostLabel(match[1]), ":443")
		if host == "app.snowflake.com" {
			path := match[2]
			if cut := strings.IndexAny(path, "?#"); cut >= 0 {
				path = path[:cut]
			}
			segments := strings.Split(path, "/")
			org, account := "", ""
			if len(segments) > 1 {
				org = hostLabel(segments[1])
			}
			if len(segments) > 2 {
				account = hostLabel(segments[2])
			}
			if !isDNSLabel(org) || !isDNSLabel(account) {
				return "", errors.New(invalidAccountMessage)
			}
			// Legacy links are /<cloud-region>/<locator>; their host mapping is region-specific.
			if legacyRegion.MatchString(org) {
				return "", errors.New("Legacy Snowsight links don't identify the account host; paste your account identifier (orgname-accountname) or account URL")
			}
			host = org + "-" + account + ".snowflakecomputing.com"
		}
	} else {
		if strings.ContainsAny(value, "/?#") {
			return "", errors.New(invalidAccountMessage)
		}
		host = strings.TrimSuffix(hostLabel(value), ":443")
		if !strings.HasSuffix(host, ".snowflakecomputing.com") && !strings.HasSuffix(host, ".snowflakecomputing.cn") {
			host += ".snowflakecomputing.com"
		}
	}
	if strings.HasSuffix(host, ".snowflakecomputing.cn") {
		return "", errors.New("Snowflake Cortex REST API is not available in China-region accounts (.snowflakecomputing.cn)")
	}
	const suffix = ".snowflakecomputing.com"
	if !strings.HasSuffix(host, suffix) || len(host) == len(suffix) {
		return "", errors.New(invalidAccountMessage)
	}
	for _, label := range strings.Split(host, ".") {
		if !isDNSLabel(label) {
			return "", errors.New(invalidAccountMessage)
		}
	}
	return "https://" + host, nil
}

// Credential is a Snowflake key: a bearer token and, for a structured OAuth
// credential, the account it was issued for.
type Credential struct {
	Token      string
	AccountURL string
}

// ParseCredential is parseSnowflakeCredential: a plain value is a PAT; a JSON
// object is the registry's structured OAuth key, which must name its account
// (enterpriseUrl). ok is false for an empty value.
func ParseCredential(value string) (credential Credential, ok bool, err error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return Credential{}, false, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return Credential{Token: trimmed}, true, nil
	}
	var fields map[string]any
	if json.Unmarshal([]byte(trimmed), &fields) != nil || fields == nil {
		return Credential{}, false, ErrInvalidCredential
	}
	token, _ := fields["token"].(string)
	// Structured keys are OAuth bearers; without their account they must not get PAT routing.
	account, _ := fields["enterpriseUrl"].(string)
	if strings.TrimSpace(token) == "" || strings.TrimSpace(account) == "" {
		return Credential{}, false, ErrInvalidCredential
	}
	accountURL, err := NormalizeAccountURL(account)
	if err != nil {
		return Credential{}, false, fmt.Errorf("Invalid Snowflake credential account: %s; run /login snowflake again", err.Error())
	}
	return Credential{Token: strings.TrimSpace(token), AccountURL: accountURL}, true, nil
}

// BaseURL is snowflakeTransport.prepareRequest's endpoint rewrite: a row on
// the placeholder origin moves to the credential's account (else the
// environment's SNOWFLAKE_ACCOUNT); any other endpoint must belong to an
// OAuth credential's account.
func BaseURL(base string, credential Credential, envAccount string) (string, error) {
	if base == Placeholder || strings.HasPrefix(base, Placeholder+"/") {
		account := credential.AccountURL
		if account == "" {
			normalized, err := NormalizeAccountURL(envAccount)
			if err != nil {
				return "", err
			}
			account = normalized
		}
		return account + base[len(Placeholder):], nil
	}
	if credential.AccountURL != "" && origin(base) != credential.AccountURL {
		return "", errors.New("Snowflake OAuth account does not match the model endpoint")
	}
	return base, nil
}

// origin is `new URL(base).origin` for an https account URL ("" when base does
// not parse): the lowercased scheme and host, without the default port.
func origin(base string) string {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	scheme, host := strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host)
	if scheme == "https" {
		host = strings.TrimSuffix(host, ":443")
	}
	return scheme + "://" + host
}
