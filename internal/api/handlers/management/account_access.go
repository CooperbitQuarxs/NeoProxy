package management

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var compatNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// CreateAccountAPIKey appends a credential to the EXISTING engine configuration,
// using its synthesizer, stable identities and reload path. Native providers use
// their fixed upstream; openai-compatibility entries carry the caller's base URL
// and model list, so the caller (the dashboard access API) is responsible for
// restricting who may choose an upstream. It never reveals existing credentials.
func (h *Handler) CreateAccountAPIKey(ctx context.Context, k coreauth.AccountKey) (*coreauth.Auth, error) {
	key := strings.TrimSpace(k.APIKey)
	keyless := key == "" && k.Provider == "openai-compatibility"
	if (key == "" && !keyless) || len(key) > 16384 || coreauth.AccountLoginOwner(inheritAccountLoginOwner(ctx, ctx)) == "" {
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
		if key != "" && a.Attributes["api_key"] == key {
			h.mu.Unlock()
			return nil, coreauth.ErrAccountKeyConflict
		}
	}
	match := func(a *coreauth.Auth) bool { return a.Provider == k.Provider && a.Attributes["api_key"] == key }
	switch k.Provider {
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
	case "openai-compatibility":
		name := strings.TrimSpace(k.CompatName)
		base := strings.TrimRight(strings.TrimSpace(k.BaseURL), "/")
		parsed, errURL := url.Parse(base)
		if !compatNamePattern.MatchString(name) || errURL != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			h.mu.Unlock()
			return nil, coreauth.ErrAccountKeyInvalid
		}
		if err = appendCompatKey(next, name, base, key, k.Models); err != nil {
			h.mu.Unlock()
			return nil, err
		}
		match = func(a *coreauth.Auth) bool {
			return strings.EqualFold(a.Attributes["compat_name"], name) && a.Attributes["api_key"] == key
		}
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
		if match(a) {
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

// appendCompatKey adds key to the openai-compatibility entry named name, creating
// it when absent. An existing entry with a different base URL is a conflict: one
// name maps to exactly one upstream. Models are merged by upstream name.
func appendCompatKey(cfg *config.Config, name, base, key string, models []string) error {
	for i := range cfg.OpenAICompatibility {
		entry := &cfg.OpenAICompatibility[i]
		if !strings.EqualFold(strings.TrimSpace(entry.Name), name) {
			continue
		}
		if strings.TrimRight(strings.TrimSpace(entry.BaseURL), "/") != base || key == "" || len(entry.APIKeyEntries) == 0 {
			return fmt.Errorf("%w: openai-compatibility entry %q already exists", coreauth.ErrAccountKeyConflict, name)
		}
		entry.APIKeyEntries = append(entry.APIKeyEntries, config.OpenAICompatibilityAPIKey{APIKey: key})
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" || slices.ContainsFunc(entry.Models, func(m config.OpenAICompatibilityModel) bool { return m.Name == model }) {
				continue
			}
			entry.Models = append(entry.Models, config.OpenAICompatibilityModel{Name: model})
		}
		return nil
	}
	entry := config.OpenAICompatibility{Name: name, BaseURL: base, Models: []config.OpenAICompatibilityModel{}}
	if key != "" {
		entry.APIKeyEntries = []config.OpenAICompatibilityAPIKey{{APIKey: key}}
	}
	for _, model := range models {
		if model = strings.TrimSpace(model); model != "" {
			entry.Models = append(entry.Models, config.OpenAICompatibilityModel{Name: model})
		}
	}
	cfg.OpenAICompatibility = append(cfg.OpenAICompatibility, entry)
	return nil
}

// configCredential locates the configuration entry that synthesized authID in
// cfg. keyIndex is only meaningful for openai-compatibility entries.
type configCredential struct {
	section  string
	index    int
	keyIndex int
}

func locateConfigCredential(cfg *config.Config, authID string) (configCredential, bool) {
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		return configCredential{}, false
	}
	for _, a := range auths {
		if a.ID != authID {
			continue
		}
		index, errIndex := strconv.Atoi(a.Attributes["config_index"])
		if errIndex != nil || index < 0 {
			return configCredential{}, false
		}
		source := a.Attributes["source"]
		if compat := strings.TrimSpace(a.Attributes["compat_name"]); compat != "" {
			if index >= len(cfg.OpenAICompatibility) {
				return configCredential{}, false
			}
			if len(cfg.OpenAICompatibility[index].APIKeyEntries) == 0 {
				return configCredential{section: "openai-compatibility", index: index, keyIndex: -1}, true
			}
			key := strings.TrimSpace(a.Attributes["api_key"])
			for j, entry := range cfg.OpenAICompatibility[index].APIKeyEntries {
				if strings.TrimSpace(entry.APIKey) == key && strings.TrimSpace(entry.ProxyURL) == strings.TrimSpace(a.ProxyURL) {
					return configCredential{section: "openai-compatibility", index: index, keyIndex: j}, true
				}
			}
			return configCredential{}, false
		}
		for _, section := range []string{"gemini", "interactions", "claude", "codex", "xai", "meta", "vertex-apikey"} {
			if strings.HasPrefix(source, "config:"+section+"[") {
				return configCredential{section: section, index: index}, true
			}
		}
		return configCredential{}, false
	}
	return configCredential{}, false
}

// removeConfigCredential deletes the located entry; an openai-compatibility
// entry left without keys is removed with its model list.
func removeConfigCredential(cfg *config.Config, at configCredential) bool {
	remove := func(n int) bool { return at.index < n }
	switch at.section {
	case "gemini":
		if remove(len(cfg.GeminiKey)) {
			cfg.GeminiKey = slices.Delete(cfg.GeminiKey, at.index, at.index+1)
			return true
		}
	case "interactions":
		if remove(len(cfg.InteractionsKey)) {
			cfg.InteractionsKey = slices.Delete(cfg.InteractionsKey, at.index, at.index+1)
			return true
		}
	case "claude":
		if remove(len(cfg.ClaudeKey)) {
			cfg.ClaudeKey = slices.Delete(cfg.ClaudeKey, at.index, at.index+1)
			return true
		}
	case "codex":
		if remove(len(cfg.CodexKey)) {
			cfg.CodexKey = slices.Delete(cfg.CodexKey, at.index, at.index+1)
			return true
		}
	case "xai":
		if remove(len(cfg.XAIKey)) {
			cfg.XAIKey = slices.Delete(cfg.XAIKey, at.index, at.index+1)
			return true
		}
	case "meta":
		if remove(len(cfg.MetaKey)) {
			cfg.MetaKey = slices.Delete(cfg.MetaKey, at.index, at.index+1)
			return true
		}
	case "vertex-apikey":
		if remove(len(cfg.VertexCompatAPIKey)) {
			cfg.VertexCompatAPIKey = slices.Delete(cfg.VertexCompatAPIKey, at.index, at.index+1)
			return true
		}
	case "openai-compatibility":
		if !remove(len(cfg.OpenAICompatibility)) || at.keyIndex >= len(cfg.OpenAICompatibility[at.index].APIKeyEntries) {
			return false
		}
		entry := &cfg.OpenAICompatibility[at.index]
		if at.keyIndex >= 0 {
			entry.APIKeyEntries = slices.Delete(entry.APIKeyEntries, at.keyIndex, at.keyIndex+1)
		}
		if len(entry.APIKeyEntries) == 0 {
			cfg.OpenAICompatibility = slices.Delete(cfg.OpenAICompatibility, at.index, at.index+1)
		}
		return true
	}
	return false
}

// commitAccountConfig persists next as the engine configuration and reloads it.
// Callers must hold h.mu; it is released before the reload runs.
func (h *Handler) commitAccountConfigLocked(ctx context.Context, next *config.Config) error {
	if err := config.SaveConfigPreserveComments(h.configFilePath, next, true); err != nil {
		h.mu.Unlock()
		return fmt.Errorf("save account configuration: %w", err)
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.reloadConfigAfterManagementSave(context.WithoutCancel(ctx), snapshot)
	return nil
}

// SetAccountDisabled pauses or resumes one engine credential without removing
// it: config keys through their existing exclusion/disabled config fields, file
// credentials through the auth manager's persisted disabled state.
func (h *Handler) SetAccountDisabled(ctx context.Context, authID string, disabled bool) error {
	if h == nil || h.authManager == nil {
		return fmt.Errorf("core auth manager unavailable")
	}
	target, ok := h.authManager.GetByID(authID)
	if !ok || target == nil {
		return coreauth.ErrAccountNotFound
	}
	if strings.EqualFold(target.Attributes[coreauth.AttributeRuntimeOnly], "true") || coreauth.IsPluginVirtualAuth(target) {
		return coreauth.ErrAccountUnsupported
	}
	if target.AuthSourceKind() == coreauth.AuthSourceConfig {
		h.mu.Lock()
		if h.cfg == nil || h.configFilePath == "" {
			h.mu.Unlock()
			return fmt.Errorf("engine configuration is unavailable")
		}
		next := h.cfg.CloneForRuntime()
		if strings.TrimSpace(target.Attributes["compat_name"]) != "" {
			at, found := locateConfigCredential(next, authID)
			if !found || at.keyIndex < 0 {
				h.mu.Unlock()
				if found {
					return coreauth.ErrAccountUnsupported
				}
				return coreauth.ErrAccountNotFound
			}
			next.OpenAICompatibility[at.index].APIKeyEntries[at.keyIndex].Disabled = disabled
		} else {
			handled, err := toggleConfigAPIKeyExcludedAll(next, target, disabled)
			if err != nil || !handled {
				h.mu.Unlock()
				if err == nil {
					err = coreauth.ErrAccountNotFound
				}
				return err
			}
		}
		return h.commitAccountConfigLocked(ctx, next)
	}
	h.authStatusMu.Lock()
	updated, committed, err := h.authManager.Modify(ctx, target.ID, target.RegistrationEpoch, func(current *coreauth.Auth) error {
		applyAuthDisabledState(current, disabled)
		return nil
	})
	h.authStatusMu.Unlock()
	if err != nil {
		return err
	}
	if !committed || updated == nil {
		return coreauth.ErrAccountNotFound
	}
	return h.invokePostAuthPersistHooks(ctx, []*coreauth.Auth{updated})
}

// DeleteAccountCredential removes one engine credential: its config entry for
// API keys, or its auth file. Runtime-only credentials (NeoPi gateway accounts)
// are not engine-owned and return ErrAccountUnsupported.
func (h *Handler) DeleteAccountCredential(ctx context.Context, authID string) error {
	if h == nil || h.authManager == nil {
		return fmt.Errorf("core auth manager unavailable")
	}
	target, ok := h.authManager.GetByID(authID)
	if !ok || target == nil {
		return coreauth.ErrAccountNotFound
	}
	if strings.EqualFold(target.Attributes[coreauth.AttributeRuntimeOnly], "true") || target.Attributes["neopi_source_id"] != "" {
		return coreauth.ErrAccountUnsupported
	}
	if target.AuthSourceKind() == coreauth.AuthSourceConfig {
		h.mu.Lock()
		if h.cfg == nil || h.configFilePath == "" {
			h.mu.Unlock()
			return fmt.Errorf("engine configuration is unavailable")
		}
		next := h.cfg.CloneForRuntime()
		at, found := locateConfigCredential(next, authID)
		if !found || !removeConfigCredential(next, at) {
			h.mu.Unlock()
			return coreauth.ErrAccountNotFound
		}
		return h.commitAccountConfigLocked(ctx, next)
	}
	name := strings.TrimSpace(target.FileName)
	if name == "" {
		name = filepath.Base(strings.TrimSpace(authAttribute(target, "path")))
	}
	if name == "" || name == "." {
		return coreauth.ErrAccountUnsupported
	}
	if _, _, err := h.deleteAuthFileByName(ctx, name); err != nil {
		if errors.Is(err, errAuthFileNotFound) {
			return coreauth.ErrAccountNotFound
		}
		return err
	}
	return nil
}
