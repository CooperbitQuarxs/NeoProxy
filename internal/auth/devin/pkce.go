package devin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCECodes contains the PKCE code verifier and code challenge pair.
type PKCECodes struct {
	CodeVerifier  string
	CodeChallenge string
}

// GeneratePKCECodes generates NeoPi's PKCE pair: a 96-byte random verifier
// and its S256 challenge, both base64url.
func GeneratePKCECodes() (*PKCECodes, error) {
	raw := make([]byte, 96)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return pkceFromBytes(raw), nil
}

func pkceFromBytes(raw []byte) *PKCECodes {
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(verifier))
	return &PKCECodes{CodeVerifier: verifier, CodeChallenge: base64.RawURLEncoding.EncodeToString(hash[:])}
}
