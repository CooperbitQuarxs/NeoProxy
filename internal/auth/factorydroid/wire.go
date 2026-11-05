// Package factorydroid ports NeoPi's Factory Droid provider: the WorkOS
// device-code sign-in and refresh, the account scope a credential resolves
// to, and the wire constants shared by discovery, inference and usage
// (pi-catalog wire/factory-droid.ts, pi-ai registry/oauth/factory-droid.ts).
package factorydroid

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// Provider is NeoPi's provider id for Factory Droid.
const Provider = "factory-droid"

// ClientVersion is FACTORY_DROID_CLIENT_VERSION, the CLI release the proxy
// sees.
const ClientVersion = "0.237.0"

// Wire is a protocol the Factory proxy multiplexes (FACTORY_DROID_WIRES).
type Wire string

// Factory proxy wires.
const (
	WireCompletions Wire = "openai-completions"
	WireResponses   Wire = "openai-responses"
	WireMessages    Wire = "anthropic-messages"
	WireGoogle      Wire = "google-generate"
)

// ClientHeaders is factoryDroidClientHeaders: the identity discovery, usage
// and inference requests share.
func ClientHeaders(orgID string) http.Header {
	headers := http.Header{}
	headers.Set("X-Client-Version", ClientVersion)
	headers.Set("X-Factory-Client", "cli")
	if orgID != "" {
		headers.Set("X-Factory-Org-Id", orgID)
	}
	return headers
}

// ModelCacheScope is factoryDroidModelCacheProviderId's namespace: the
// account a credential signs in as (the JWT's external organization and
// user, else the opaque bearer itself), its organization, residency and
// inference region. WorkOS tokens rotate, but the roster belongs to the
// account; it is hashed so no bearer is kept.
func ModelCacheScope(credential *Credential) string {
	if credential == nil {
		return ""
	}
	token := credential.AccessToken
	scope := "bearer\x00" + token
	if parts := strings.Split(token, "."); len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "" {
		claims := jwtClaims(token)
		org, _ := claims["external_org_id"].(string)
		sub, _ := claims["sub"].(string)
		if strings.TrimSpace(org) != "" && strings.TrimSpace(sub) != "" {
			scope = "account\x00" + org + "\x00" + sub
		}
	}
	region := "global"
	if credential.Region == "eu" {
		region = "eu"
	}
	inference := credential.InferenceRegion
	if inference == "" {
		inference = region
	}
	sum := sha256.Sum256([]byte(scope + "\x00" + credential.OrgID + "\x00" + region + "\x00" + inference))
	return hex.EncodeToString(sum[:])
}
