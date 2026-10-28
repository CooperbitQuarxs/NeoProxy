package auth

import (
	"context"
	"errors"
)

var ErrAccountKeyConflict = errors.New("credential already configured")
var ErrAccountKeyInvalid = errors.New("unsupported provider or invalid API key")

type accountLoginOwnerKey struct{}

// WithAccountLoginOwner carries a server-authenticated initiating owner through
// existing asynchronous engine login flows. Never populate it from client IDs.
func WithAccountLoginOwner(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, accountLoginOwnerKey{}, userID)
}
func AccountLoginOwner(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	owner, _ := ctx.Value(accountLoginOwnerKey{}).(string)
	return owner
}
