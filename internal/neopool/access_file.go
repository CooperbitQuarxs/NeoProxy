package neopool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

const acceptedCredentialFiles = "Upload one of: the Codex CLI file ~/.codex/auth.json, the Claude Code file ~/.claude/.credentials.json, or an auth file exported from CLIProxyAPI or another NeoProxy (a JSON object with a \"type\" field such as \"codex\" or \"claude\")."

// credentialFile is an uploaded credential converted to the engine's native
// auth-file format. APIKey is set instead when the file only holds an API key.
type credentialFile struct {
	Format   string // codex-cli, claude-code or native
	Provider string // catalogue provider ID
	Name     string // auth file name
	Data     []byte // native engine JSON
	APIKey   string
}

var safeFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@+-]{0,180}\.json$`)

// detectCredentialFile recognises the accepted formats by their contents; the
// uploaded file name is only kept for native files, and only when safe.
func detectCredentialFile(uploadName string, data []byte) (credentialFile, error) {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil || doc == nil {
		return credentialFile{}, accessErr(http.StatusBadRequest, "This file is not a JSON object, so it is not a credential file NeoProxy can read. %s", acceptedCredentialFiles)
	}
	if tokens, ok := doc["tokens"].(map[string]any); ok {
		return convertCodexCLI(doc, tokens)
	}
	if key, ok := doc["OPENAI_API_KEY"].(string); ok {
		if key = strings.TrimSpace(key); key != "" {
			return credentialFile{Format: "codex-cli", Provider: "codex", APIKey: key}, nil
		}
		return credentialFile{}, accessErr(http.StatusBadRequest, "This Codex CLI file has no sign-in tokens and no API key. Sign in with the Codex CLI first, then upload ~/.codex/auth.json again.")
	}
	if oauth, ok := doc["claudeAiOauth"].(map[string]any); ok {
		return convertClaudeCode(oauth)
	}
	if typ, ok := doc["type"].(string); ok && strings.TrimSpace(typ) != "" {
		typ = strings.ToLower(strings.TrimSpace(typ))
		name := filepath.Base(strings.TrimSpace(uploadName))
		if !safeFileName.MatchString(name) {
			sum := sha256.Sum256(data)
			name = typ + "-" + hex.EncodeToString(sum[:])[:12] + ".json"
		}
		return credentialFile{Format: "native", Provider: catalogProviderID(typ, ""), Name: name, Data: data}, nil
	}
	return credentialFile{}, accessErr(http.StatusBadRequest, "NeoProxy does not recognise this file. %s", acceptedCredentialFiles)
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

// convertCodexCLI turns {"tokens":{id_token,access_token,refresh_token,account_id},
// "last_refresh"} into the engine's codex auth file, named like a sign-in.
func convertCodexCLI(doc, tokens map[string]any) (credentialFile, error) {
	idToken, access, refresh := stringField(tokens, "id_token"), stringField(tokens, "access_token"), stringField(tokens, "refresh_token")
	if access == "" || refresh == "" {
		return credentialFile{}, accessErr(http.StatusBadRequest, "This Codex CLI file is missing its access or refresh token. Sign in with the Codex CLI again, then upload ~/.codex/auth.json.")
	}
	native := map[string]any{"type": "codex", "id_token": idToken, "access_token": access, "refresh_token": refresh, "account_id": stringField(tokens, "account_id"), "last_refresh": stringField(doc, "last_refresh")}
	email, plan, hash := "", "", ""
	if claims, err := codex.ParseJWTToken(idToken); err == nil && claims != nil {
		email = strings.TrimSpace(claims.Email)
		plan = chatGPTPlanID(claims.CodexAuthInfo.ChatgptPlanType)
		if accountID := claims.GetAccountID(); accountID != "" {
			sum := sha256.Sum256([]byte(accountID))
			hash = hex.EncodeToString(sum[:])[:8]
			if native["account_id"] == "" {
				native["account_id"] = accountID
			}
		}
	}
	if claims, err := codex.ParseJWTToken(access); err == nil && claims != nil && claims.Exp > 0 {
		native["expired"] = time.Unix(int64(claims.Exp), 0).UTC().Format(time.RFC3339)
	}
	if email != "" {
		native["email"] = email
	}
	if plan != "" {
		native["plan_type"] = plan
	}
	if email == "" && hash == "" {
		sum := sha256.Sum256([]byte(refresh))
		hash = hex.EncodeToString(sum[:])[:8]
	}
	data, err := json.MarshalIndent(native, "", "  ")
	if err != nil {
		return credentialFile{}, err
	}
	return credentialFile{Format: "codex-cli", Provider: "codex", Name: codex.CredentialFileName(email, plan, hash, true), Data: data}, nil
}

// convertClaudeCode turns {"claudeAiOauth":{accessToken,refreshToken,expiresAt,
// subscriptionType,rateLimitTier}} into the engine's claude auth file.
func convertClaudeCode(oauth map[string]any) (credentialFile, error) {
	access, refresh := stringField(oauth, "accessToken"), stringField(oauth, "refreshToken")
	if access == "" || refresh == "" {
		return credentialFile{}, accessErr(http.StatusBadRequest, "This Claude Code file is missing its access or refresh token. Run claude and sign in again, then upload ~/.claude/.credentials.json.")
	}
	native := map[string]any{"type": "claude", "access_token": access, "refresh_token": refresh}
	if expires, ok := oauth["expiresAt"].(float64); ok && expires > 0 {
		native["expired"] = time.UnixMilli(int64(expires)).UTC().Format(time.RFC3339)
	}
	sub := strings.ToLower(stringField(oauth, "subscriptionType"))
	if sub != "" {
		native["subscription_type"] = sub
	}
	if tier := stringField(oauth, "rateLimitTier"); tier != "" {
		native["rate_limit_tier"] = tier
	}
	data, err := json.MarshalIndent(native, "", "  ")
	if err != nil {
		return credentialFile{}, err
	}
	sum := sha256.Sum256([]byte(refresh))
	name := "claude-code-" + hex.EncodeToString(sum[:])[:12] + ".json"
	return credentialFile{Format: "claude-code", Provider: "claude", Name: name, Data: data}, nil
}
