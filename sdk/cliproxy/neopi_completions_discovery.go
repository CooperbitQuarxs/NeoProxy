package cliproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
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
	"yolo-auto", "zhipu-coding-plan", "coreweave", "cline-pass", "aimlapi", "xiaomi",
	"singularityapi-dev", "singularityapi-tech", "charm-hyper", "alibaba-coding-plan", "kilo",
	"siliconflow", "siliconflow-cn", "vllm", "lm-studio", "zenmux",
	"minimax", "minimax-cn", "minimax-code", "minimax-code-cn", "snowflake", "cloudflare-ai-gateway",
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
			bundled: func(*coreauth.Auth) []*ModelInfo { return registry.NeoPiCompletionsBundledModels(provider, provider) },
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
	models := neoPiConfiguredCompletionsModels(cfg, a, catalog, s.registerNeoPiDiscovered(neoPiCompletionsDiscoveries[catalog], a))
	models = applyExcludedModels(models, excluded)
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

// neoPiConfiguredCompletionsModels keeps an openai-compatibility entry's
// configured models on its NeoPi roster: each configured upstream name is
// served with NeoPi's record for it (the discovered row, else NeoPi's row for
// an unknown id), so its wire policy is NeoPi's, and is listed under its
// configured alias and display name. An entry without configured models
// serves the roster unchanged.
func neoPiConfiguredCompletionsModels(cfg *config.Config, a *coreauth.Auth, catalog string, roster []*ModelInfo) []*ModelInfo {
	if cfg == nil {
		return roster
	}
	_, compatName, ok := openAICompatInfoFromAuth(a)
	if !ok {
		return roster
	}
	entry := configEntryForAuthIndex(a, cfg.OpenAICompatibility)
	if entry == nil || !strings.EqualFold(strings.TrimSpace(entry.Name), compatName) {
		entry = nil
		for i := range cfg.OpenAICompatibility {
			if strings.EqualFold(strings.TrimSpace(cfg.OpenAICompatibility[i].Name), compatName) {
				entry = &cfg.OpenAICompatibility[i]
				break
			}
		}
	}
	if entry == nil || len(entry.Models) == 0 {
		return roster
	}
	rows := make(map[string]*registry.NeoPiModel, len(roster))
	for _, model := range roster {
		if model != nil && model.NeoPi != nil {
			rows[model.NeoPi.ID] = model.NeoPi
		}
	}
	out := slices.Clone(roster)
	index := make(map[string]int, len(out))
	for i, model := range out {
		index[model.ID] = i
	}
	for _, configured := range entry.Models {
		name := strings.TrimSpace(configured.Name)
		if name == "" || configured.Image {
			continue
		}
		row := rows[name]
		if row == nil {
			row = registry.NeoPiCompletionsModel(catalog, name)
		}
		if row == nil {
			continue
		}
		info := registry.NeoPiCompletionsModelInfo(row, entry.Name)
		if alias := strings.TrimSpace(configured.Alias); alias != "" {
			info.ID = alias
		}
		if display := strings.TrimSpace(configured.DisplayName); display != "" {
			info.DisplayName = display
		}
		if at, exists := index[info.ID]; exists {
			out[at] = info
			continue
		}
		index[info.ID] = len(out)
		out = append(out, info)
	}
	return out
}

// alibabaTokenPlanToken is the sk-... token pattern of a Token Plan key.
var alibabaTokenPlanToken = regexp.MustCompile(`^sk-[A-Za-z0-9._~+/-]+={0,2}$`)

// neoPiCompletionsDiscoveryAccount reads the key and discovery base URL a
// NeoPi manager is configured with for the account: its key, and a base URL
// that defaults to the provider's own. A Token Plan key is either a bare
// sk-... token or a {"token","cookie","baseUrl"} bundle whose region decides
// the endpoint (parseAlibabaTokenPlanCredential). Providers whose manager
// lists without a key (Charm Hyper) may have none.
func neoPiCompletionsDiscoveryAccount(provider string, auth *coreauth.Auth) (key, base string, err error) {
	if auth != nil {
		key = strings.TrimSpace(auth.Attributes["api_key"])
		if key == "" {
			key, _ = auth.Metadata["api_key"].(string)
			key = strings.TrimSpace(key)
		}
		base = strings.TrimSpace(auth.Attributes["base_url"])
	}
	if key == "" && !registry.NeoPiCompletionsDiscoversWithoutKey(provider) {
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
	return key, registry.NeoPiV1BaseURL(provider, base), nil
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

// ClinePass discovery reads fixed hosts, not the account's base URL; tests
// point them at a loopback server.
var (
	neoPiClinePassModelsURL      = registry.NeoPiClinePassModelsURL
	neoPiClinePassLiveCatalogURL = registry.NeoPiClinePassLiveCatalogURL
)

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
		switch provider {
		case "fireworks":
			return fetchNeoPiFireworksModels(ctx, cfg, auth, key, base)
		case "cline-pass":
			return fetchNeoPiClinePassModels(ctx, cfg, auth, base)
		case "siliconflow", "siliconflow-cn":
			return fetchNeoPiSiliconFlowModels(ctx, cfg, auth, provider, key, base)
		case "lm-studio":
			return fetchNeoPiLmStudioModels(ctx, cfg, auth, key, base)
		case "cloudflare-ai-gateway":
			return fetchNeoPiCloudflareModels(ctx, cfg, auth, key, base)
		case "xiaomi":
			if neoPiIsXiaomiTokenPlanKey(key) {
				return fetchNeoPiXiaomiTokenPlanModels(ctx, cfg, auth, key)
			}
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

// fetchNeoPiCloudflareModels is cloudflareAiGatewayModelManagerOptions'
// listing: GET <base>/v1/models with Anthropic's discovery headers and the
// stored credential as the key, on the account's configured endpoint, else on
// NeoPi's Anthropic gateway base. That base keeps its <account>/<gateway>
// placeholders, where NeoPi's listing never reaches an account and it keeps
// the bundled roster; that roster is served without sending the credential
// there.
func fetchNeoPiCloudflareModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, key, configured string) ([]*ModelInfo, error) {
	const provider = "cloudflare-ai-gateway"
	base := registry.NeoPiCloudflareListingBase(configured)
	if registry.NeoPiCloudflarePlaceholders(base) {
		return registry.NeoPiCompletionsBundledModels(provider, provider), nil
	}
	endpoint := base
	if !strings.HasSuffix(endpoint, "/v1") {
		endpoint += "/v1"
	}
	endpoint += "/models"
	header := http.Header{}
	anthropicnpi.SetDiscoveryHeaders(header, key)
	// The stored credential rides in X-Api-Key, which net/http copies onto a
	// redirect to any origin; that hop must not carry it.
	body, err := neoPiDiscoveryFetch(ctx, cfg, auth, provider, endpoint, header, "X-Api-Key", "Authorization", "cf-aig-authorization")
	if err != nil {
		return nil, err
	}
	models, err := registry.NeoPiCompletionsListing(provider, base, body)
	if err != nil {
		return nil, fmt.Errorf("%s models: %s: %w", provider, endpoint, err)
	}
	return models, nil
}

// neoPiIsXiaomiTokenPlanKey is isXiaomiTokenPlanKey.
func neoPiIsXiaomiTokenPlanKey(key string) bool {
	return strings.HasPrefix(key, "tp-") || strings.HasPrefix(key, "ttp-")
}

// fetchNeoPiXiaomiTokenPlanModels is xiaomiModelManagerOptions for a Token
// Plan key on the standard provider: the regional clusters are listed in
// NeoPi's SGP, AMS, CN order and the first that answers with a usable listing
// serves the roster from its own host. The account's base URL is ignored, as
// NeoPi pins Token Plan keys to the clusters.
func fetchNeoPiXiaomiTokenPlanModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, key string) ([]*ModelInfo, error) {
	var lastErr error
	for _, region := range []string{"xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "xiaomi-token-plan-cn"} {
		base := strings.TrimSuffix(registry.NeoPiCompletionsListingBase(region), "/")
		body, err := neoPiDiscoveryGet(ctx, cfg, auth, "xiaomi", base+"/models", key)
		if err == nil {
			var models []*ModelInfo
			models, err = registry.NeoPiCompletionsListing("xiaomi", base, body)
			if empty := (*registry.NeoPiEmptyListing)(nil); err == nil || errors.As(err, &empty) {
				return models, err
			}
		}
		lastErr = fmt.Errorf("xiaomi models: %s: %w", base, err)
	}
	return nil, lastErr
}

// fetchNeoPiClinePassModels is fetchClinePassModels: Cline's recommended-models
// roster, sent with the Cline client identity and no key, beside OpenRouter's
// public catalog, which only enriches ids the bundle does not know and whose
// failure leaves the roster unenriched. NeoPi bounds both fetches at five
// seconds; the repository bans deadlines after the connection is up, so a
// failed roster cancels the catalog fetch instead of waiting it out.
func fetchNeoPiClinePassModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, base string) ([]*ModelInfo, error) {
	liveCtx, cancelLive := context.WithCancel(ctx)
	defer cancelLive()
	liveDone := make(chan []byte, 1)
	go func() {
		live, err := neoPiDiscoveryFetch(liveCtx, cfg, auth, "cline-pass", neoPiClinePassLiveCatalogURL, http.Header{})
		if err != nil {
			live = nil
		}
		liveDone <- live
	}()
	header := http.Header{}
	for name, value := range openaiwire.ClinePassClientHeaders("") {
		header.Set(name, value)
	}
	roster, err := neoPiDiscoveryFetch(ctx, cfg, auth, "cline-pass", neoPiClinePassModelsURL, header)
	if err != nil {
		return nil, err
	}
	live := <-liveDone
	models, err := registry.NeoPiClinePassListing(base, roster, live)
	if err != nil {
		return nil, fmt.Errorf("cline-pass models: %s: %w", neoPiClinePassModelsURL, err)
	}
	return models, nil
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
	return registry.NeoPiFireworksListing(base, records)
}

// neoPiDiscoveryGet sends NeoPi's discovery request and returns the body of a
// 2xx answer. Without a key it sends no Authorization, as NeoPi does.
func neoPiDiscoveryGet(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, provider, endpoint, key string) ([]byte, error) {
	header := http.Header{}
	header.Set("Accept", "application/json")
	if key != "" {
		header.Set("Authorization", "Bearer "+key)
	}
	if provider == "coreweave" {
		// coreWeaveModelManagerOptions sends the W&B project the environment names.
		if project := openaiwire.ResolveCoreWeaveProject(os.Getenv); project != "" {
			header.Set(openaiwire.CoreWeaveProjectHeader, project)
		}
	}
	return neoPiDiscoveryFetch(ctx, cfg, auth, provider, endpoint, header)
}

// neoPiDiscoveryFetch GETs a discovery endpoint with header, under the
// runtime's User-Agent unless header names one. The named credential headers
// are dropped from any redirect hop that leaves the endpoint's origin.
func neoPiDiscoveryFetch(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, provider, endpoint string, header http.Header, credentialHeaders ...string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header = header
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", neoPiBunUserAgent)
	}
	resp, err := helps.GuardCredentialRedirects(helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0), credentialHeaders...).Do(req)
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
