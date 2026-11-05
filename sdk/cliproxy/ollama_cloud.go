package cliproxy

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ollamaCloudDiscovery is NeoPi's Ollama Cloud model manager: the bundled
// rows, with the account's /api/tags listing merged in (2h cache, 5m retry
// after a failure; NeoPi's discovery carries no deadline here either).
var ollamaCloudDiscovery = &neoPiDiscovery{provider: registry.OllamaCloudProvider, ttl: 2 * time.Hour, retry: 5 * time.Minute,
	bundled: neoPiStaticSeed(registry.OllamaCloudModels), fetch: executor.FetchOllamaCloudModels,
	match: func(a *coreauth.Auth) bool {
		_, ok := ollamaCloudIdentity(a)
		return ok
	},
	entries: map[string]*neoPiDiscoveryEntry{}}

func init() {
	neoPiDiscoveries = append(neoPiDiscoveries, ollamaCloudDiscovery)
}

// ollamaCloudIdentity resolves an Ollama Cloud account's runtime provider
// key (the key its models register under and its executor is bound to): the
// dashboard adds the key as an OpenAI-compatibility entry named after the
// catalog provider, suffixed for a second endpoint.
func ollamaCloudIdentity(a *coreauth.Auth) (runtimeKey string, ok bool) {
	if a == nil {
		return "", false
	}
	runtimeKey = strings.ToLower(strings.TrimSpace(a.Provider))
	catalog := strings.TrimPrefix(runtimeKey, "openai-compatible-")
	if compatKey, compatName, compat := openAICompatInfoFromAuth(a); compat {
		runtimeKey = compatKey
		catalog = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(compatName)), "openai-compatible-")
		if catalog == "" {
			catalog = strings.TrimPrefix(compatKey, "openai-compatible-")
		}
	}
	if index := strings.LastIndexByte(catalog, '-'); index > 0 {
		if suffix, err := strconv.Atoi(catalog[index+1:]); err == nil && suffix >= 2 && catalog[:index] == registry.OllamaCloudProvider {
			catalog = catalog[:index]
		}
	}
	return runtimeKey, catalog == registry.OllamaCloudProvider
}

// registerOllamaCloudModels publishes an Ollama Cloud account's roster under
// its runtime provider key.
func (s *Service) registerOllamaCloudModels(ctx context.Context, cfg *config.Config, a *coreauth.Auth, fence modelRegistrationFence, provider, authKind, runtimeKey string, excluded []string) bool {
	models := applyExcludedModels(s.registerNeoPiDiscovered(ollamaCloudDiscovery, a), excluded)
	if ctx.Err() != nil {
		return true
	}
	models = applyOAuthModelAliasForAuth(cfg, provider, authKind, a.Attributes, models)
	if ctx.Err() != nil {
		return true
	}
	models = s.appendPluginModels(runtimeKey, models, excluded)
	if len(models) == 0 {
		return s.unregisterModelsForAuthFenced(a, fence)
	}
	models = applyOAuthSettingsForAuth(cfg, provider, authKind, models)
	prefix, forcePrefix := a.Prefix, cfg != nil && cfg.ForceModelPrefix
	return s.registerResolvedModelsForAuthFenced(a, fence, runtimeKey, applyModelPrefixes(models, prefix, forcePrefix))
}

// bindOllamaCloudExecutor binds the native Ollama chat executor to an Ollama
// Cloud account's runtime key, reporting whether the account is one.
func (s *Service) bindOllamaCloudExecutor(cfg *config.Config, a *coreauth.Auth, forceReplace bool) bool {
	providerKey, ok := ollamaCloudIdentity(a)
	if !ok {
		return false
	}
	if !forceReplace {
		if existing, found := s.coreManager.Executor(providerKey); found {
			if current, isOllama := existing.(*executor.OllamaCloudExecutor); isOllama && current.UsesConfig(cfg) {
				return true
			}
		}
	}
	s.coreManager.RegisterExecutor(executor.NewOllamaCloudExecutor(providerKey, cfg))
	return true
}
