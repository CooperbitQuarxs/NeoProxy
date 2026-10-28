// Package neo owns durable dashboard identity and inference-key authority.
package neo

import (
	"context"
	"errors"
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

type Key struct {
	ID         string   `json:"id"`
	Prefix     string   `json:"prefix"`
	Label      string   `json:"label"`
	AccountIDs []string `json:"accountIds,omitempty"`
	CreatedAt  int64    `json:"createdAt"`
	LastUsedAt *int64   `json:"lastUsedAt"`
	ExpiresAt  *int64   `json:"expiresAt,omitempty"`
}

type User struct {
	ID             string   `json:"id"`
	Handle         string   `json:"handle"`
	Name           string   `json:"name"`
	Role           string   `json:"role"`
	State          string   `json:"state"`
	Keys           []Key    `json:"keys"`
	ProviderAccess []string `json:"providerAccess"`
	Grants         []Grant  `json:"grants"`
	Limits         Limits   `json:"limits"`
	CreatedAt      int64    `json:"createdAt"`
}

// UserInput replaces organization policy; a blank Password preserves its verifier.
type UserInput struct {
	Handle         string   `json:"handle"`
	Name           string   `json:"name"`
	Role           string   `json:"role"`
	State          string   `json:"state"`
	ProviderAccess []string `json:"providerAccess"`
	Grants         []Grant  `json:"grants"`
	Limits         Limits   `json:"limits"`
	Password       string   `json:"password,omitempty"`
}

// Principal is trusted server identity, never decoded from client JSON. Session is
// true only after server-side cookie verification; an admin key is not a session.
// A nil AccountIDs means no additional key scope; non-nil is an intersection.
type Principal struct {
	UserID         string
	KeyID          string
	Role           string
	ProviderAccess []string
	Grants         []Grant
	AccountIDs     []string
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
