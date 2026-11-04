package devin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// CreateAuthRecord stores a completed login. The token is kept as NeoPi keeps
// it (the wire prefix is added per request), with its projected expiry. The
// account identity comes from the GetUserStatus report NeoPi's usage reads;
// without one the file is named after a digest of the token.
func (s *DevinAuthService) CreateAuthRecord(ctx context.Context, credential Credential) (*coreauth.Auth, error) {
	token := strings.TrimSpace(credential.Access)
	if token == "" {
		return nil, fmt.Errorf("devin session token is required")
	}
	usage, errUsage := FetchUsage(ctx, s.client, s.serverBaseURL, token)
	if errUsage != nil {
		log.Warn("failed to fetch devin user status and quota")
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return nil, errCtx
	}

	identifier := ""
	if usage != nil {
		identifier = usage.Metadata.Email
		if identifier == "" {
			identifier = usage.Metadata.AccountID
		}
	}
	if identifier == "" {
		// Avoid overwriting another account when the identity is unavailable.
		digest := sha256.Sum256([]byte(token))
		identifier = fmt.Sprintf("user-%x", digest[:8])
	}
	// Upstream identity values must never introduce path components into filenames.
	fileIdentifier := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '@' {
			return r
		}
		return '_'
	}, identifier)
	if fileIdentifier != identifier || len(fileIdentifier) > 160 {
		digest := sha256.Sum256([]byte(identifier))
		fileIdentifier = fmt.Sprintf("user-%x", digest[:8])
	}
	fileName := fmt.Sprintf("devin-%s.json", fileIdentifier)

	metadata := map[string]any{"type": "devin", "api_key": token, "auth_kind": "oauth"}
	if !credential.Expires.IsZero() {
		metadata["expired"] = credential.Expires.UTC().Format(time.RFC3339)
	}
	now := s.now()
	record := &coreauth.Auth{
		ID:         fileName,
		Provider:   "devin",
		FileName:   fileName,
		Label:      fmt.Sprintf("Devin (%s)", identifier),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"api_key": token, "auth_kind": "oauth"},
		Metadata:   metadata,
		Quota:      coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{}},
	}
	ApplyUsage(record, usage, now)
	return record, nil
}
