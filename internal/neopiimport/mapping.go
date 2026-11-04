package neopiimport

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Import kinds, matching the dashboard's AccessItem vocabulary.
const (
	KindAPIKey       = "apiKey"
	KindSubscription = "subscription"
)

// route maps one NeoPi provider and credential type onto NeoProxy.
type route struct {
	kind string
	// target is the dashboard catalog id for API keys, or the engine provider
	// (auth-file "type") for subscriptions.
	target string
	// reason is set when NeoProxy cannot use the credential.
	reason string
}

// apiKeyRoutes maps NeoPi API-key providers onto the dashboard catalog.
// Catalog ids codex/claude/gemini are the engine's native OpenAI/Anthropic/Gemini
// key paths; the rest are OpenAI-compatible entries with fixed base URLs, except
// the self-hosted Marineris gateway whose address comes from the profile.
var apiKeyRoutes = map[string]route{
	"openai":             {target: "codex"},
	"anthropic":          {target: "claude"},
	"google":             {target: "gemini"},
	"xai":                {target: "xai"},
	"meta":               {target: "meta"},
	"openrouter":         {target: "openrouter"},
	"deepseek":           {target: "deepseek"},
	"groq":               {target: "groq"},
	"mistral":            {target: "mistral"},
	"together":           {target: "together"},
	"fireworks":          {target: "fireworks"},
	"cerebras":           {target: "cerebras"},
	"moonshot":           {target: "moonshot"},
	"zai":                {target: "zai"},
	"nanogpt":            {target: "nanogpt"},
	"huggingface":        {target: "huggingface"},
	"nvidia":             {target: "nvidia"},
	"vercel-ai-gateway":  {target: "vercel-ai-gateway"},
	"nous-portal":        {target: "nous-portal"},
	"marineris":          {target: "marineris"},
	"opencode-go":        {reason: "OpenCode Go routes some models through Anthropic or Gemini dialects, which a single OpenAI-compatible entry cannot reproduce."},
	"opencode-zen":       {reason: "OpenCode Zen routes some models through Anthropic or Gemini dialects, which a single OpenAI-compatible entry cannot reproduce."},
	"alibaba-token-plan": {reason: "Alibaba Token Plan stores a session bundle, not a plain API key."},
	"sakana":             {reason: "Sakana is served through the OpenAI Responses API only; NeoProxy's OpenAI-compatible entries use chat completions."},
	"abliteration":       {reason: "Abliteration is served through the OpenAI Responses API only; NeoProxy's OpenAI-compatible entries use chat completions."},
}

// oauthRoutes maps NeoPi subscription sign-ins onto engine auth-file types.
var oauthRoutes = map[string]route{
	"openai-codex":       {target: "codex"},
	"anthropic":          {target: "claude"},
	"google-antigravity": {target: "antigravity"},
	"xai-oauth":          {target: "xai"},
	"kimi-code":          {target: "kimi"},
	"devin":              {target: "devin"},
	// Z.ai's sign-in mints a durable API key into the access field; there is
	// nothing to refresh, so it imports as an ordinary API key.
	"zai":               {kind: KindAPIKey, target: "zai"},
	"google-gemini-cli": {reason: "NeoProxy has no Gemini CLI subscription engine yet; add a Gemini API key instead."},
	"cursor":            {reason: "Cursor uses its own agent protocol; NeoProxy has no Cursor engine."},
	"github-copilot":    {reason: "NeoProxy has no GitHub Copilot engine."},
}

func routeFor(c Credential) route {
	var r route
	var ok bool
	switch c.Type {
	case "api_key":
		r, ok = apiKeyRoutes[c.Provider]
		if r.kind == "" {
			r.kind = KindAPIKey
		}
	case "oauth":
		r, ok = oauthRoutes[c.Provider]
		if r.kind == "" {
			r.kind = KindSubscription
		}
	default:
		return route{kind: KindAPIKey, reason: "NeoPi stores this credential in a format NeoProxy does not recognize."}
	}
	if !ok {
		r.reason = "NeoProxy has no matching provider for NeoPi's \"" + c.Provider + "\"."
	}
	return r
}

// profileBaseURL returns the address NeoPi uses for a provider whose endpoint
// lives in the profile rather than in the credential. NeoPi's Marineris
// extension keeps the gateway URL in extensions/marineris/config.json and
// normalizes it to an http(s) base ending in /v1; an absent file means the
// gateway's default address, which is also the catalog's.
func profileBaseURL(profile, provider string) string {
	if provider != "marineris" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(profile, "extensions", "marineris", "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		BaseURL string `json:"baseUrl"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base
}

// Candidate describes one importable (or not) credential without any secret.
type Candidate struct {
	CredentialID     string  `json:"credentialId"`
	Provider         string  `json:"provider"`
	NeoProxyProvider *string `json:"neoproxyProvider"`
	Kind             string  `json:"kind"`
	Label            string  `json:"label"`
	Plan             *string `json:"plan"`
	ExpiresAt        int64   `json:"expiresAt,omitempty"`
	Importable       bool    `json:"importable"`
	Reason           string  `json:"reason,omitempty"`
	AlreadyImported  *string `json:"alreadyImported"`
}

// Describe classifies a credential for the import list.
func Describe(c Credential, now time.Time) Candidate {
	r := routeFor(c)
	out := Candidate{CredentialID: c.ID, Provider: c.Provider, Kind: r.kind, Label: label(c, r)}
	if r.target != "" {
		target := r.target
		out.NeoProxyProvider = &target
	}
	if plan := planOf(c, r); plan != "" {
		out.Plan = &plan
	}
	if r.kind == KindSubscription {
		if exp := expiresAt(c); !exp.IsZero() {
			out.ExpiresAt = exp.UnixMilli()
		}
	}
	out.Reason = reasonNotImportable(c, r, now)
	out.Importable = out.Reason == ""
	return out
}

func reasonNotImportable(c Credential, r route, now time.Time) string {
	switch {
	case c.Disabled:
		return "NeoPi has disabled this credential."
	case r.reason != "":
		return r.reason
	case r.kind == KindAPIKey:
		if _, has := c.data["tlsTrust"]; has {
			return "This key pins a custom TLS trust in NeoPi, which NeoProxy cannot reproduce."
		}
		if _, has := c.data["requestOrigin"]; has {
			return "This key is bound to a custom request origin in NeoPi, which NeoProxy cannot reproduce."
		}
		if apiKeyValue(c) == "" {
			return "NeoPi's record has no key."
		}
	case r.kind == KindSubscription:
		if str(c.data, "access") == "" {
			return "NeoPi's record has no access token."
		}
		if exp := expiresAt(c); !exp.IsZero() && !exp.After(now) {
			return "NeoPi's access token has expired. Use this account once in NeoPi so it refreshes, then import."
		}
		if r.target == "antigravity" && str(c.data, "projectId") == "" {
			return "NeoPi has not discovered this account's Antigravity project yet. Use it once in NeoPi, then import."
		}
	}
	return ""
}

// ErrNotImportable wraps the human reason a credential cannot be imported.
var ErrNotImportable = errors.New("not importable")

func checkImportable(c Credential, now time.Time) (route, error) {
	r := routeFor(c)
	if reason := reasonNotImportable(c, r, now); reason != "" {
		return r, fmt.Errorf("%w: %s", ErrNotImportable, reason)
	}
	return r, nil
}

// APIKey is an imported pay-as-you-go key for the dashboard's API-key path.
type APIKey struct {
	// Provider is the dashboard catalog id.
	Provider string
	APIKey   string
	// BaseURL is set only for self-hosted providers whose address NeoPi keeps
	// outside the credential; empty means the catalog's fixed address.
	BaseURL string
	Label   string
}

// BuildAPIKey returns the catalog entry for an importable API-key credential.
func BuildAPIKey(c Credential, now time.Time) (APIKey, error) {
	r, err := checkImportable(c, now)
	if err != nil {
		return APIKey{}, err
	}
	if r.kind != KindAPIKey {
		return APIKey{}, fmt.Errorf("%w: this is a subscription sign-in, not an API key", ErrNotImportable)
	}
	return APIKey{Provider: r.target, APIKey: apiKeyValue(c), BaseURL: c.baseURL, Label: label(c, r)}, nil
}

// AuthFile is an imported subscription in the engine's native auth-file format.
type AuthFile struct {
	// Provider is the engine provider ("type" in the file).
	Provider  string
	FileName  string
	Data      []byte
	Label     string
	Plan      string
	ExpiresAt time.Time
}

// FileName is the stable engine auth-file name for one imported credential.
// It is derived from the profile and NeoPi id so a re-import replaces in place
// and never collides with a credential signed in directly in NeoProxy.
func FileName(engineProvider, profile, credentialID string) string {
	sum := sha256.Sum256([]byte(profile + "\x00" + credentialID))
	return "neopi-" + engineProvider + "-" + hex.EncodeToString(sum[:6]) + ".json"
}

// BuildAuthFile converts an importable subscription credential into the engine's
// native auth-file JSON, refresh-locked and with provenance. The refresh token
// is deliberately not copied: NeoPi stays the only party that can renew it.
func BuildAuthFile(c Credential, profile string, importedAt, now time.Time) (AuthFile, error) {
	r, err := checkImportable(c, now)
	if err != nil {
		return AuthFile{}, err
	}
	if r.kind != KindSubscription {
		return AuthFile{}, fmt.Errorf("%w: this is an API key, not a subscription sign-in", ErrNotImportable)
	}
	access := str(c.data, "access")
	email := str(c.data, "email")
	meta := map[string]any{"type": r.target}
	switch r.target {
	case "codex":
		claims := jwtClaims(access)
		accountID := str(c.data, "accountId")
		if accountID == "" {
			accountID = nestedStr(claims, "https://api.openai.com/auth", "chatgpt_account_id")
		}
		if email == "" {
			email = nestedStr(claims, "https://api.openai.com/profile", "email")
		}
		meta["access_token"] = access
		setNonEmpty(meta, "account_id", accountID)
		setNonEmpty(meta, "plan_type", planOf(c, r))
	case "claude":
		meta["access_token"] = access
		setNonEmpty(meta, "account_uuid", str(c.data, "accountId"))
		setNonEmpty(meta, "organization_uuid", str(c.data, "orgId"))
		setNonEmpty(meta, "organization_name", str(c.data, "orgName"))
	case "antigravity":
		meta["access_token"] = access
		meta["project_id"] = str(c.data, "projectId")
	case "xai":
		meta["access_token"] = access
		meta["auth_kind"] = "oauth"
		setNonEmpty(meta, "sub", str(c.data, "accountId"))
	case "kimi":
		meta["access_token"] = access
		meta["token_type"] = "Bearer"
	case "devin":
		token := access
		if !strings.HasPrefix(token, "devin-session-token$") {
			token = "devin-session-token$" + token
		}
		meta["api_key"] = token
		meta["session_token"] = token
		meta["auth_kind"] = "oauth"
		setNonEmpty(meta, "org_id", str(c.data, "orgId"))
	default:
		return AuthFile{}, fmt.Errorf("%w: no auth-file format for %s", ErrNotImportable, r.target)
	}
	setNonEmpty(meta, "email", email)
	exp := expiresAt(c)
	if !exp.IsZero() {
		meta["expired"] = exp.UTC().Format(time.RFC3339)
	}
	meta[coreauth.MetadataRefreshLocked] = true
	meta[coreauth.MetadataImportedFrom] = map[string]any{
		"source":        "neopi",
		"profile":       profile,
		"credential_id": c.ID,
		"provider":      c.Provider,
		"imported_at":   importedAt.UnixMilli(),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return AuthFile{}, err
	}
	return AuthFile{Provider: r.target, FileName: FileName(r.target, profile, c.ID), Data: data, Label: label(c, r), Plan: planOf(c, r), ExpiresAt: exp}, nil
}

func apiKeyValue(c Credential) string {
	if c.Type == "oauth" {
		return str(c.data, "access")
	}
	return str(c.data, "key")
}

// label is the email for sign-ins, or the last four characters of a key.
func label(c Credential, r route) string {
	if r.kind == KindSubscription {
		if email := str(c.data, "email"); email != "" {
			return email
		}
		if c.Provider == "openai-codex" {
			if email := nestedStr(jwtClaims(str(c.data, "access")), "https://api.openai.com/profile", "email"); email != "" {
				return email
			}
		}
		return "NeoPi credential " + c.ID
	}
	if email := str(c.data, "email"); email != "" && c.Type == "oauth" {
		return email
	}
	key := apiKeyValue(c)
	if len(key) < 12 {
		return "NeoPi key " + c.ID
	}
	return "…" + key[len(key)-4:]
}

func planOf(c Credential, r route) string {
	if r.target != "codex" || r.kind != KindSubscription {
		return ""
	}
	if plan := nestedStr(jwtClaims(str(c.data, "access")), "https://api.openai.com/auth", "chatgpt_plan_type"); plan != "" {
		return strings.ToLower(plan)
	}
	// NeoPi stores the Codex plan type as the organization name.
	return strings.ToLower(str(c.data, "orgName"))
}

func expiresAt(c Credential) time.Time {
	switch v := c.data["expires"].(type) {
	case float64:
		if v > 0 {
			return time.UnixMilli(int64(v))
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return time.UnixMilli(n)
		}
	}
	return time.Time{}
}

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

func nestedStr(m map[string]any, outer, key string) string {
	inner, _ := m[outer].(map[string]any)
	return str(inner, key)
}

func setNonEmpty(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// jwtClaims decodes a JWT payload without verifying it; used only for display
// metadata (plan, account) that NeoPi itself derived the same way.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}
