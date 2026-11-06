package registry

import (
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
)

// NeoPi's LiteLLM model manager (provider-models/openai-compat.ts
// litellmModelManagerOptions): no bundled rows; the proxy's management
// endpoints describe its model groups, the first that answers with any
// defining the roster and later ones only adding metadata, and its OpenAI
// /models listing stands in when none answers. Ids are hydrated from the
// models.dev reference map, then NeoPi's global bundled references.

// NeoPiLiteLLMProvider is NeoPi's LiteLLM provider id.
const NeoPiLiteLLMProvider = "litellm"

// neoPiLiteLLMRichEndpoints is LITELLM_RICH_ENDPOINTS, in the order NeoPi
// asks them.
var neoPiLiteLLMRichEndpoints = []string{"/model_group/info", "/v2/model/info", "/model/info", "/v1/model/info"}

const (
	// neoPiLiteLLMMaxRichPages is LITELLM_MAX_RICH_PAGES.
	neoPiLiteLLMMaxRichPages = 100
	// OPENAI_COMPAT_DISCOVERY_DEFAULT_CONTEXT_WINDOW and _MAX_TOKENS.
	neoPiLiteLLMDefaultContext = 128_000
	neoPiLiteLLMDefaultMax     = 32_768
)

// neoPiLiteLLMSentinels is LITELLM_UNUSABLE_SENTINEL_IDS.
var neoPiLiteLLMSentinels = map[string]bool{"all-team-models": true, "all-proxy-models": true, "no-default-models": true}

// neoPiLiteLLMUsageSuffix is LITELLM_RESELLER_USAGE_SUFFIX, with JavaScript's
// \s spelled out.
var neoPiLiteLLMUsageSuffix = regexp.MustCompile(`(?i)[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+\(\d+(?:\.\d+)?[x×] usage\)$`)

// NeoPiLiteLLMGet performs one discovery GET (Accept: application/json, the
// key as a bearer token when there is one): the status and body, or a
// transport error.
type NeoPiLiteLLMGet func(endpoint string) (status int, body []byte, err error)

// NeoPiLiteLLMRuntimeBaseURL is normalizeLiteLLMRuntimeBaseUrl: the base
// trimmed, without one trailing slash.
func NeoPiLiteLLMRuntimeBaseURL(base string) string {
	return strings.TrimSuffix(neoPiJSTrim(base), "/")
}

// NeoPiLiteLLMManagementBaseURL is normalizeLiteLLMManagementBaseUrl: the
// proxy root the management endpoints hang off (a terminal /v1, trailing
// slashes, query and fragment dropped).
func NeoPiLiteLLMManagementBaseURL(base string) string {
	trimmed := strings.TrimRight(neoPiJSTrim(base), "/")
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return strings.TrimSuffix(trimmed, "/v1")
	}
	host := strings.ToLower(parsed.Host)
	if parsed.Scheme == "http" {
		host = strings.TrimSuffix(host, ":80")
	} else {
		host = strings.TrimSuffix(host, ":443")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if strings.HasSuffix(path, "/v1") {
		path = path[:len(path)-3]
	}
	if path == "" {
		path = "/"
	}
	return strings.TrimSuffix(parsed.Scheme+"://"+host+path, "/")
}

// neoPiLiteLLMModels is the hydrated roster the manager's fetchDynamicModels
// returns: the specs, or ok false where NeoPi returns null (no endpoint and
// no listing answered).
func neoPiLiteLLMModels(baseURL string, modelsDev []byte, get NeoPiLiteLLMGet) (specs []neoPiSpec, ok bool) {
	references := neoPiModelsDevGlobalReferences(modelsDev)
	resolve := func(id string) *NeoPiModel {
		if reference := references[id]; reference != nil {
			return reference
		}
		reference, _ := neoPiGlobalReference(id)
		return reference
	}
	runtime := NeoPiLiteLLMRuntimeBaseURL(baseURL)
	specs, ok = neoPiLiteLLMRich(baseURL, get, resolve)
	if !ok {
		if specs, ok = neoPiLiteLLMListing(runtime, get, resolve); !ok {
			return nil, false
		}
	}
	// Bare catalog names can label proxy namespaces; nothing else of theirs
	// carries over.
	for i := range specs {
		var referenceName string
		if reference := resolve(neoPiBareModelID(specs[i].id)); reference != nil {
			referenceName = reference.Name
		}
		specs[i].name = neoPiLiteLLMDisplayName(specs[i].name, referenceName, specs[i].id)
	}
	return specs, true
}

// NeoPiLiteLLMListing resolves a LiteLLM proxy at baseURL into the roster
// NeoPi's model manager produces, fetching through get; modelsDev is the
// models.dev payload (nil when unavailable). When neither the management
// endpoints nor the /models listing answer, the error is ErrNeoPiListing.
func NeoPiLiteLLMListing(baseURL string, modelsDev []byte, get NeoPiLiteLLMGet) ([]*ModelInfo, error) {
	specs, ok := neoPiLiteLLMModels(baseURL, modelsDev, get)
	if !ok {
		return nil, fmt.Errorf("%w: %s: no management endpoint or /models listing answered", ErrNeoPiListing, NeoPiLiteLLMProvider)
	}
	return neoPiMergeListing(NeoPiLiteLLMProvider, NeoPiLiteLLMRuntimeBaseURL(baseURL), specs, false)
}

// neoPiBareModelID is bareModelId: the id after its last slash.
func neoPiBareModelID(id string) string {
	if at := strings.LastIndexByte(id, '/'); at >= 0 {
		return id[at+1:]
	}
	return id
}

// neoPiLiteLLMStripUsage is stripLiteLLMResellerUsageSuffix.
func neoPiLiteLLMStripUsage(name string) string {
	if cleaned := neoPiJSTrim(neoPiLiteLLMUsageSuffix.ReplaceAllString(name, "")); cleaned != "" {
		return cleaned
	}
	return name
}

// neoPiLiteLLMDisplayName is toLiteLLMDisplayName (an empty model name is
// none).
func neoPiLiteLLMDisplayName(modelName, referenceName, id string) string {
	if modelName != "" {
		if cleaned := neoPiLiteLLMStripUsage(modelName); cleaned != id {
			return cleaned
		}
	}
	if referenceName != "" {
		return neoPiLiteLLMStripUsage(referenceName)
	}
	return id
}

// neoPiLiteLLMString is toNonEmptyString.
func neoPiLiteLLMString(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return neoPiJSTrim(value.Str)
}

// neoPiLiteLLMMeta is getLiteLLMMetadataValue: the entry's own field, else
// its model_info's (a null counts as absent).
func neoPiLiteLLMMeta(entry gjson.Result, key string) gjson.Result {
	if value := entry.Get(gjson.Escape(key)); value.Exists() && value.Type != gjson.Null {
		return value
	}
	if info := entry.Get("model_info"); info.IsObject() {
		return info.Get(gjson.Escape(key))
	}
	return gjson.Result{}
}

// neoPiLiteLLMParams is getLiteLLMParams.
func neoPiLiteLLMParams(entry gjson.Result) gjson.Result {
	if params := entry.Get("litellm_params"); params.IsObject() {
		return params
	}
	return gjson.Result{}
}

// neoPiLiteLLMBool is a metadata flag: set only when it is a boolean.
func neoPiLiteLLMBool(value gjson.Result) *bool {
	switch value.Type {
	case gjson.True:
		return new(true)
	case gjson.False:
		return new(false)
	}
	return nil
}

// neoPiLiteLLMSelectableMode is isSelectableLiteLLMModelMode: only the known
// non-conversational modes leave the roster.
func neoPiLiteLLMSelectableMode(mode gjson.Result) bool {
	return mode.Type != gjson.String || !neoPiExcludedDiscoveryMode(NeoPiLiteLLMProvider, mode.Str)
}

// neoPiLiteLLMRichID is getLiteLLMRichModelId.
func neoPiLiteLLMRichID(entry gjson.Result) string {
	for _, value := range []gjson.Result{entry.Get("model_group"), entry.Get("model_name"), entry.Get("id"), neoPiLiteLLMParams(entry).Get("model")} {
		if id := neoPiLiteLLMString(value); id != "" {
			return id
		}
	}
	return ""
}

// neoPiLiteLLMOpenAIParams is getSupportedOpenAIParams: the string entries,
// or ok false when the metadata is not a list.
func neoPiLiteLLMOpenAIParams(entry gjson.Result) (params []string, ok bool) {
	value := neoPiLiteLLMMeta(entry, "supported_openai_params")
	if !value.IsArray() {
		return nil, false
	}
	params = []string{}
	for _, item := range value.Array() {
		if item.Type == gjson.String {
			params = append(params, item.Str)
		}
	}
	return params, true
}

// neoPiLiteLLMSentinel is isLiteLLMUnusableSentinelPlaceholder: a sentinel
// group that names nothing usable.
func neoPiLiteLLMSentinel(entry gjson.Result) bool {
	group, id := neoPiLiteLLMString(entry.Get("model_group")), neoPiLiteLLMString(entry.Get("id"))
	if (group == "" || !neoPiLiteLLMSentinels[group]) && (id == "" || !neoPiLiteLLMSentinels[id]) {
		return false
	}
	if providers := entry.Get("providers"); providers.Exists() && (!providers.IsArray() || len(providers.Array()) > 0) {
		return false
	}
	if name := neoPiLiteLLMString(entry.Get("model_name")); name != "" && !neoPiLiteLLMSentinels[name] {
		return false
	}
	if id != "" && !neoPiLiteLLMSentinels[id] {
		return false
	}
	if backend := neoPiLiteLLMString(neoPiLiteLLMParams(entry).Get("model")); backend != "" && !neoPiLiteLLMSentinels[backend] {
		return false
	}
	if neoPiPositive(neoPiLiteLLMMeta(entry, "max_input_tokens"), 0) > 0 || neoPiPositive(neoPiLiteLLMMeta(entry, "max_output_tokens"), 0) > 0 {
		return false
	}
	for _, key := range []string{"supports_vision", "supports_reasoning", "supports_function_calling", "supports_tools"} {
		if neoPiLiteLLMMeta(entry, key).Type == gjson.True {
			return false
		}
	}
	if params, ok := neoPiLiteLLMOpenAIParams(entry); ok && len(params) > 0 {
		return false
	}
	return true
}

// neoPiLiteLLMProviders is getLiteLLMProviders: the lowercase names, or nil
// when the entry lists none.
func neoPiLiteLLMProviders(entry gjson.Result) []string {
	value := entry.Get("providers")
	if !value.IsArray() {
		return nil
	}
	var providers []string
	for _, item := range value.Array() {
		if name := strings.ToLower(neoPiLiteLLMString(item)); name != "" {
			providers = append(providers, name)
		}
	}
	return providers
}

// neoPiLiteLLMNamespace is the route a namespaced backend id names: openai,
// other, or "" for an id without one.
func neoPiLiteLLMNamespace(value string) string {
	at := strings.IndexByte(value, '/')
	if value == "" || at <= 0 {
		return ""
	}
	if strings.ToLower(value[:at]) == "openai" {
		return "openai"
	}
	return "other"
}

// neoPiLiteLLMRoute is classifyLiteLLMApiRoute: openai, other or unknown.
func neoPiLiteLLMRoute(entry gjson.Result, id string) string {
	if entry.Exists() {
		if providers := neoPiLiteLLMProviders(entry); len(providers) > 0 {
			for _, provider := range providers {
				if provider != "openai" {
					return "other"
				}
			}
			return "openai"
		}
		params := neoPiLiteLLMParams(entry)
		if configured := strings.ToLower(neoPiLiteLLMString(params.Get("custom_llm_provider"))); configured != "" {
			if configured == "openai" {
				return "openai"
			}
			return "other"
		}
		if route := neoPiLiteLLMNamespace(neoPiLiteLLMString(params.Get("model"))); route != "" {
			return route
		}
		if route := neoPiLiteLLMNamespace(neoPiLiteLLMString(neoPiLiteLLMMeta(entry, "base_model"))); route != "" {
			return route
		}
	}
	model := id
	if strings.HasPrefix(strings.ToLower(id), "openai/") {
		model = id[len("openai/"):]
	}
	if trimmed := neoPiJSTrim(model); trimmed != "" && neoPiIsLikelyOpenAIResponsesID(trimmed) {
		return "openai"
	}
	return "unknown"
}

// neoPiLiteLLMAPI is resolveLiteLLMApi over Chat Completions: a decisions
// group runs on the Decisions API, an OpenAI-backed group on Responses.
// The empty api is Chat Completions.
func neoPiLiteLLMAPI(entry gjson.Result, id string) string {
	if entry.Exists() {
		if mode := neoPiLiteLLMMeta(entry, "mode"); mode.Type == gjson.String && mode.Str == "decisions" {
			return "openai-decisions"
		}
	}
	if neoPiLiteLLMRoute(entry, id) == "openai" {
		return "openai-responses"
	}
	return ""
}

// neoPiLiteLLMKind is liteLLMRunnerKind: a runner api's kind.
func neoPiLiteLLMKind(api string) string {
	if api == "openai-decisions" {
		return "judge"
	}
	return ""
}

// neoPiLiteLLMCost is a reported tariff: each leg the endpoint priced
// (per-token rates scaled to per million), nil for the rest.
type neoPiLiteLLMCost struct {
	input, output, cacheRead, cacheWrite *float64
}

func (c neoPiLiteLLMCost) empty() bool {
	return c.input == nil && c.output == nil && c.cacheRead == nil && c.cacheWrite == nil
}

func (c neoPiLiteLLMCost) complete() bool {
	return c.input != nil && c.output != nil && c.cacheRead != nil && c.cacheWrite != nil
}

// over is {...base, ...c}.
func (c neoPiLiteLLMCost) over(base neoPiLiteLLMCost) neoPiLiteLLMCost {
	for _, pair := range []struct{ to, from **float64 }{{&base.input, &c.input}, {&base.output, &c.output}, {&base.cacheRead, &c.cacheRead}, {&base.cacheWrite, &c.cacheWrite}} {
		if *pair.from != nil {
			*pair.to = *pair.from
		}
	}
	return base
}

// applyTo is {...cost, ...c}.
func (c neoPiLiteLLMCost) applyTo(cost NeoPiCost) NeoPiCost {
	for _, pair := range []struct {
		to   *float64
		from *float64
	}{{&cost.Input, c.input}, {&cost.Output, c.output}, {&cost.CacheRead, c.cacheRead}, {&cost.CacheWrite, c.cacheWrite}} {
		if pair.from != nil {
			*pair.to = *pair.from
		}
	}
	return cost
}

// neoPiLiteLLMReportedCost is getLiteLLMReportedCost.
func neoPiLiteLLMReportedCost(entry gjson.Result) neoPiLiteLLMCost {
	rate := func(key string) *float64 {
		if perToken, ok := neoPiToNumber(neoPiLiteLLMMeta(entry, key)); ok && perToken > 0 {
			return new(perToken * 1_000_000)
		}
		return nil
	}
	return neoPiLiteLLMCost{input: rate("input_cost_per_token"), output: rate("output_cost_per_token"),
		cacheRead: rate("cache_read_input_token_cost"), cacheWrite: rate("cache_creation_input_token_cost")}
}

// neoPiLiteLLMTariff is getLiteLLMCost: the reported tariff with missing
// legs at zero, or ok false unless input or output is reported.
func neoPiLiteLLMTariff(entry gjson.Result) (NeoPiCost, bool) {
	reported := neoPiLiteLLMReportedCost(entry)
	if reported.input == nil && reported.output == nil {
		return NeoPiCost{}, false
	}
	return reported.applyTo(NeoPiCost{}), true
}

// neoPiLiteLLMEndpointModel is LiteLLMRichEndpointModel: a mapped group and
// what its endpoint reported about it.
type neoPiLiteLLMEndpointModel struct {
	spec                     neoPiSpec
	route                    string
	supportsVision           *bool
	supportsReasoning        *bool
	hasContextWindow         bool
	hasMaxTokens             bool
	hasSupportedOpenAIParams bool
	hasCost, hasMode         bool
	reportedCost             neoPiLiteLLMCost
}

// neoPiLiteLLMRichSpec is mapLiteLLMRichEntry, or ok false for a group that
// leaves the roster.
func neoPiLiteLLMRichSpec(entry gjson.Result, runtime string, resolve func(string) *NeoPiModel) (neoPiSpec, bool) {
	if !neoPiLiteLLMSelectableMode(neoPiLiteLLMMeta(entry, "mode")) || neoPiLiteLLMSentinel(entry) {
		return neoPiSpec{}, false
	}
	id := neoPiLiteLLMRichID(entry)
	if id == "" {
		return neoPiSpec{}, false
	}
	reference := resolve(id)
	contextFallback, referenceName := neoPiLiteLLMDefaultContext, ""
	if reference != nil {
		referenceName = reference.Name
		if reference.ContextWindow > 0 {
			contextFallback = reference.ContextWindow
		}
	}
	contextWindow := neoPiPositive(neoPiLiteLLMMeta(entry, "max_input_tokens"), contextFallback)
	maxFallback := min(contextWindow, neoPiLiteLLMDefaultMax)
	if reference != nil && reference.MaxTokens > 0 {
		maxFallback = reference.MaxTokens
	}
	vision := neoPiLiteLLMBool(neoPiLiteLLMMeta(entry, "supports_vision"))
	reasoning := neoPiLiteLLMBool(neoPiLiteLLMMeta(entry, "supports_reasoning"))
	params, hasParams := neoPiLiteLLMOpenAIParams(entry)
	// Only the reference's effort vocabulary carries over: its transport
	// compat belongs to another provider.
	compat := map[string]any{"supportsStore": false, "supportsDeveloperRole": false}
	var referenceCompat map[string]any
	if reference != nil {
		referenceCompat = reference.CompatConfig
	}
	if hasParams {
		compat["supportsReasoningEffort"] = slices.Contains(params, "reasoning_effort")
	} else if value, set := referenceCompat["supportsReasoningEffort"]; set && value != nil {
		compat["supportsReasoningEffort"] = value
	}
	if value, set := referenceCompat["reasoningEffortMap"]; set && value != nil {
		compat["reasoningEffortMap"] = value
	}
	if value, set := referenceCompat["omitReasoningEffort"]; set && value != nil {
		compat["omitReasoningEffort"] = value
	}
	if vision != nil && *vision {
		// The deployment's declaration outranks the class-wide text-only
		// guard.
		compat["stripImageInput"] = false
	}
	api := neoPiLiteLLMAPI(entry, id)
	spec := neoPiSpec{
		id:            id,
		name:          neoPiLiteLLMDisplayName(neoPiLiteLLMString(entry.Get("model_name")), referenceName, id),
		api:           api,
		kind:          neoPiLiteLLMKind(api),
		baseURL:       runtime,
		contextWindow: contextWindow,
		maxTokens:     neoPiPositive(neoPiLiteLLMMeta(entry, "max_output_tokens"), maxFallback),
		compat:        compat,
	}
	switch {
	case vision != nil && *vision:
		spec.input = []string{"text", "image"}
	case vision != nil:
		spec.input = []string{"text"}
	case reference != nil && reference.Input != nil:
		spec.input = slices.Clone(reference.Input)
	default:
		spec.input = []string{"text"}
	}
	switch {
	case reasoning != nil:
		spec.reasoning = *reasoning
	case reference != nil:
		spec.reasoning = reference.Reasoning
	}
	if reference != nil {
		spec.thinking = reference.Thinking.Clone()
		spec.thinkingRequiresEffortSet = reference.thinkingRequiresEffortSet
	}
	if cost, ok := neoPiLiteLLMTariff(entry); ok {
		spec.cost = cost
	} else if reference != nil {
		spec.cost = reference.cost
	}
	return spec, true
}

// neoPiLiteLLMMergeCompat is mergeLiteLLMCompat: next's reported axes over
// existing's, where a reasoning-effort verdict inferred without a reported
// parameter list never overrides the other endpoint's.
func neoPiLiteLLMMergeCompat(existing, next map[string]any, nextReportedParams bool) map[string]any {
	if existing == nil {
		return next
	}
	if next == nil {
		return existing
	}
	merged := maps.Clone(existing)
	for axis, value := range next {
		merged[axis] = value
	}
	if !nextReportedParams {
		if reported, set := existing["supportsReasoningEffort"]; set {
			merged["supportsReasoningEffort"] = reported
		} else {
			delete(merged, "supportsReasoningEffort")
		}
	}
	return merged
}

// neoPiLiteLLMMerge is mergeLiteLLMRichEndpointModels: next's reported
// fields over existing's.
func neoPiLiteLLMMerge(existing, next neoPiLiteLLMEndpointModel) neoPiLiteLLMEndpointModel {
	route := "unknown"
	switch {
	case existing.route == "other" || next.route == "other":
		route = "other"
	case existing.route == "openai" || next.route == "openai":
		route = "openai"
	}
	// Only a group's mode selects a runner api, so either side's wins.
	api := existing.spec.api
	switch {
	case neoPiLiteLLMKind(next.spec.api) != "":
		api = next.spec.api
	case neoPiLiteLLMKind(existing.spec.api) != "":
	case next.route == route:
		api = next.spec.api
	}
	spec := existing.spec
	spec.api, spec.kind = api, neoPiLiteLLMKind(api)
	if next.spec.name != next.spec.id {
		spec.name = next.spec.name
	}
	if next.hasContextWindow {
		spec.contextWindow = next.spec.contextWindow
	}
	if next.hasMaxTokens {
		spec.maxTokens = next.spec.maxTokens
	}
	if next.supportsVision != nil {
		spec.input = next.spec.input
	}
	if next.supportsReasoning != nil {
		spec.reasoning = next.spec.reasoning
	}
	spec.cost = next.reportedCost.applyTo(existing.reportedCost.applyTo(existing.spec.cost))
	spec.compat = neoPiLiteLLMMergeCompat(existing.spec.compat, next.spec.compat, next.hasSupportedOpenAIParams)
	merged := next
	merged.route, merged.spec = route, spec
	merged.reportedCost = next.reportedCost.over(existing.reportedCost)
	merged.hasContextWindow = existing.hasContextWindow || next.hasContextWindow
	merged.hasMaxTokens = existing.hasMaxTokens || next.hasMaxTokens
	merged.hasSupportedOpenAIParams = existing.hasSupportedOpenAIParams || next.hasSupportedOpenAIParams
	merged.hasCost = existing.hasCost || next.hasCost
	merged.hasMode = existing.hasMode || next.hasMode
	return merged
}

// neoPiLiteLLMRichEntries is extractLiteLLMRichEntries: an array's object
// entries, or the first of data/models/result/items that yields entries;
// ok false when nothing does.
func neoPiLiteLLMRichEntries(payload gjson.Result) (entries []gjson.Result, ok bool) {
	if payload.IsArray() {
		entries = []gjson.Result{}
		for _, entry := range payload.Array() {
			if entry.IsObject() {
				entries = append(entries, entry)
			}
		}
		return entries, true
	}
	if !payload.IsObject() {
		return nil, false
	}
	for _, key := range []string{"data", "models", "result", "items"} {
		candidate := payload.Get(key)
		if !candidate.Exists() {
			continue
		}
		if entries, ok = neoPiLiteLLMRichEntries(candidate); ok {
			return entries, true
		}
	}
	return nil, false
}

// neoPiLiteLLMEndpoint is a LiteLLMRichEndpointResult: skipped (absent, or
// no entries), failed with an HTTP status (0 for none), or the endpoint's
// groups by id, sorted, and the ids it excluded.
type neoPiLiteLLMEndpoint struct {
	skipped  bool
	failed   bool
	status   int
	models   []neoPiLiteLLMEndpointModel
	excluded map[string]bool
}

// neoPiLiteLLMPage is one fetched management page.
type neoPiLiteLLMPage struct {
	payload gjson.Result
	status  int
	failed  bool
	skipped bool
}

func neoPiLiteLLMFetchPage(get NeoPiLiteLLMGet, endpoint string, page int) neoPiLiteLLMPage {
	if page > 0 {
		endpoint += fmt.Sprintf("?page=%d", page)
	}
	status, body, err := get(endpoint)
	if err != nil {
		return neoPiLiteLLMPage{failed: true}
	}
	if status < 200 || status > 299 {
		if status == 404 && page == 0 {
			return neoPiLiteLLMPage{skipped: true}
		}
		return neoPiLiteLLMPage{failed: true, status: status}
	}
	if !gjson.ValidBytes(body) {
		return neoPiLiteLLMPage{failed: true, status: status}
	}
	return neoPiLiteLLMPage{payload: neoPiJSONParse(string(body)), status: status}
}

// neoPiLiteLLMSafeInteger is Number.isSafeInteger for a JSON value.
func neoPiLiteLLMSafeInteger(value gjson.Result) (int, bool) {
	if value.Type != gjson.Number || value.Num != math.Trunc(value.Num) || math.Abs(value.Num) > 1<<53-1 {
		return 0, false
	}
	return int(value.Num), true
}

// neoPiLiteLLMFetchEndpoint is fetchLiteLLMRichEndpoint: every page of one
// management endpoint, mapped and deduplicated by id.
func neoPiLiteLLMFetchEndpoint(get NeoPiLiteLLMGet, management, endpoint, runtime string, resolve func(string) *NeoPiModel) neoPiLiteLLMEndpoint {
	first := neoPiLiteLLMFetchPage(get, management+endpoint, 0)
	switch {
	case first.skipped:
		return neoPiLiteLLMEndpoint{skipped: true}
	case first.failed:
		return neoPiLiteLLMEndpoint{failed: true, status: first.status}
	}
	entries, ok := neoPiLiteLLMRichEntries(first.payload)
	if !ok || len(entries) == 0 {
		return neoPiLiteLLMEndpoint{skipped: true}
	}
	invalid := func(status int) neoPiLiteLLMEndpoint { return neoPiLiteLLMEndpoint{failed: true, status: status} }
	// /v2/model/info pages its answer ({current_page, total_pages, data});
	// other endpoints and unpaginated proxies answer whole.
	if endpoint == "/v2/model/info" && first.payload.IsObject() && (first.payload.Get("current_page").Exists() || first.payload.Get("total_pages").Exists()) {
		current := first.payload.Get("current_page")
		totalPages, totalOK := neoPiLiteLLMSafeInteger(first.payload.Get("total_pages"))
		countValue := first.payload.Get("total_count")
		totalCount, countOK := neoPiLiteLLMSafeInteger(countValue)
		if current.Type != gjson.Number || current.Num != 1 || !totalOK || totalPages < 1 || totalPages > neoPiLiteLLMMaxRichPages ||
			countValue.Exists() && (!countOK || totalCount < len(entries)) {
			return invalid(first.status)
		}
		pages := make([]neoPiLiteLLMPage, totalPages-1)
		var wg sync.WaitGroup
		for i := range pages {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				pages[i] = neoPiLiteLLMFetchPage(get, management+endpoint, i+2)
			}(i)
		}
		wg.Wait()
		for i, page := range pages {
			if page.failed {
				return invalid(page.status)
			}
			next, nextOK := neoPiLiteLLMRichEntries(page.payload)
			if !page.payload.IsObject() || page.payload.Get("current_page").Type != gjson.Number || page.payload.Get("current_page").Num != float64(i+2) ||
				page.payload.Get("total_pages").Type != gjson.Number || page.payload.Get("total_pages").Num != float64(totalPages) ||
				countValue.Exists() && (page.payload.Get("total_count").Type != gjson.Number || page.payload.Get("total_count").Num != float64(totalCount)) ||
				!nextOK || len(next) == 0 {
				return invalid(page.status)
			}
			entries = append(entries, next...)
		}
		if countValue.Exists() && len(entries) != totalCount {
			return invalid(first.status)
		}
	}
	var order []string
	deduped := map[string]neoPiLiteLLMEndpointModel{}
	excluded := map[string]bool{}
	for _, entry := range entries {
		if neoPiLiteLLMSentinel(entry) {
			continue
		}
		id := neoPiLiteLLMRichID(entry)
		mode := neoPiLiteLLMMeta(entry, "mode")
		if !neoPiLiteLLMSelectableMode(mode) {
			if id != "" {
				excluded[id] = true
				delete(deduped, id)
			}
			continue
		}
		if id != "" && excluded[id] {
			continue
		}
		spec, mapped := neoPiLiteLLMRichSpec(entry, runtime, resolve)
		if !mapped {
			continue
		}
		_, hasParams := neoPiLiteLLMOpenAIParams(entry)
		_, hasCost := neoPiLiteLLMTariff(entry)
		next := neoPiLiteLLMEndpointModel{
			spec:                     spec,
			route:                    neoPiLiteLLMRoute(entry, spec.id),
			supportsVision:           neoPiLiteLLMBool(neoPiLiteLLMMeta(entry, "supports_vision")),
			supportsReasoning:        neoPiLiteLLMBool(neoPiLiteLLMMeta(entry, "supports_reasoning")),
			hasContextWindow:         neoPiPositive(neoPiLiteLLMMeta(entry, "max_input_tokens"), 0) > 0,
			hasMaxTokens:             neoPiPositive(neoPiLiteLLMMeta(entry, "max_output_tokens"), 0) > 0,
			hasSupportedOpenAIParams: hasParams,
			hasCost:                  hasCost,
			hasMode:                  mode.Type == gjson.String,
			reportedCost:             neoPiLiteLLMReportedCost(entry),
		}
		if existing, seen := deduped[spec.id]; seen {
			deduped[spec.id] = neoPiLiteLLMMerge(existing, next)
			continue
		}
		if !slices.Contains(order, spec.id) {
			order = append(order, spec.id)
		}
		deduped[spec.id] = next
	}
	if len(deduped) == 0 && len(excluded) == 0 {
		return neoPiLiteLLMEndpoint{skipped: true}
	}
	var models []neoPiLiteLLMEndpointModel
	for _, id := range order {
		if model, kept := deduped[id]; kept {
			models = append(models, model)
		}
	}
	neoPiSortStableByLocale(models, func(model neoPiLiteLLMEndpointModel) string { return model.spec.id })
	return neoPiLiteLLMEndpoint{models: models, excluded: excluded}
}

// neoPiLiteLLMRich is fetchLiteLLMRichModels: the management endpoints in
// order, the first answering with groups defining the roster and the rest
// enriching it until no group lacks metadata; ok false where NeoPi returns
// null (falling back to /models). A roster every group of which was
// excluded is an empty success.
func neoPiLiteLLMRich(baseURL string, get NeoPiLiteLLMGet, resolve func(string) *NeoPiModel) ([]neoPiSpec, bool) {
	management, runtime := NeoPiLiteLLMManagementBaseURL(baseURL), NeoPiLiteLLMRuntimeBaseURL(baseURL)
	if management == "" || runtime == "" {
		return nil, false
	}
	var order []string
	deduped := map[string]neoPiLiteLLMEndpointModel{}
	excluded := map[string]bool{}
	for _, endpoint := range neoPiLiteLLMRichEndpoints {
		result := neoPiLiteLLMFetchEndpoint(get, management, endpoint, runtime, resolve)
		if result.skipped || result.failed {
			continue
		}
		for id := range result.excluded {
			excluded[id] = true
			delete(deduped, id)
		}
		hadPrior := len(deduped) > 0
		for _, next := range result.models {
			if excluded[next.spec.id] {
				continue
			}
			existing, seen := deduped[next.spec.id]
			if !seen {
				if !hadPrior {
					if !slices.Contains(order, next.spec.id) {
						order = append(order, next.spec.id)
					}
					deduped[next.spec.id] = next
				}
				continue
			}
			deduped[next.spec.id] = neoPiLiteLLMMerge(existing, next)
		}
		if len(deduped) == 0 {
			continue
		}
		more := false
		for _, id := range order {
			entry, kept := deduped[id]
			if !kept {
				continue
			}
			if entry.supportsVision == nil || entry.route == "unknown" || !entry.hasMode ||
				!entry.reportedCost.empty() && !entry.reportedCost.complete() {
				more = true
				break
			}
		}
		if !more {
			break
		}
	}
	if len(deduped) == 0 {
		if len(excluded) > 0 {
			return []neoPiSpec{}, true
		}
		return nil, false
	}
	specs := make([]neoPiSpec, 0, len(deduped))
	for _, id := range order {
		if entry, kept := deduped[id]; kept {
			specs = append(specs, entry.spec)
		}
	}
	neoPiSortStableByLocale(specs, func(spec neoPiSpec) string { return spec.id })
	return specs, true
}

// neoPiLiteLLMListing is the manager's fallback: fetchOpenAICompatibleModels
// over <runtime>/models with mapLiteLLMOpenAICompatibleModel; ok false when
// the listing fails or is unreadable.
func neoPiLiteLLMListing(runtime string, get NeoPiLiteLLMGet, resolve func(string) *NeoPiModel) ([]neoPiSpec, bool) {
	if runtime == "" {
		return nil, false
	}
	status, body, err := get(runtime + "/models")
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(body) {
		return nil, false
	}
	entries, ok := neoPiExtractEntries(neoPiJSONParse(string(body)))
	if !ok {
		return nil, false
	}
	var order []string
	byID := map[string]neoPiSpec{}
	for _, entry := range entries {
		if !neoPiLiteLLMSelectableMode(entry.Get("mode")) {
			continue
		}
		defaults := neoPiDefaultSpec(entry)
		spec := neoPiLiteLLMWithReference(entry, defaults, resolve(defaults.id))
		spec.api = neoPiLiteLLMAPI(entry, spec.id)
		spec.kind = neoPiLiteLLMKind(spec.api)
		spec.name = neoPiLiteLLMStripUsage(spec.name)
		if spec.id == "" {
			continue
		}
		if _, seen := byID[spec.id]; !seen {
			order = append(order, spec.id)
		}
		byID[spec.id] = spec
	}
	specs := make([]neoPiSpec, 0, len(order))
	for _, id := range order {
		specs = append(specs, byID[id])
	}
	neoPiSortStableByLocale(specs, func(spec neoPiSpec) string { return spec.id })
	return specs, true
}

// neoPiLiteLLMWithReference is mapWithBundledReference: the reference as a
// chat spec (its runner kind dropped) under the listing's id, name and
// limits, on the provider's own transport and endpoint.
func neoPiLiteLLMWithReference(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	fallback := defaults.name
	if reference != nil {
		fallback = reference.Name
	}
	name := neoPiModelName(entry.Get("name"), fallback)
	if reference == nil {
		defaults.name = name
		return defaults
	}
	spec := neoPiSpec{id: defaults.id, name: name, reasoning: reference.Reasoning, input: slices.Clone(reference.Input),
		thinking: reference.Thinking.Clone(), thinkingRequiresEffortSet: reference.thinkingRequiresEffortSet,
		compat: maps.Clone(reference.CompatConfig), requestModelID: reference.RequestModelID, cost: reference.cost}
	spec.contextWindow = neoPiPositive(entry.Get("context_length"), reference.ContextWindow)
	spec.maxTokens = neoPiPositive(entry.Get("max_completion_tokens"), reference.MaxTokens)
	return spec
}
