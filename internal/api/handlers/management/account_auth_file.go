package management

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// SaveAccountAuthFile persists a native engine auth file for the dashboard and
// registers it with the auth manager through the same synthesis path as an
// uploaded file. With bindOwner, the account owner carried by ctx (or the
// authenticated dashboard principal) is bound before anything is written, so an
// ownership conflict leaves no file behind. Without it, the file replaces an
// existing credential in place and its ownership, meters and routes are kept.
func (h *Handler) SaveAccountAuthFile(ctx context.Context, name string, data []byte, bindOwner bool) (*coreauth.Auth, error) {
	name = strings.TrimSpace(name)
	if isUnsafeAuthFileName(name) || filepath.Base(name) != name || !strings.HasSuffix(strings.ToLower(name), ".json") {
		return nil, fmt.Errorf("%w: unsafe auth file name", errInvalidAuthFile)
	}
	if h == nil || h.cfg == nil || h.authManager == nil || strings.TrimSpace(h.cfg.AuthDir) == "" {
		return nil, fmt.Errorf("account credential storage is unavailable")
	}
	dst := filepath.Join(h.cfg.AuthDir, name)
	if abs, errAbs := filepath.Abs(dst); errAbs == nil {
		dst = abs
	}
	auth, err := h.buildAuthFromFileData(dst, data)
	if err != nil {
		return nil, err
	}
	if bindOwner {
		ctx = inheritAccountLoginOwner(ctx, ctx)
		if h.accountOwnershipHook == nil || coreauth.AccountLoginOwner(ctx) == "" {
			return nil, fmt.Errorf("account login is unavailable")
		}
		if err = h.accountOwnershipHook(ctx, auth); err != nil {
			return nil, err
		}
	} else if _, ok := h.authManager.GetByID(auth.ID); !ok {
		return nil, fmt.Errorf("%w: %s", errAuthFileNotFound, name)
	}
	if err = h.writeAuthFileBytes(dst, data, auth); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}
	if err = h.upsertAuthRecord(ctx, auth); err != nil {
		return nil, err
	}
	if h.postAuthPersistHook != nil {
		if errHook := h.postAuthPersistHook(ctx, auth); errHook != nil {
			return nil, fmt.Errorf("post-auth persist hook failed: %w", errHook)
		}
	}
	if saved, ok := h.authManager.GetByID(auth.ID); ok {
		return saved, nil
	}
	return auth, nil
}
