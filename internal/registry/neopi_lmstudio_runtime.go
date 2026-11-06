package registry

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/tidwall/gjson"
)

// NeoPi's LM Studio discovery, the path its runtime takes. The model registry
// registers `lm-studio` as an implicit discoverable provider
// (coding-agent config/model-registry.ts #addImplicitDiscoverableProviders,
// in gateway mode too) with Chat Completions rows at LM_STUDIO_BASE_URL, else
// http://127.0.0.1:1234/v1, which excludes the catalog's model manager. A
// server is listed by config/model-discovery.ts discoverOpenAIModelsList with
// discovery type lm-studio: GET <base>/models beside LM Studio's native
// /api/v0/models and /api/v1/models, each row hydrated from NeoPi's bundled
// reference index.

const (
	// The implicit provider's default base.
	neoPiLmStudioDefaultBase = "http://127.0.0.1:1234/v1"
	// DISCOVERY_DEFAULT_CONTEXT_WINDOW and DISCOVERY_DEFAULT_MAX_TOKENS.
	neoPiLmStudioDefaultContext = 128_000
	neoPiLmStudioDefaultMax     = 32_768
)

// neoPiAdvertisedEfforts is ADVERTISED_REASONING_EFFORTS, least to most.
var neoPiAdvertisedEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// NeoPiLmStudioRequest performs one discovery GET with the given headers
// (the key as a bearer when the account holds a real one; Accept on the
// native requests): the status and body, or a transport error.
type NeoPiLmStudioRequest func(endpoint string, native bool) (status int, data []byte, err error)

// neoPiLmStudioHTTPError is DiscoveryHttpError: /models answered with a
// failure status.
type neoPiLmStudioHTTPError struct {
	status   int
	endpoint string
}

func (e *neoPiLmStudioHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d from %s", e.status, e.endpoint)
}

// NeoPiLmStudioBaseURL is normalizeOpenAIModelsListBaseUrl over the account's
// configured base (else the implicit provider's, LM_STUDIO_BASE_URL or the
// default): its origin and path without trailing slashes, ending in /v1; a
// base that does not parse is kept as is.
func NeoPiLmStudioBaseURL(configured string, getenv func(string) string) string {
	raw := configured
	if strings.TrimSpace(raw) == "" {
		raw = getenv("LM_STUDIO_BASE_URL")
	}
	if raw == "" {
		raw = neoPiLmStudioDefaultBase
	}
	parsed, ok := neoPiParseWHATWG(raw)
	if !ok {
		return raw
	}
	path := strings.TrimRight(parsed.path, "/")
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	return parsed.scheme + "://" + parsed.host + path
}

// NeoPiLmStudioRuntimeListing discovers an LM Studio server at base (already
// NeoPiLmStudioBaseURL) as NeoPi's runtime does. A failed or unreadable
// /models listing is ErrNeoPiListing, returned without waiting for the
// native probes (Promise.all rejects first; the caller cancels them); an
// empty listing is a NeoPiEmptyListing.
func NeoPiLmStudioRuntimeListing(base string, do NeoPiLmStudioRequest) ([]*ModelInfo, error) {
	var payload gjson.Result
	var listErr error
	var v0, v1 []byte
	listed := make(chan struct{})
	go func() {
		defer close(listed)
		endpoint := base + "/models"
		status, data, err := do(endpoint, false)
		switch {
		case err != nil:
			listErr = err
		case status < 200 || status > 299:
			listErr = &neoPiLmStudioHTTPError{status: status, endpoint: endpoint}
		case !gjson.ValidBytes(data):
			listErr = errors.New("unreadable /models answer")
		default:
			payload = gjson.ParseBytes(data)
		}
	}()
	native := NeoPiLmStudioNativeBaseURL(base)
	var natives sync.WaitGroup
	natives.Add(2)
	nativeBody := func(path string, into *[]byte) {
		defer natives.Done()
		if status, data, err := do(native+path, true); err == nil && status >= 200 && status <= 299 {
			*into = data
		}
	}
	go nativeBody("/api/v0/models", &v0)
	go nativeBody("/api/v1/models", &v1)
	<-listed
	if listErr != nil {
		return nil, fmt.Errorf("%w: lm-studio: %w", ErrNeoPiListing, listErr)
	}
	natives.Wait()
	if payload.Type == gjson.Null {
		// payload.data on null throws.
		return nil, fmt.Errorf("%w: lm-studio: null /models answer", ErrNeoPiListing)
	}
	metadata := neoPiLmStudioMetadataFrom(v0, v1)
	// for (const item of payload.data ?? []): only a payload object's data
	// counts; null or absent is empty, a string iterates characters (none has
	// an id), and any other non-list throws, as does a null item.
	var items []gjson.Result
	if data := payload.Get("data"); payload.IsObject() && data.Exists() && data.Type != gjson.Null {
		switch {
		case data.IsArray():
			items = data.Array()
		case data.Type != gjson.String:
			return nil, fmt.Errorf("%w: lm-studio: /models data is not a list", ErrNeoPiListing)
		}
	}
	var specs []neoPiSpec
	for _, item := range items {
		if item.Type == gjson.Null {
			return nil, fmt.Errorf("%w: lm-studio: null /models entry", ErrNeoPiListing)
		}
		if spec, ok := neoPiLmStudioRuntimeSpec(item, metadata); ok {
			specs = append(specs, spec)
		}
	}
	return neoPiMergeListing("lm-studio", base, specs, false)
}

// neoPiLmStudioRuntimeSpec is discoverOpenAIModelsList's mapping of one
// /models row, false for a row without an id.
func neoPiLmStudioRuntimeSpec(item gjson.Result, native map[string]neoPiLmStudioMetadata) (neoPiSpec, bool) {
	id := item.Get("id")
	if !neoPiJSTruthy(id) || id.Type != gjson.String {
		return neoPiSpec{}, false
	}
	nativeModel, hasNative := native[id.Str]
	reference := neoPiResolveModelReference(id.Str)
	metadata := item.Get("metadata")
	if !metadata.IsObject() {
		metadata = gjson.Result{}
	}
	name := id.Str
	if reference != nil && reference.Name != "" {
		name = reference.Name
	}
	if display := metadata.Get("display_name"); display.Type == gjson.String {
		if trimmed := neoPiJSTrim(display.Str); trimmed != "" {
			name = trimmed
		}
	}
	input := neoPiModelsListInput(item, metadata)
	if hasNative {
		input = nativeModel.input
	}
	if input == nil && reference != nil {
		input = slices.Clone(reference.Input)
	}
	if input == nil {
		input = []string{"text"}
	}
	limits := item.Get("limits")
	if !limits.IsObject() {
		limits = gjson.Result{}
	}
	maxInput, maxOutput := neoPiTokenLimit(limits.Get("max_input_tokens")), neoPiTokenLimit(limits.Get("max_output_tokens"))
	topProvider := item.Get("top_provider")
	if !topProvider.IsObject() {
		topProvider = gjson.Result{}
	}
	reportedMax := maxOutput
	for _, candidate := range []gjson.Result{item.Get("max_output_tokens"), item.Get("max_completion_tokens"), topProvider.Get("max_completion_tokens"), item.Get("max_tokens")} {
		if reportedMax > 0 {
			break
		}
		reportedMax = neoPiTokenLimit(candidate)
	}
	window := neoPiOllamaPositive(item.Get("max_model_len"))
	for _, candidate := range []float64{neoPiOllamaPositive(item.Get("context_length")), neoPiOllamaPositive(metadata.Get("context_window"))} {
		if window > 0 {
			break
		}
		window = candidate
	}
	if window == 0 && maxInput > 0 && maxOutput > 0 && maxInput+maxOutput <= 1<<53-1 {
		window = float64(maxInput + maxOutput)
	}
	if window == 0 && hasNative && nativeModel.contextWindow > 0 {
		window = float64(nativeModel.contextWindow)
	}
	if window == 0 && reference != nil && reference.ContextWindow > 0 {
		window = float64(reference.ContextWindow)
	}
	// A dedicated task answers through its own runner, not chat.
	if task, kind := neoPiModelsListTask(item); hasNative && nativeModel.decision || task != "" {
		if hasNative && nativeModel.decision {
			task, kind = "openai-decisions", "judge"
		}
		spec := neoPiSpec{id: id.Str, name: name, api: task, kind: kind, input: input}
		if kind != "image" {
			spec.contextWindow = int(window)
		}
		return spec, true
	}
	contextWindow := int(window)
	if contextWindow == 0 {
		contextWindow = neoPiLmStudioDefaultContext
	}
	maxTokens := reportedMax
	if maxTokens == 0 && reference != nil && reference.MaxTokens > 0 {
		maxTokens = reference.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = neoPiLmStudioDefaultMax
	}
	spec := neoPiSpec{id: id.Str, name: name, api: neopicompat.APIOpenAICompletions, input: input,
		contextWindow: contextWindow, maxTokens: min(maxTokens, contextWindow)}
	spec.compat = map[string]any{"supportsStore": false, "supportsDeveloperRole": false, "supportsReasoningEffort": false}
	if reference != nil {
		spec.reasoning = reference.Reasoning
		// inheritReferenceThinking: only a same-provider reference lends its ladder.
		if reference.Provider == "lm-studio" {
			if thinking, set, err := neoPiDecodeThinking(reference.Thinking); err == nil && thinking != nil {
				spec.thinking, spec.thinkingRequiresEffortSet = thinking, set
			}
		}
		if compat := reference.Compat; compat != nil {
			if compat.SupportsReasoningEffort != nil {
				spec.compat["supportsReasoningEffort"] = *compat.SupportsReasoningEffort
			}
			if compat.ReasoningEffortMap != nil {
				spec.compat["reasoningEffortMap"] = maps.Clone(compat.ReasoningEffortMap)
			}
			if compat.OmitReasoningEffort != nil {
				spec.compat["omitReasoningEffort"] = *compat.OmitReasoningEffort
			}
		}
	}
	neoPiWithAdvertisedEfforts(&spec, item, metadata)
	return spec, true
}

// neoPiModelsListInput is extractOpenAIModelsListInputCapabilities: every
// listed input modality (input, input_modalities, architecture's and
// metadata's), nil when none is listed.
func neoPiModelsListInput(item, metadata gjson.Result) []string {
	architecture := item.Get("architecture")
	if !architecture.IsObject() {
		architecture = gjson.Result{}
	}
	modalities := neoPiModalities(item.Get("input"), item.Get("input_modalities"), architecture.Get("input_modalities"), metadata.Get("input_modalities"))
	if len(modalities) == 0 {
		return nil
	}
	if modalities["image"] {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// neoPiModelsListTask is extractOpenAIModelsListOutputTask: the runner API
// and kind of a row whose one output modality is embeddings or images.
func neoPiModelsListTask(item gjson.Result) (api, kind string) {
	architecture := item.Get("architecture")
	if !architecture.IsObject() {
		architecture = gjson.Result{}
	}
	modalities := neoPiModalities(item.Get("output"), item.Get("output_modalities"), architecture.Get("output_modalities"))
	if len(modalities) != 1 {
		return "", ""
	}
	for modality := range modalities {
		switch modality {
		case "embedding", "embeddings":
			return "openai-embeddings", "embedding"
		case "image":
			return "openai-images", "image"
		}
	}
	return "", ""
}

// neoPiModalities is collectModalities: the lowercased strings of every
// array given.
func neoPiModalities(values ...gjson.Result) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		if !value.IsArray() {
			continue
		}
		for _, entry := range value.Array() {
			if entry.Type == gjson.String {
				out[strings.ToLower(entry.Str)] = true
			}
		}
	}
	return out
}

// neoPiTokenLimit is toTokenLimitOrUndefined: a positive safe integer, 0 for
// none.
func neoPiTokenLimit(value gjson.Result) int {
	number := neoPiOllamaPositive(value)
	if number <= 0 || number != float64(int64(number)) || number > 1<<53-1 {
		return 0
	}
	return int(number)
}

// neoPiWithAdvertisedEfforts is extractAdvertisedReasoning plus
// withAdvertisedEfforts for a Chat Completions row: a row that says it does
// not reason drops the reference's dial, and one that lists its own effort
// ladder outranks the reference's.
func neoPiWithAdvertisedEfforts(spec *neoPiSpec, item, metadata gjson.Result) {
	denial := metadata.Get("reasoning")
	if !denial.Exists() || denial.Type == gjson.Null {
		denial = item.Get("reasoning")
	}
	if denial.Type == gjson.False {
		spec.reasoning, spec.thinking, spec.thinkingRequiresEffortSet = false, nil, false
		neoPiStripBorrowedEffortCompat(spec)
		spec.compat["supportsReasoningEffort"] = false
		return
	}
	raw := metadata.Get("supported_reasoning_levels")
	if !raw.Exists() || raw.Type == gjson.Null {
		raw = item.Get("supported_reasoning_levels")
	}
	efforts, acceptsNone := neoPiAdvertisedLevels(raw)
	if len(efforts) == 0 {
		return
	}
	rawDefault := metadata.Get("default_reasoning_level")
	if !rawDefault.Exists() || rawDefault.Type == gjson.Null {
		rawDefault = item.Get("default_reasoning_level")
	}
	preferred := ""
	if rawDefault.Type == gjson.String && slices.Contains(efforts, strings.ToLower(rawDefault.Str)) {
		preferred = strings.ToLower(rawDefault.Str)
	}
	if preferred == "" {
		// The default the catalog rule resolves, when the ladder holds it.
		if policy, err := neopicompat.Resolve(neopicompat.Spec{Provider: "lm-studio", API: neopicompat.APIOpenAICompletions, ID: spec.id,
			Reasoning: spec.reasoning, Thinking: neoPiThinkingSpec(spec.thinking, spec.thinkingRequiresEffortSet), Compat: spec.compat}); err == nil {
			if level, ok := policy.Thinking["defaultLevel"].(string); ok {
				preferred = level
			}
		}
	}
	defaultLevel := efforts[0]
	if slices.Contains(efforts, preferred) {
		defaultLevel = preferred
	}
	spec.reasoning = true
	spec.thinking, spec.thinkingRequiresEffortSet = &NeoPiThinking{Mode: "effort", Efforts: efforts, DefaultLevel: defaultLevel}, false
	neoPiStripBorrowedEffortCompat(spec)
	spec.compat["supportsReasoningEffort"] = true
	if acceptsNone {
		spec.compat["reasoningDisableMode"] = "none-effort"
	}
}

// neoPiStripBorrowedEffortCompat is stripBorrowedEffortCompat: another
// host's effort map and omission never describe this endpoint's wire.
func neoPiStripBorrowedEffortCompat(spec *neoPiSpec) {
	delete(spec.compat, "reasoningEffortMap")
	delete(spec.compat, "omitReasoningEffort")
}

// neoPiAdvertisedLevels is extractReasoningLevels plus advisoryEffortEntries:
// the advertised effort tiers in canonical order, and whether "none" is
// listed.
func neoPiAdvertisedLevels(raw gjson.Result) (efforts []string, acceptsNone bool) {
	if !raw.IsArray() {
		return nil, false
	}
	seen := map[string]bool{}
	for _, entry := range raw.Array() {
		value := entry
		if entry.IsObject() {
			value = entry.Get("effort")
		}
		if value.Type != gjson.String {
			continue
		}
		level := strings.ToLower(value.Str)
		if level == "none" {
			acceptsNone = true
		}
		if slices.Contains(neoPiAdvertisedEfforts, level) {
			seen[level] = true
		}
	}
	for _, level := range neoPiAdvertisedEfforts {
		if seen[level] {
			efforts = append(efforts, level)
		}
	}
	return efforts, acceptsNone
}
