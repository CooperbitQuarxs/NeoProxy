package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// neoPiCompletionsProviders are the providers NeoPi serves through its
// OpenAI Chat Completions provider, each with its own live model discovery.
var neoPiCompletionsProviders = []string{
	"cerebras", "deepseek", "fireworks", "groq", "huggingface", "mistral",
	"moonshot", "nanogpt", "nvidia", "together", "alibaba-token-plan",
	"aiand", "baseten", "coralbricks", "deepinfra", "firepass", "gmi-cloud", "helmcode", "inception",
	"novita", "qianfan", "qwen-portal", "sambanova", "stepfun", "synthetic", "venice",
	"wafer-serverless", "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn",
	"yolo-auto", "zhipu-coding-plan",
}

// neoPiCompletionsDiscoveries holds one discovery per provider, keyed by
// NeoPi's provider id.
var neoPiCompletionsDiscoveries = newNeoPiCompletionsDiscoveries()

func init() {
	for _, provider := range neoPiCompletionsProviders {
		neoPiDiscoveries = append(neoPiDiscoveries, neoPiCompletionsDiscoveries[provider])
	}
}

func newNeoPiCompletionsDiscoveries() map[string]*neoPiDiscovery {
	out := make(map[string]*neoPiDiscovery, len(neoPiCompletionsProviders))
	for _, provider := range neoPiCompletionsProviders {
		// NeoPi's model manager default 2h cache TTL and 5m retry after a
		// failure. NeoPi bounds openai-compatible discovery at 10s; the
		// repository bans deadlines after the connection is up, so the
		// fetch runs without one.
		out[provider] = &neoPiDiscovery{
			provider: provider,
			ttl:      2 * time.Hour, retry: 5 * time.Minute,
			bundled: func() []*ModelInfo { return registry.NeoPiCompletionsBundledModels(provider, provider) },
			fetch:   fetchNeoPiCompletionsModels(provider),
			match:   func(a *coreauth.Auth) bool { return neoPiCompletionsCatalogProvider(a) == provider },
			entries: map[string]*neoPiDiscoveryEntry{},
		}
	}
	return out
}

// neoPiCompletionsIdentity resolves the account's runtime provider key (the
// key its models register under and its executor is bound to) and NeoPi's
// catalog provider, which differ for an OpenAI-compatibility account:
// "openai-compatible-cerebras" runs NeoPi's "cerebras".
func neoPiCompletionsIdentity(a *coreauth.Auth) (runtimeKey, catalog string, ok bool) {
	if a == nil {
		return "", "", false
	}
	runtimeKey = strings.ToLower(strings.TrimSpace(a.Provider))
	catalog = strings.TrimPrefix(runtimeKey, "openai-compatible-")
	if compatKey, compatName, compat := openAICompatInfoFromAuth(a); compat {
		runtimeKey = compatKey
		catalog = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(compatName)), "openai-compatible-")
		if catalog == "" {
			catalog = strings.TrimPrefix(compatKey, "openai-compatible-")
		}
	}
	// The access catalog suffixes a provider name when a second account uses
	// a different endpoint, such as Token Plan's China region.
	if index := strings.LastIndexByte(catalog, '-'); index > 0 {
		if suffix, err := strconv.Atoi(catalog[index+1:]); err == nil && suffix >= 2 && slices.Contains(neoPiCompletionsProviders, catalog[:index]) {
			catalog = catalog[:index]
		}
	}
	if !slices.Contains(neoPiCompletionsProviders, catalog) {
		return "", "", false
	}
	return runtimeKey, catalog, true
}

// neoPiCompletionsCatalogProvider is the catalog provider of a NeoPi
// completions account, or "".
func neoPiCompletionsCatalogProvider(a *coreauth.Auth) string {
	_, catalog, ok := neoPiCompletionsIdentity(a)
	if !ok {
		return ""
	}
	return catalog
}

// registerNeoPiCompletionsModels publishes a NeoPi completions account's
// roster under its runtime provider key.
func (s *Service) registerNeoPiCompletionsModels(ctx context.Context, cfg *config.Config, a *coreauth.Auth, fence modelRegistrationFence, provider, authKind, runtimeKey, catalog string, excluded []string) bool {
	models := applyExcludedModels(s.registerNeoPiDiscovered(neoPiCompletionsDiscoveries[catalog], a), excluded)
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

// alibabaTokenPlanToken is the sk-... token pattern of a Token Plan key.
var alibabaTokenPlanToken = regexp.MustCompile(`^sk-[A-Za-z0-9._~+/-]+={0,2}$`)

// neoPiCompletionsDiscoveryAccount reads the key and discovery base URL a
// NeoPi manager is configured with for the account: its key, and a base URL
// that defaults to the provider's own. A Token Plan key is either a bare
// sk-... token or a {"token","cookie","baseUrl"} bundle whose region decides
// the endpoint (parseAlibabaTokenPlanCredential).
func neoPiCompletionsDiscoveryAccount(provider string, auth *coreauth.Auth) (key, base string, err error) {
	if auth != nil {
		key = strings.TrimSpace(auth.Attributes["api_key"])
		if key == "" {
			key, _ = auth.Metadata["api_key"].(string)
			key = strings.TrimSpace(key)
		}
		base = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if key == "" {
		return "", "", fmt.Errorf("%s models: account has no api key", provider)
	}
	if provider == "alibaba-token-plan" {
		var bundleBase string
		key, bundleBase, err = parseAlibabaTokenPlanKey(key)
		if err != nil {
			return "", "", fmt.Errorf("%s models: %w", provider, err)
		}
		if bundleBase != "" {
			base = bundleBase
		}
	}
	if base == "" {
		base = registry.NeoPiCompletionsListingBase(provider)
	}
	return key, strings.TrimSuffix(base, "/"), nil
}

// parseAlibabaTokenPlanKey is parseAlibabaTokenPlanCredential: the token and
// the region base URL of the credential.
func parseAlibabaTokenPlanKey(value string) (token, base string, err error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "{") {
		if !alibabaTokenPlanToken.MatchString(value) {
			return "", "", errors.New("not a Token Plan key")
		}
		return value, "", nil
	}
	var bundle struct {
		Token   *string `json:"token"`
		Cookie  any     `json:"cookie"`
		BaseURL any     `json:"baseUrl"`
	}
	if json.Unmarshal([]byte(value), &bundle) != nil || bundle.Token == nil {
		return "", "", errors.New("not a Token Plan credential")
	}
	if _, isString := bundle.Cookie.(string); bundle.Cookie != nil && !isString {
		return "", "", errors.New("not a Token Plan credential")
	}
	baseURL, isString := bundle.BaseURL.(string)
	if bundle.BaseURL != nil && !isString {
		return "", "", errors.New("not a Token Plan credential")
	}
	token = strings.TrimSpace(*bundle.Token)
	if !alibabaTokenPlanToken.MatchString(token) {
		return "", "", errors.New("not a Token Plan key")
	}
	return token, strings.TrimRight(strings.TrimSpace(baseURL), "/"), nil
}

// neoPiBunUserAgent is the User-Agent NeoPi's runtime (Bun) sends on its
// discovery fetches.
const neoPiBunUserAgent = "Bun/1.3.14"

// neoPiDiscoveryBodyLimit bounds a listing; NanoGPT's detailed roster is the
// largest by far.
const neoPiDiscoveryBodyLimit = 64 << 20

// neoPiFireworksMaxPages is FIREWORKS_CONTROL_PLANE_MAX_PAGES.
const neoPiFireworksMaxPages = 25

// fetchNeoPiCompletionsModels is NeoPi's fetchDynamicModels for provider:
// GET <base>/models with an Accept header and the key as a bearer token, or
// Fireworks' paginated control-plane catalog. A provider whose NeoPi model
// manager has no listing (Firepass) serves its bundled roster unfetched.
func fetchNeoPiCompletionsModels(provider string) func(context.Context, *config.Config, *coreauth.Auth) ([]*ModelInfo, error) {
	return func(ctx context.Context, cfg *config.Config, auth *coreauth.Auth) ([]*ModelInfo, error) {
		if !registry.NeoPiCompletionsDiscovers(provider) {
			return registry.NeoPiCompletionsBundledModels(provider, provider), nil
		}
		key, base, err := neoPiCompletionsDiscoveryAccount(provider, auth)
		if err != nil {
			return nil, err
		}
		if provider == "fireworks" {
			return fetchNeoPiFireworksModels(ctx, cfg, auth, key, base)
		}
		endpoint := base + "/models"
		if query := registry.NeoPiCompletionsListingQuery(provider); len(query) > 0 {
			endpoint += "?" + query.Encode()
		}
		body, err := neoPiDiscoveryGet(ctx, cfg, auth, provider, endpoint, key)
		if err != nil {
			return nil, err
		}
		models, err := registry.NeoPiCompletionsListing(provider, base, body)
		if err != nil {
			return nil, fmt.Errorf("%s models: %s: %w", provider, endpoint, err)
		}
		return models, nil
	}
}

// fetchNeoPiFireworksModels lists Fireworks' serverless catalog through the
// control plane (GET /v1/accounts/fireworks/models on the inference origin),
// following page tokens. Any failed page fails discovery: NeoPi would
// otherwise cache a truncated list as authoritative.
func fetchNeoPiFireworksModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, key, base string) ([]*ModelInfo, error) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("fireworks models: base url %q has no origin", base)
	}
	listURL := parsed.Scheme + "://" + parsed.Host + "/v1/accounts/fireworks/models"
	var records []json.RawMessage
	pageToken := ""
	for page := 0; page < neoPiFireworksMaxPages; page++ {
		query := url.Values{"filter": {"supports_serverless=true"}, "pageSize": {"200"}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		body, err := neoPiDiscoveryGet(ctx, cfg, auth, "fireworks", listURL+"?"+query.Encode(), key)
		if err != nil {
			return nil, err
		}
		pageRecords, next, err := registry.NeoPiFireworksPage(body)
		if err != nil {
			return nil, fmt.Errorf("fireworks models: %s: %w", listURL, err)
		}
		records = append(records, pageRecords...)
		if next == "" {
			break
		}
		pageToken = next
	}
	return registry.NeoPiFireworksListing(base, records), nil
}

// neoPiDiscoveryGet sends NeoPi's discovery request and returns the body of a
// 2xx answer.
func neoPiDiscoveryGet(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, provider, endpoint, key string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", neoPiBunUserAgent)
	resp, err := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s models: %w", provider, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s models: %s returned HTTP %d", provider, endpoint, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, neoPiDiscoveryBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("%s models: read %s: %w", provider, endpoint, err)
	}
	return body, nil
}
