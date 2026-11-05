// Package cloudflare ports NeoPi's Cloudflare AI Gateway credential and
// endpoint handling (pi-catalog wire/cloudflare.ts and pi-ai
// registry/cloudflare-ai-gateway.ts prepareRequest).
package cloudflare

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Provider is NeoPi's provider id.
const Provider = "cloudflare-ai-gateway"

// GatewayBase is CLOUDFLARE_AI_GATEWAY_BASE_URL: the provider-native prefix
// whose account and gateway placeholders a request fills.
const GatewayBase = "https://gateway.ai.cloudflare.com/v1/<account>/<gateway>"

// Credential is a gateway token and the routing ids collected with it.
type Credential struct {
	Token     string `json:"token"`
	AccountID string `json:"accountId,omitempty"`
	GatewayID string `json:"gatewayId,omitempty"`
}

// ParseCredential is parseCloudflareCredential: a structured login
// credential or a legacy plain token; ok is false when empty or malformed.
func ParseCredential(value string) (Credential, bool) {
	trimmed := jsTrim(value)
	if trimmed == "" {
		return Credential{}, false
	}
	if !strings.HasPrefix(trimmed, "{") {
		return Credential{Token: trimmed}, true
	}
	var fields map[string]any
	if json.Unmarshal([]byte(trimmed), &fields) != nil || fields == nil {
		return Credential{}, false
	}
	token, isString := fields["token"].(string)
	if !isString || jsTrim(token) == "" {
		return Credential{}, false
	}
	credential := Credential{Token: jsTrim(token)}
	for key, target := range map[string]*string{"accountId": &credential.AccountID, "gatewayId": &credential.GatewayID} {
		value, present := fields[key]
		if !present {
			continue
		}
		text, isString := value.(string)
		if !isString {
			return Credential{}, false
		}
		*target = jsTrim(text)
	}
	return credential, true
}

// Serialize is serializeCloudflareCredential: trimmed fields, empty ids
// omitted, in JSON.stringify's field order and escaping.
func Serialize(credential Credential) string {
	trimmed := Credential{Token: jsTrim(credential.Token), AccountID: jsTrim(credential.AccountID), GatewayID: jsTrim(credential.GatewayID)}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(trimmed)
	return strings.TrimSuffix(encoded.String(), "\n")
}

// BaseURL is prepareRequest's endpoint rewrite: a gateway endpoint gets the
// credential's account and gateway ids (else the environment's
// CLOUDFLARE_ACCOUNT_ID / CLOUDFLARE_GATEWAY_ID); any other endpoint is kept.
func BaseURL(base string, credential Credential, envAccount, envGateway string) (string, error) {
	if !strings.HasPrefix(base, GatewayBase) {
		return base, nil
	}
	account := credential.AccountID
	if account == "" {
		account = envAccount
	}
	gateway := credential.GatewayID
	if gateway == "" {
		gateway = envGateway
	}
	if account == "" {
		return "", errors.New("Cloudflare account ID is required")
	}
	if gateway == "" {
		return "", errors.New("Cloudflare AI Gateway ID is required")
	}
	// String.replace replaces the first occurrence of each placeholder.
	return strings.Replace(strings.Replace(base, "<account>", account, 1), "<gateway>", gateway, 1), nil
}

// jsTrim is String.prototype.trim (WhiteSpace plus LineTerminator; unlike
// strings.TrimSpace it trims U+FEFF and keeps U+0085).
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}
