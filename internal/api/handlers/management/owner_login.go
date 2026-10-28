package management

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func inheritAccountLoginOwner(ctx, source context.Context) context.Context {
	if owner := coreauth.AccountLoginOwner(source); owner != "" {
		return coreauth.WithAccountLoginOwner(ctx, owner)
	}
	if p, ok := neo.PrincipalFromContext(source); ok && p.Session && (p.Role == "admin" || p.Role == "owner") {
		return coreauth.WithAccountLoginOwner(ctx, p.UserID)
	}
	return ctx
}

func bindPluginLoginOwner(ctx context.Context, state string) {
	oauthSessions.mu.Lock()
	defer oauthSessions.mu.Unlock()
	session, ok := oauthSessions.sessions[state]
	if !ok {
		return
	}
	session.AccountOwnerID = coreauth.AccountLoginOwner(ctx)
	oauthSessions.sessions[state] = session
}

func oauthInitiatingOwner(ctx context.Context, state string) context.Context {
	session, ok := oauthSessions.Get(state)
	if !ok {
		return coreauth.WithAccountLoginOwner(ctx, "")
	}
	return coreauth.WithAccountLoginOwner(ctx, session.AccountOwnerID)
}

// SetAccountOwnershipHook installs the SQLite ownership check independently of
// application post-auth hooks. Configure once before accepting login requests.
func (h *Handler) SetAccountOwnershipHook(hook coreauth.PostAuthHook) { h.accountOwnershipHook = hook }

// CreateAccountAPIKey appends a credential to the EXISTING engine configuration,
// using its synthesizer, stable identities and reload path. It never accepts
// arbitrary upstream URLs, config patches, headers, filenames or auth IDs from
// an account owner, and never reveals existing credentials.
func (h *Handler) CreateAccountAPIKey(ctx context.Context, provider, key string) (*coreauth.Auth, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 16384 || coreauth.AccountLoginOwner(inheritAccountLoginOwner(ctx, ctx)) == "" {
		return nil, coreauth.ErrAccountKeyInvalid
	}
	ctx = inheritAccountLoginOwner(ctx, ctx)
	h.mu.Lock()
	if h.cfg == nil || h.configFilePath == "" || h.accountOwnershipHook == nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("account login is unavailable")
	}
	next := h.cfg.CloneForRuntime()
	synth := synthesizer.NewConfigSynthesizer()
	existing, err := synth.Synthesize(&synthesizer.SynthesisContext{Config: next, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	for _, a := range existing {
		if a.Attributes["api_key"] == key {
			h.mu.Unlock()
			return nil, coreauth.ErrAccountKeyConflict
		}
	}
	switch provider {
	case "claude":
		next.ClaudeKey = append(next.ClaudeKey, config.ClaudeKey{APIKey: key, BaseURL: "https://api.anthropic.com"})
	case "gemini":
		next.GeminiKey = append(next.GeminiKey, config.GeminiKey{APIKey: key, BaseURL: "https://generativelanguage.googleapis.com"})
	case "codex":
		next.CodexKey = append(next.CodexKey, config.CodexKey{APIKey: key, BaseURL: "https://api.openai.com/v1"})
	case "xai":
		next.XAIKey = append(next.XAIKey, config.CodexKey{APIKey: key, BaseURL: "https://api.x.ai/v1"})
	case "meta":
		next.MetaKey = append(next.MetaKey, config.CodexKey{APIKey: key, BaseURL: "https://api.meta.ai/v1"})
	default:
		h.mu.Unlock()
		return nil, coreauth.ErrAccountKeyInvalid
	}
	auths, err := synth.Synthesize(&synthesizer.SynthesisContext{Config: next, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	var added *coreauth.Auth
	for _, a := range auths {
		if a.Provider == provider && a.Attributes["api_key"] == key {
			added = a
			break
		}
	}
	if added == nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("engine did not synthesize the new credential")
	}
	if err = h.accountOwnershipHook(ctx, added); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	if err = config.SaveConfigPreserveComments(h.configFilePath, next, true); err != nil {
		h.mu.Unlock()
		return nil, fmt.Errorf("save account credential: %w", err)
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.reloadConfigAfterManagementSave(context.WithoutCancel(ctx), snapshot)
	return added, nil
}
