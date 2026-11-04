package auth

import (
	"context"
	"errors"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

var ErrAccountKeyConflict = errors.New("credential already configured")
var ErrAccountKeyInvalid = errors.New("unsupported provider or invalid API key")
var ErrAccountNotFound = errors.New("account credential not found")
var ErrAccountUnsupported = errors.New("operation is not supported for this credential")

// AccountKey describes one pay-as-you-go credential for CreateAccountAPIKey.
// Native providers (claude, gemini, codex, xai, meta) use their fixed upstream.
// Provider "openai-compatibility" appends an openai-compatibility entry named
// CompatName at BaseURL; Models are the upstream model IDs the engine serves.
// RequestScopedErrors seeds a new openai-compatibility entry's upstream error
// classification (provider quirks known to the dashboard catalogue).
type AccountKey struct {
	Provider            string
	APIKey              string
	BaseURL             string
	CompatName          string
	Models              []string
	RequestScopedErrors []internalconfig.RequestScopedErrorRule
}

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
