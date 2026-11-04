package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func resolveCodexAuthDiskPath(cfg *config.Config, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", nil
	}
	var candidates []string
	if len(auth.Attributes) > 0 {
		if p := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributePath]); p != "" {
			return p, nil
		}
		if s := strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeSource]); s != "" && !strings.Contains(s, ":") {
			candidates = append(candidates, s)
		}
	}
	for _, name := range []string{strings.TrimSpace(auth.FileName), strings.TrimSpace(auth.ID)} {
		if name == "" {
			continue
		}
		if filepath.IsAbs(name) {
			candidates = append(candidates, name)
		} else if cfg != nil && strings.TrimSpace(cfg.AuthDir) != "" {
			candidates = append(candidates, filepath.Join(strings.TrimSpace(cfg.AuthDir), name))
		}
	}
	for _, candidate := range candidates {
		info, errStat := os.Stat(candidate)
		if errStat != nil {
			if os.IsNotExist(errStat) {
				continue
			}
			return "", fmt.Errorf("codex executor: stat credential file %s: %w", candidate, errStat)
		}
		if !info.IsDir() {
			return candidate, nil
		}
	}
	if len(candidates) > 0 {
		// A removed persisted row is not an in-memory credential to refresh.
		return candidates[0], nil
	}
	return "", nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	if cliproxyauth.IsRefreshLocked(auth) {
		return nil, cliproxyauth.ErrRefreshLocked
	}
	if e == nil {
		e = NewCodexExecutor(nil)
	}
	if auth.ID == "" {
		return e.refreshCredential(ctx, auth)
	}
	result := e.refresh.flight.DoChan(auth.ID, func() (any, error) {
		return e.refreshCredential(ctx, auth)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case completed := <-result:
		if refreshed, ok := completed.Val.(*cliproxyauth.Auth); ok && refreshed != nil {
			return refreshed.Clone(), completed.Err
		}
		return nil, completed.Err
	}
}

func (e *CodexExecutor) refreshCredential(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	cfg := e.cfg
	homeEnabled := cfg != nil && cfg.Home.Enabled

	diskPath, errPath := resolveCodexAuthDiskPath(cfg, auth)
	if errPath != nil {
		return nil, errPath
	}
	if diskPath != "" {
		var outAuth *cliproxyauth.Auth
		errLock := codexauth.WithFileLockContext(ctx, diskPath, func() error {
			var errInner error
			outAuth, errInner = e.refreshLocked(ctx, auth, diskPath, homeEnabled)
			return errInner
		})
		return outAuth, errLock
	}
	return e.refreshLocked(ctx, auth, "", homeEnabled)
}

func (e *CodexExecutor) refreshLocked(ctx context.Context, observed *cliproxyauth.Auth, diskPath string, homeEnabled bool) (*cliproxyauth.Auth, error) {
	var cfg *config.Config
	if e != nil {
		cfg = e.cfg
	}
	auth := observed.Clone()
	auth.CodexRefreshPatch = nil
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	var diskBaseline map[string]any
	if diskPath != "" {
		raw, errRead := os.ReadFile(diskPath)
		if errRead != nil {
			return nil, fmt.Errorf("codex executor: read credential file: %w", errRead)
		}
		if errJSON := json.Unmarshal(raw, &diskBaseline); errJSON != nil {
			return nil, fmt.Errorf("codex executor: invalid credential JSON: %w", errJSON)
		}
		if diskBaseline == nil {
			return nil, fmt.Errorf("codex executor: empty credential JSON")
		}
		// The persisted row is authoritative, including a login or rotation by a peer.
		auth.Metadata = diskBaseline
		if cliproxyauth.IsRefreshLocked(auth) {
			return nil, cliproxyauth.ErrRefreshLocked
		}
	}
	if disabled, _ := auth.Metadata["disabled"].(bool); auth.Disabled || disabled {
		return nil, fmt.Errorf("codex executor: credential is disabled")
	}
	rotated := false
	for _, key := range []string{"access_token", "refresh_token", "expired", "account_id", "email"} {
		if !reflect.DeepEqual(auth.Metadata[key], observed.Metadata[key]) {
			rotated = true
			break
		}
	}
	if rotated {
		auth.RejectedAccessToken = ""
	}
	clock := time.Now
	if e.refreshClock != nil {
		clock = e.refreshClock
	}
	now := clock()
	if !cliproxyauth.IsForceRefreshContext(ctx) && auth.RejectedAccessToken != "" {
		access, _ := auth.Metadata["access_token"].(string)
		e.refresh.mu.Lock()
		mint, present := e.refresh.mints[auth.ID]
		e.refresh.mu.Unlock()
		usable := auth.Clone()
		usable.RejectedAccessToken = ""
		expiry, known := usable.ExpirationTime()
		if present && mint.access == access && now.Sub(mint.at) < 5*time.Minute && known && expiry.After(now.Add(time.Minute)) {
			return usable, nil
		}
	}
	_, hasRefresh, needsRefresh := cliproxyauth.CodexNeedsRefresh(auth, now)
	force := cliproxyauth.IsForceRefreshContext(ctx)
	if (!force || rotated) && !needsRefresh || !hasRefresh && !homeEnabled {
		if rotated {
			auth.CodexRefreshPatch = codexauth.NewCredentialRefreshPatch(observed.Metadata, diskBaseline, auth.Metadata)
		}
		return auth, nil
	}

	// NeoPi bounds credential acquisition, not the subsequent inference stream.
	refreshCtx, cancelRefresh := context.WithTimeout(ctx, 10*time.Second)
	defer cancelRefresh()
	var errRefresh error
	if homeEnabled {
		var refreshed *cliproxyauth.Auth
		var handled bool
		refreshed, handled, errRefresh = helps.RefreshAuthViaHome(refreshCtx, cfg, auth)
		if errRefresh == nil {
			if !handled || refreshed == nil {
				return nil, fmt.Errorf("codex executor: credential broker returned no refresh")
			}
			auth = refreshed
		}
	} else {
		refreshToken, _ := auth.Metadata["refresh_token"].(string)
		svc := codexauth.NewCodexAuthWithProxyURL(cfg, auth.ProxyURL)
		var token *codexauth.CodexTokenData
		if e.refreshToken != nil {
			token, errRefresh = e.refreshToken(refreshCtx, auth)
		} else {
			token, errRefresh = svc.RefreshTokens(refreshCtx, refreshToken)
		}
		if errRefresh == nil {
			// Clone before mutation: diskBaseline remains the compare-and-set version.
			auth = auth.Clone()
			auth.Metadata["access_token"] = token.AccessToken
			auth.Metadata["refresh_token"] = token.RefreshToken
			auth.Metadata["expired"] = token.Expire
			auth.Metadata["last_refresh"] = now.Format(time.RFC3339Nano)
			auth.Metadata["type"] = "codex"
			if token.IDToken != "" {
				auth.Metadata["id_token"] = token.IDToken
			}
			if token.AccountID != "" {
				auth.Metadata["account_id"] = token.AccountID
			}
			if token.Email != "" {
				auth.Metadata["email"] = token.Email
			}
			if storage, ok := auth.Storage.(*codexauth.CodexTokenStorage); ok && storage != nil {
				clonedStorage := *storage
				svc.UpdateTokenStorage(&clonedStorage, token)
				auth.Storage = &clonedStorage
			}
		}
	}
	if errRefresh != nil {
		if !util.IsDefinitiveOAuthRefreshFailure(errRefresh.Error()) {
			return nil, errRefresh
		}
		patch := codexauth.NewCredentialDisablePatch(observed.Metadata, auth.Metadata)
		latest := auth.Metadata
		if diskPath != "" {
			raw, errRead := os.ReadFile(diskPath)
			if errRead != nil {
				return nil, fmt.Errorf("codex executor: re-read failed credential: %w", errRead)
			}
			latest = nil
			if errJSON := json.Unmarshal(raw, &latest); errJSON != nil || latest == nil {
				return nil, fmt.Errorf("codex executor: invalid failed credential JSON")
			}
		}
		failed := auth.Clone()
		failed.Metadata = patch.Apply(latest)
		failed.Disabled, _ = failed.Metadata["disabled"].(bool)
		if !failed.Disabled {
			// A peer's complete credential version won the disable CAS.
			failed.RejectedAccessToken = ""
			if expiry, known := failed.ExpirationTime(); known && expiry.After(clock().Add(time.Minute)) {
				failed.CodexRefreshPatch = codexauth.NewCredentialRefreshPatch(observed.Metadata, latest, latest)
				return failed, nil
			}
			return nil, errRefresh
		}
		if diskPath != "" {
			if errWrite := codexauth.WriteJSONFileAtomic(diskPath, failed.Metadata); errWrite != nil {
				return nil, fmt.Errorf("codex executor: persist disabled credential: %w", errWrite)
			}
		}
		failed.CodexRefreshPatch = patch
		failed.Storage = nil
		failed.Status = cliproxyauth.StatusDisabled
		failed.Unavailable = true
		return failed, errRefresh
	}
	auth.RejectedAccessToken = ""
	patch := codexauth.NewCredentialRefreshPatch(observed.Metadata, diskBaseline, auth.Metadata)
	auth.CodexRefreshPatch = patch
	if storage, ok := auth.Storage.(*codexauth.CodexTokenStorage); ok && storage != nil {
		clonedStorage := *storage
		clonedStorage.RefreshPatch = patch
		auth.Storage = &clonedStorage
	}
	minted := true
	if diskPath != "" {
		raw, errRead := os.ReadFile(diskPath)
		if errRead != nil {
			return nil, fmt.Errorf("codex executor: re-read credential before persistence: %w", errRead)
		}
		var latest map[string]any
		if errJSON := json.Unmarshal(raw, &latest); errJSON != nil {
			return nil, fmt.Errorf("codex executor: invalid credential JSON before persistence: %w", errJSON)
		}
		if latest == nil {
			return nil, fmt.Errorf("codex executor: credential disappeared before persistence")
		}
		minted = patch.CanApplyTo(latest)
		if minted {
			latest = patch.Apply(latest)
			if errWrite := codexauth.WriteJSONFileAtomic(diskPath, latest); errWrite != nil {
				return nil, fmt.Errorf("codex executor: persist refreshed credential: %w", errWrite)
			}
		}
		auth.Metadata = latest
	}
	expiry, known := auth.ExpirationTime()
	minimum := clock()
	if !minted {
		minimum = minimum.Add(cliproxyauth.CodexRefreshLead)
	}
	if !known || !expiry.After(minimum) {
		return nil, fmt.Errorf("codex executor: refresh did not produce a usable credential")
	}
	if minted && !homeEnabled && auth.ID != "" {
		access, _ := auth.Metadata["access_token"].(string)
		e.refresh.mu.Lock()
		if e.refresh.mints == nil {
			e.refresh.mints = make(map[string]codexMint)
		}
		for id, mint := range e.refresh.mints {
			if minimum.Sub(mint.at) >= 5*time.Minute {
				delete(e.refresh.mints, id)
			}
		}
		e.refresh.mints[auth.ID] = codexMint{access: access, at: minimum}
		e.refresh.mu.Unlock()
	}
	return auth, nil
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		baseURL = a.Attributes["base_url"]
	}
	// NeoPi authenticates every Codex subscription request with the outer ChatGPT grant.
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if e == nil {
		return nil
	}
	return resolveCodexKeyConfig(e.cfg, auth)
}

func resolveCodexKeyConfig(cfg *config.Config, auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
		if index, errIndex := strconv.Atoi(strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeConfigIndex])); errIndex == nil && index >= 0 && index < len(cfg.CodexKey) {
			entry := &cfg.CodexKey[index]
			cfgKey := strings.TrimSpace(entry.APIKey)
			cfgBase := strings.TrimSpace(entry.BaseURL)
			if (attrKey == "" || strings.EqualFold(cfgKey, attrKey)) && (attrBase == "" || strings.EqualFold(cfgBase, attrBase)) {
				return entry
			}
		}
	}
	for i := range cfg.CodexKey {
		entry := &cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range cfg.CodexKey {
			entry := &cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func (e *CodexExecutor) resolveCodexModelIsCompat(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, baseModel string) bool {
	if modelInfo, ok := cliproxyauth.ResolvedModelInfo(req); ok && modelInfo != nil {
		return modelInfo.IsCompat
	}
	entry := e.resolveCodexConfig(auth)
	if entry != nil && len(entry.Models) > 0 {
		requested := strings.TrimSpace(req.Model)
		target := strings.TrimSpace(baseModel)
		for i := range entry.Models {
			name := strings.TrimSpace(entry.Models[i].Name)
			alias := strings.TrimSpace(entry.Models[i].Alias)
			if (target != "" && (strings.EqualFold(name, target) || strings.EqualFold(alias, target))) ||
				(requested != "" && (strings.EqualFold(name, requested) || strings.EqualFold(alias, requested))) {
				return entry.Models[i].IsCompat
			}
		}
		return false
	}
	if cliproxyauth.CodexAPIKeyModelIsCompat(e.cfg, auth, baseModel) || cliproxyauth.CodexAPIKeyModelIsCompat(e.cfg, auth, req.Model) {
		return true
	}
	return false
}
