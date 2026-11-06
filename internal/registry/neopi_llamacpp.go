package registry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/tidwall/gjson"
)

// NeoPi's llama.cpp discovery, the path its runtime takes. The model registry
// registers `llama.cpp` as an implicit discoverable provider
// (coding-agent config/model-registry.ts #addImplicitDiscoverableProviders,
// in gateway mode too) with Responses rows; NeoPi's catalog has no llama.cpp
// model manager at all. A server is listed by config/model-discovery.ts
// discoverLlamaCppModels: llama-server's native GET /models beside GET /props,
// each discovered model on the API its KDL discovery-api names (Qwen on Chat
// Completions) at the server's /v1.

// NeoPiLlamaCppProvider is NeoPi's llama.cpp provider id.
const NeoPiLlamaCppProvider = "llama.cpp"

// NeoPiLlamaCppKeylessMarker is the key NeoPi's login stores for an empty
// paste (auth/llama.cpp.kdl empty-fallback): a keyless-mode marker that is
// never sent as a bearer and resolves no request credential.
const NeoPiLlamaCppKeylessMarker = "llama-cpp-local"

// neoPiLlamaCppDefaultBases is LLAMA_CPP_DEFAULT_BASE_URLS: the implicit
// provider's server, then its fallback.
var neoPiLlamaCppDefaultBases = []string{"http://127.0.0.1:8080", "http://127.0.0.1:9931"}

// neoPiLlamaCppIniCtx is extractLlamaCppCtxSizeFromIni's pattern.
var neoPiLlamaCppIniCtx = regexp.MustCompile(`(?:^|\n)\s*ctx-size\s*=\s*(-?\d+)\s*(?:$|\n)`)

// NeoPiLlamaCppRequest performs one discovery GET under ctx (the account's key
// as a bearer when it holds a real one): the status and body, or a transport
// error.
type NeoPiLlamaCppRequest func(ctx context.Context, endpoint string) (status int, data []byte, err error)

// neoPiLlamaCppHTTPError is DiscoveryHttpError: the server answered /models
// with a failure status, which ends discovery without trying another server.
type neoPiLlamaCppHTTPError struct {
	status   int
	endpoint string
}

func (e *neoPiLlamaCppHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d from %s", e.status, e.endpoint)
}

// NeoPiLlamaCppBases are the servers an account is discovered at, in order:
// its configured base; else the implicit provider's, LLAMA_CPP_BASE_URL or
// the default with its fallback.
func NeoPiLlamaCppBases(configured string, getenv func(string) string) []string {
	if strings.TrimSpace(configured) != "" {
		return []string{configured}
	}
	if base := getenv("LLAMA_CPP_BASE_URL"); base != "" {
		return []string{base}
	}
	return neoPiLlamaCppDefaultBases
}

// NeoPiLlamaCppBaseURL is normalizeLlamaCppBaseUrl: the base's origin and
// path without trailing slashes, read as the WHATWG URL parser reads it; a
// base that does not parse is kept as is.
func NeoPiLlamaCppBaseURL(base string) string {
	raw := base
	if raw == "" {
		raw = neoPiLlamaCppDefaultBases[0]
	}
	parsed, ok := neoPiParseWHATWG(raw)
	if !ok {
		return raw
	}
	return parsed.scheme + "://" + parsed.host + strings.TrimRight(parsed.path, "/")
}

// NeoPiLlamaCppV1BaseURL is ensureLlamaCppV1BaseUrl: where a server's rows
// run.
func NeoPiLlamaCppV1BaseURL(base string) string {
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

// neoPiLlamaCppNativeBase is toLlamaCppNativeBaseUrl: the base without its
// /v1, where /props lives.
func neoPiLlamaCppNativeBase(base string) string {
	parsed, ok := neoPiParseWHATWG(base)
	if !ok {
		return strings.TrimSuffix(base, "/v1")
	}
	path := strings.TrimRight(parsed.path, "/")
	if strings.HasSuffix(path, "/v1") {
		path = path[:len(path)-3]
	}
	if path == "" {
		path = "/"
	}
	return strings.TrimSuffix(parsed.scheme+"://"+parsed.host+path, "/")
}

// neoPiLlamaCppEntry is one /models entry (LlamaCppModelListEntry); zero
// windows are none.
type neoPiLlamaCppEntry struct {
	id                                  string
	input                               []string
	runtime, configured, trainingWindow float64
}

// neoPiLlamaCppServer is discoverLlamaCppServerMetadata's /props answer.
type neoPiLlamaCppServer struct {
	contextWindow float64
	unlimited     bool
	input         []string
}

// NeoPiLlamaCppListing discovers the llama.cpp servers an account names
// (NeoPiLlamaCppBases) as NeoPi's runtime does: the first that answers
// defines the roster. A server that answers /models with a failure status
// ends discovery; only an unreachable or unreadable one moves on to the
// next. Failure is ErrNeoPiListing; an empty roster is a NeoPiEmptyListing.
func NeoPiLlamaCppListing(ctx context.Context, bases []string, do NeoPiLlamaCppRequest) ([]*ModelInfo, error) {
	var primary error
	for i, base := range bases {
		normalized := NeoPiLlamaCppBaseURL(base)
		specs, err := neoPiLlamaCppAt(ctx, normalized, do)
		if err == nil {
			return neoPiMergeListing(NeoPiLlamaCppProvider, NeoPiLlamaCppV1BaseURL(normalized), specs, false)
		}
		if httpErr := (*neoPiLlamaCppHTTPError)(nil); errors.As(err, &httpErr) {
			return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, NeoPiLlamaCppProvider, err)
		}
		if i == 0 {
			primary = err
		}
	}
	return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, NeoPiLlamaCppProvider, primary)
}

// neoPiLlamaCppAt is discoverLlamaCppModelsAt for a normalized base: /models
// and /props fetched together, each entry mapped to its row. Like its
// Promise.all, both requests are sent and a failed /models ends the attempt
// without waiting for /props to answer: once dispatched, the /props request
// is cancelled (no deadline is involved) and returns promptly.
func neoPiLlamaCppAt(ctx context.Context, base string, do NeoPiLlamaCppRequest) ([]neoPiSpec, error) {
	propsCtx, cancelProps := context.WithCancel(ctx)
	defer cancelProps()
	props := make(chan *neoPiLlamaCppServer, 1)
	dispatched := make(chan struct{})
	go func() {
		props <- neoPiLlamaCppFetchServer(propsCtx, base, func(ctx context.Context, endpoint string) (int, []byte, error) {
			close(dispatched)
			return do(ctx, endpoint)
		})
	}()
	fail := func(err error) ([]neoPiSpec, error) {
		<-dispatched
		cancelProps()
		<-props
		return nil, err
	}
	endpoint := base + "/models"
	status, data, err := do(ctx, endpoint)
	switch {
	case err != nil:
		return fail(err)
	case status < 200 || status > 299:
		return fail(&neoPiLlamaCppHTTPError{status: status, endpoint: endpoint})
	case !gjson.ValidBytes(data):
		return fail(errors.New("unreadable /models answer"))
	}
	payload := gjson.ParseBytes(data)
	server := <-props
	v1 := NeoPiLlamaCppV1BaseURL(base)
	var specs []neoPiSpec
	for _, entry := range neoPiLlamaCppEntries(payload) {
		window := entry.runtime
		for _, candidate := range []float64{entry.configured, neoPiLlamaCppServerWindow(server), entry.trainingWindow, neoPiOllamaDefaultContext} {
			if window > 0 {
				break
			}
			window = candidate
		}
		maxTokens := min(window, neoPiOllamaDefaultMax)
		if server != nil && server.unlimited {
			maxTokens = window
		}
		input := entry.input
		if input == nil && server != nil {
			input = server.input
		}
		if input == nil {
			input = []string{"text"}
		}
		spec := neoPiSpec{id: entry.id, name: entry.id, api: neopicompat.APIOpenAIResponses, input: input,
			contextWindow: int(window), maxTokens: int(maxTokens)}
		spec.api = neoPiDiscoveryAPI(NeoPiLlamaCppProvider, v1, &spec)
		specs = append(specs, spec)
	}
	return specs, nil
}

// neoPiDiscoveryAPI is buildDiscoveredModel's resolveDiscoveryApi: the API
// the provider's KDL `discovery-api` assigns the spec, else its own.
func neoPiDiscoveryAPI(provider, baseURL string, spec *neoPiSpec) string {
	policy, err := neopicompat.Resolve(neopicompat.Spec{Provider: provider, API: spec.api, ID: spec.id, BaseURL: baseURL,
		Reasoning: spec.reasoning, Thinking: neoPiThinkingSpec(spec.thinking, spec.thinkingRequiresEffortSet), Compat: spec.compat})
	if err != nil {
		return spec.api
	}
	if api, ok := policy.Catalog["discoveryApi"].(string); ok && api != "" {
		return api
	}
	return spec.api
}

// neoPiLlamaCppServerWindow is the /props window, 0 without one.
func neoPiLlamaCppServerWindow(server *neoPiLlamaCppServer) float64 {
	if server == nil {
		return 0
	}
	return server.contextWindow
}

// neoPiLlamaCppEntries is parseLlamaCppModelList: data's object entries with
// a non-empty string id.
func neoPiLlamaCppEntries(payload gjson.Result) []neoPiLlamaCppEntry {
	data := payload.Get("data")
	if !payload.IsObject() || !data.IsArray() {
		return nil
	}
	var entries []neoPiLlamaCppEntry
	for _, item := range data.Array() {
		id := item.Get("id")
		if !item.IsObject() || id.Type != gjson.String || id.Str == "" {
			continue
		}
		entry := neoPiLlamaCppEntry{id: id.Str}
		if modalities := item.Get("architecture.input_modalities"); item.Get("architecture").IsObject() && modalities.IsArray() {
			entry.input = []string{"text"}
			for _, modality := range modalities.Array() {
				if modality.Type == gjson.String && strings.ToLower(modality.Str) == "image" {
					entry.input = []string{"text", "image"}
				}
			}
		}
		if meta := item.Get("meta"); meta.IsObject() {
			entry.runtime = neoPiOllamaPositive(meta.Get("n_ctx"))
			entry.trainingWindow = neoPiOllamaPositive(meta.Get("n_ctx_train"))
		}
		if status := item.Get("status"); status.IsObject() {
			entry.configured = neoPiLlamaCppArgsCtx(status.Get("args"))
			if entry.configured == 0 {
				if preset := status.Get("preset"); preset.Type == gjson.String {
					if match := neoPiLlamaCppIniCtx.FindStringSubmatch(preset.Str); match != nil {
						entry.configured = neoPiOllamaPositive(gjson.Result{Type: gjson.String, Str: match[1]})
					}
				}
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// neoPiLlamaCppArgsCtx is extractLlamaCppCtxSizeFromArgs: the first usable
// --ctx-size/-c value of a rendered argument vector (`--flag value` or
// `--flag=value`), 0 for none.
func neoPiLlamaCppArgsCtx(args gjson.Result) float64 {
	if !args.IsArray() {
		return 0
	}
	values := args.Array()
	for i, raw := range values {
		if raw.Type != gjson.String {
			continue
		}
		flag, value, hasValue := strings.Cut(raw.Str, "=")
		if flag != "--ctx-size" && flag != "-c" {
			continue
		}
		var candidate gjson.Result
		if hasValue {
			candidate = gjson.Result{Type: gjson.String, Str: value}
		} else if i+1 < len(values) {
			candidate = values[i+1]
		}
		if parsed := neoPiOllamaPositive(candidate); parsed > 0 {
			return parsed
		}
	}
	return 0
}

// neoPiLlamaCppFetchServer is discoverLlamaCppServerMetadata: GET /props at
// the native base; nil when it does not answer with a JSON object.
func neoPiLlamaCppFetchServer(ctx context.Context, base string, do NeoPiLlamaCppRequest) *neoPiLlamaCppServer {
	status, data, err := do(ctx, neoPiLlamaCppNativeBase(base)+"/props")
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(data) {
		return nil
	}
	payload := gjson.ParseBytes(data)
	if !payload.IsObject() {
		return nil
	}
	server := &neoPiLlamaCppServer{}
	settings := payload.Get("default_generation_settings")
	if settings.IsObject() {
		server.contextWindow = neoPiOllamaPositive(settings.Get("n_ctx"))
	}
	if server.contextWindow == 0 {
		server.contextWindow = neoPiOllamaPositive(payload.Get("n_ctx"))
	}
	// extractLlamaCppMaxTokens: only the -1 sentinel bounds generation by the
	// window; positive values are per-request defaults, not caps.
	var candidates []gjson.Result
	if settings.IsObject() {
		if params := settings.Get("params"); params.IsObject() {
			candidates = append(candidates, params.Get("max_tokens"), params.Get("n_predict"))
		}
		candidates = append(candidates, settings.Get("max_tokens"), settings.Get("n_predict"))
	}
	candidates = append(candidates, payload.Get("max_tokens"), payload.Get("n_predict"))
	for _, candidate := range candidates {
		if neoPiLlamaCppUnlimited(candidate) {
			server.unlimited = true
		}
	}
	if modalities := payload.Get("modalities"); modalities.IsObject() {
		server.input = []string{"text"}
		if modalities.Get("vision").Type == gjson.True {
			server.input = []string{"text", "image"}
		}
	}
	return server
}

// neoPiLlamaCppUnlimited is isLlamaCppUnlimitedSentinel: -1, as a number or
// a string that reads as one.
func neoPiLlamaCppUnlimited(value gjson.Result) bool {
	switch value.Type {
	case gjson.Number:
		return value.Float() == -1
	case gjson.String:
		if text := neoPiJSTrim(value.Str); text != "" {
			number, ok := neoPiJSNumber(text)
			return ok && number == -1
		}
	}
	return false
}
