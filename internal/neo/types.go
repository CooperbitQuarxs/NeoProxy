// Package neo owns durable dashboard identity and inference-key authority.
package neo

import (
	"context"
	"errors"
	"strings"
	"unicode"
)

const (
	RoleAdmin   = "admin"
	RoleOwner   = "owner"
	RoleMember  = "member"
	RoleService = "service"
)

var (
	ErrUnauthenticated = errors.New("invalid credentials")
	ErrForbidden       = errors.New("access denied")
	ErrSuspended       = errors.New("user is suspended or inactive")
	ErrNotFound        = errors.New("not found")
	ErrConflict        = errors.New("already exists or bootstrap completed")
	ErrInvalid         = errors.New("invalid input")
)

type Grant struct {
	VirtualModel string  `json:"virtualModel"`
	Allotment    float64 `json:"allotment"`
}

type Limits struct {
	RPM          *int64 `json:"rpm"`
	TokensPerDay *int64 `json:"tokensPerDay"`
}

// Key.Models narrows the direct models a key may call ("<provider>/<model>"
// patterns, a trailing "*" matches any suffix); empty means no extra narrowing.
type Key struct {
	ID         string   `json:"id"`
	Prefix     string   `json:"prefix"`
	Label      string   `json:"label"`
	AccountIDs []string `json:"accountIds,omitempty"`
	Models     []string `json:"models,omitempty"`
	CreatedAt  int64    `json:"createdAt"`
	LastUsedAt *int64   `json:"lastUsedAt"`
	ExpiresAt  *int64   `json:"expiresAt,omitempty"`
}

// ModelAccess narrows direct provider access per provider: a provider in
// ProviderAccess with an entry may serve only the models matching one of its
// patterns (the model id without the "<provider>/" prefix, exact or with a
// trailing "*"). A provider without an entry serves every exposed model.
type ModelAccess map[string][]string

type User struct {
	ID             string      `json:"id"`
	Handle         string      `json:"handle"`
	Name           string      `json:"name"`
	Role           string      `json:"role"`
	State          string      `json:"state"`
	Keys           []Key       `json:"keys"`
	ProviderAccess []string    `json:"providerAccess"`
	ModelAccess    ModelAccess `json:"modelAccess,omitempty"`
	Grants         []Grant     `json:"grants"`
	Limits         Limits      `json:"limits"`
	CreatedAt      int64       `json:"createdAt"`
}

// UserInput replaces organization policy; a blank Password preserves its verifier
// and a nil ModelAccess preserves the stored allowlist (send {} to clear it).
// ModelAccess entries for providers outside ProviderAccess are dropped.
type UserInput struct {
	Handle         string      `json:"handle"`
	Name           string      `json:"name"`
	Role           string      `json:"role"`
	State          string      `json:"state"`
	ProviderAccess []string    `json:"providerAccess"`
	ModelAccess    ModelAccess `json:"modelAccess,omitempty"`
	Grants         []Grant     `json:"grants"`
	Limits         Limits      `json:"limits"`
	Password       string      `json:"password,omitempty"`
}

// Principal is trusted server identity, never decoded from client JSON. Session is
// true only after server-side cookie verification; an admin key is not a session.
// A nil AccountIDs means no additional key scope; non-nil is an intersection.
// KeyModels likewise narrows direct models for the key; nil means no narrowing.
type Principal struct {
	UserID         string
	KeyID          string
	Role           string
	ProviderAccess []string
	ModelAccess    ModelAccess
	Grants         []Grant
	AccountIDs     []string
	KeyModels      []string
	Limits         Limits
	Session        bool
}

func (p Principal) IsAdmin() bool                { return p.Session && p.Role == RoleAdmin }
func (p Principal) CanAccessUser(id string) bool { return p.IsAdmin() || p.UserID == id }
func (p Principal) AllowsAccount(id string) bool {
	if p.AccountIDs == nil {
		return true
	}
	for _, allowed := range p.AccountIDs {
		if allowed == id {
			return true
		}
	}
	return false
}

// AllowsDirect reports whether the principal may call model (the full
// "<provider>/<model>" id) of providerID directly, bypassing virtual models.
func (p Principal) AllowsDirect(providerID, model string) bool {
	found := false
	for _, id := range p.ProviderAccess {
		if id == providerID {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	if patterns, ok := p.ModelAccess[providerID]; ok && !MatchModel(patterns, strings.TrimPrefix(model, providerID+"/")) {
		return false
	}
	return p.KeyModels == nil || MatchModel(p.KeyModels, model)
}

// MatchModel reports whether id equals a pattern or starts with the prefix of
// a pattern ending in "*".
func MatchModel(patterns []string, id string) bool {
	for _, pattern := range patterns {
		if prefix, wild := strings.CutSuffix(pattern, "*"); wild {
			if strings.HasPrefix(id, prefix) {
				return true
			}
		} else if pattern == id {
			return true
		}
	}
	return false
}

// validModelPattern accepts an id or a prefix ending in one "*": printable,
// without whitespace, at most 200 bytes.
func validModelPattern(pattern string) bool {
	if pattern == "" || len(pattern) > 200 || strings.Count(pattern, "*") > 1 || (strings.Contains(pattern, "*") && !strings.HasSuffix(pattern, "*")) {
		return false
	}
	for _, r := range pattern {
		if r <= ' ' || r == 0x7f || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validModelPatterns(patterns []string, limit int) bool {
	if len(patterns) == 0 || len(patterns) > limit {
		return false
	}
	seen := make(map[string]bool, len(patterns))
	for _, pattern := range patterns {
		if !validModelPattern(pattern) || seen[pattern] {
			return false
		}
		seen[pattern] = true
	}
	return true
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	if ctx == nil {
		return Principal{}, false
	}
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok && p.UserID != ""
}

type Session struct {
	Role      string `json:"role"`
	UserID    string `json:"userId"`
	Handle    string `json:"handle"`
	ExpiresAt int64  `json:"expiresAt"`
}

// AccountScopeValidator rejects every account not currently owned by userID.
// B installs this before owner keys can be minted or used. It must not retain a
// transaction or call back into the Store while a mutation is in progress.
type AccountScopeValidator func(ctx context.Context, userID string, accountIDs []string) error
