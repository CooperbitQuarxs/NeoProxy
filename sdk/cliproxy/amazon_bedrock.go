package cliproxy

import (
	"context"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// amazonBedrockIdentity resolves an Amazon Bedrock account's runtime provider
// key (the key its models register under and its executor is bound to): the
// dashboard adds the key as an OpenAI-compatibility entry named after the
// catalog provider, suffixed for a second endpoint.
func amazonBedrockIdentity(a *coreauth.Auth) (runtimeKey string, ok bool) {
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
		if suffix, err := strconv.Atoi(catalog[index+1:]); err == nil && suffix >= 2 && catalog[:index] == registry.AmazonBedrockProvider {
			catalog = catalog[:index]
		}
	}
	return runtimeKey, catalog == registry.AmazonBedrockProvider
}

// registerAmazonBedrockModels publishes an Amazon Bedrock account's roster,
// NeoPi's bundled rows (Bedrock has no discovery), under its runtime key.
func (s *Service) registerAmazonBedrockModels(ctx context.Context, cfg *config.Config, a *coreauth.Auth, fence modelRegistrationFence, provider, authKind, runtimeKey string, excluded []string) bool {
	models := applyExcludedModels(registry.AmazonBedrockModels(), excluded)
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

// bindAmazonBedrockExecutor binds the native Converse executor to an Amazon
// Bedrock account's runtime key, reporting whether the account is one.
func (s *Service) bindAmazonBedrockExecutor(cfg *config.Config, a *coreauth.Auth, forceReplace bool) bool {
	providerKey, ok := amazonBedrockIdentity(a)
	if !ok {
		return false
	}
	if !forceReplace {
		if existing, found := s.coreManager.Executor(providerKey); found {
			if current, isBedrock := existing.(*executor.AmazonBedrockExecutor); isBedrock && current.UsesConfig(cfg) {
				return true
			}
		}
	}
	s.coreManager.RegisterExecutor(executor.NewAmazonBedrockExecutor(providerKey, cfg))
	return true
}
