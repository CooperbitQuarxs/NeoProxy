package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/tidwall/gjson"
)

// NeoPi's local Ollama discovery, the path its runtime takes. The model
// registry registers `ollama` as an implicit discoverable provider
// (coding-agent config/model-registry.ts #addImplicitDiscoverableProviders,
// in gateway mode too), which excludes the catalog's model manager. A local
// server is therefore listed by config/model-discovery.ts
// discoverOllamaModels: the native GET /api/tags roster, each entry enriched
// from POST /api/show (served window, thinking, vision). Every row runs on the
// Responses API at the server's /v1.

// NeoPiOllamaProvider is NeoPi's local Ollama provider id.
const NeoPiOllamaProvider = "ollama"

const (
	// DEFAULT_OLLAMA_BASE_URL and OLLAMA_HOST_DEFAULT_PORT.
	neoPiOllamaDefaultOrigin = "http://127.0.0.1:11434"
	neoPiOllamaHostPort      = "11434"
	// DISCOVERY_DEFAULT_CONTEXT_WINDOW and DISCOVERY_DEFAULT_MAX_TOKENS.
	neoPiOllamaDefaultContext = 128_000
	neoPiOllamaDefaultMax     = 32_768
)

// errNeoPiOllamaRoster is an /api/tags answer NeoPi's discovery throws on.
var errNeoPiOllamaRoster = errors.New("unreadable /api/tags answer")

// neoPiOllamaNumCtx is extractOllamaRuntimeContextWindow's pattern: the
// num_ctx line of the model's runtime parameters.
var neoPiOllamaNumCtx = regexp.MustCompile(`(?m)(?:^|\n)\s*num_ctx\s+(\d+)\s*(?:$|\n)`)

// NeoPiOllamaRequest performs one discovery request: a GET of endpoint when
// body is nil, else a POST of the JSON body. NeoPi's discovery sends no key.
// It returns the status and body, or a transport error.
type NeoPiOllamaRequest func(endpoint string, body []byte) (status int, data []byte, err error)

// NeoPiOllamaOrigin is the server an account reaches: its configured base,
// else NeoPi's implicit one (getImplicitOllamaBaseUrl: OLLAMA_BASE_URL, else
// OLLAMA_HOST, else the default), reduced to its origin as
// normalizeOllamaBaseUrl does through the WHATWG URL parser (a base that does
// not parse is the default).
func NeoPiOllamaOrigin(configured string, getenv func(string) string) string {
	base := configured
	if strings.TrimSpace(base) == "" {
		base = neoPiOllamaImplicitBase(getenv)
	}
	parsed, ok := neoPiParseWHATWG(base)
	if !ok {
		return neoPiOllamaDefaultOrigin
	}
	return parsed.scheme + "://" + parsed.host
}

// neoPiOllamaImplicitBase is getImplicitOllamaBaseUrl.
func neoPiOllamaImplicitBase(getenv func(string) string) string {
	if base := neoPiJSTrim(getenv("OLLAMA_BASE_URL")); base != "" {
		return base
	}
	if host := neoPiOllamaHostEnv(getenv("OLLAMA_HOST")); host != "" {
		return host
	}
	return neoPiOllamaDefaultOrigin
}

// neoPiOllamaHostEnv is normalizeOllamaHostEnv: an OLLAMA_HOST value as an
// http(s) origin, Ollama's port added to an http host without one; empty
// when the value is blank or names another scheme.
func neoPiOllamaHostEnv(value string) string {
	trimmed := neoPiJSTrim(value)
	if trimmed == "" {
		return ""
	}
	candidate := "http://" + trimmed
	switch {
	case strings.Contains(trimmed, "://"):
		candidate = trimmed
	case strings.HasPrefix(trimmed, "//"):
		candidate = "http:" + trimmed
	case strings.HasPrefix(trimmed, ":"):
		candidate = "http://127.0.0.1" + trimmed
	}
	parsed, ok := neoPiParseWHATWG(candidate)
	if !ok || parsed.hostname == "" || (parsed.scheme != "http" && parsed.scheme != "https") {
		return ""
	}
	host := parsed.host
	// URL.port is empty for an absent or default port.
	if parsed.scheme == "http" && parsed.port == "" {
		host = parsed.hostname + ":" + neoPiOllamaHostPort
	}
	return parsed.scheme + "://" + host
}

// NeoPiOllamaBaseURL is the endpoint an account's rows run on: its server's
// /v1.
func NeoPiOllamaBaseURL(configured string, getenv func(string) string) string {
	return NeoPiOllamaOrigin(configured, getenv) + "/v1"
}

// neoPiOllamaContextOverride is getOllamaContextLengthOverride: a positive
// safe integer OLLAMA_CONTEXT_LENGTH, which the registry applies to every
// discovered row.
func neoPiOllamaContextOverride(getenv func(string) string) int {
	value := neoPiJSTrim(getenv("OLLAMA_CONTEXT_LENGTH"))
	if value == "" {
		return 0
	}
	number, ok := neoPiJSNumber(value)
	if !ok || number <= 0 || number != math.Trunc(number) || number > 1<<53-1 {
		return 0
	}
	return int(number)
}

// neoPiJSNumber is Number(text) for a trimmed, non-empty string: decimal or
// exponent notation, or a 0x/0o/0b integer. Callers reject what is not a
// positive finite number, so strconv's extra spellings (inf, nan) never pass.
func neoPiJSNumber(text string) (float64, bool) {
	if len(text) > 2 && text[0] == '0' {
		base := 0
		switch text[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			value, err := strconv.ParseUint(text[2:], base, 64)
			return float64(value), err == nil
		}
	}
	if lower := strings.ToLower(strings.TrimLeft(text, "+-")); strings.HasPrefix(lower, "0x") {
		// A signed hex literal is NaN to JavaScript.
		return 0, false
	}
	value, err := strconv.ParseFloat(text, 64)
	return value, err == nil
}

// neoPiOllamaPositive is toPositiveNumberOrUndefined: a positive finite
// number, or a non-blank string that reads as one; 0 for none.
func neoPiOllamaPositive(value gjson.Result) float64 {
	switch value.Type {
	case gjson.Number:
		if number := value.Float(); number > 0 && !math.IsInf(number, 0) {
			return number
		}
	case gjson.String:
		if text := neoPiJSTrim(value.Str); text != "" {
			if number, ok := neoPiJSNumber(text); ok && number > 0 && !math.IsInf(number, 0) {
				return number
			}
		}
	}
	return 0
}

// neoPiOllamaShow is discoverOllamaModelMetadata's answer for one model.
type neoPiOllamaShow struct {
	reasoning     bool
	input         []string
	contextWindow float64
}

// neoPiOllamaFetchShow is discoverOllamaModelMetadata: POST {"model": <id>}
// to /api/show; nil when the server does not answer with a JSON object.
func neoPiOllamaFetchShow(origin, token string, do NeoPiOllamaRequest) *neoPiOllamaShow {
	status, data, err := do(origin+"/api/show", []byte(`{"model":`+token+`}`))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(data) {
		return nil
	}
	payload := gjson.ParseBytes(data)
	if !payload.IsObject() {
		return nil
	}
	show := &neoPiOllamaShow{input: []string{"text"}, contextWindow: neoPiOllamaContext(payload)}
	switch capabilities := payload.Get("capabilities"); {
	case capabilities.IsArray():
		var vision bool
		for _, capability := range capabilities.Array() {
			if capability.Type != gjson.String {
				continue
			}
			switch strings.ToLower(capability.Str) {
			case "thinking":
				show.reasoning = true
			case "vision", "image":
				vision = true
			}
		}
		if vision {
			show.input = []string{"text", "image"}
		}
	case capabilities.IsObject():
		show.reasoning = capabilities.Get("thinking").Type == gjson.True
		if capabilities.Get("vision").Type == gjson.True || capabilities.Get("image").Type == gjson.True {
			show.input = []string{"text", "image"}
		}
	}
	return show
}

// neoPiOllamaContext is extractOllamaContextWindow: the num_ctx the model
// runs with, else the first positive model_info value under context_length
// or a key ending in .context_length; 0 for none.
func neoPiOllamaContext(payload gjson.Result) float64 {
	if parameters := payload.Get("parameters"); parameters.Type == gjson.String {
		if match := neoPiOllamaNumCtx.FindStringSubmatch(parameters.Str); match != nil {
			if number, err := strconv.ParseFloat(match[1], 64); err == nil && number > 0 {
				return number
			}
		}
	}
	var window float64
	if info := payload.Get("model_info"); info.IsObject() {
		info.ForEach(func(key, value gjson.Result) bool {
			name := key.String()
			if name != "context_length" && !strings.HasSuffix(name, ".context_length") {
				return true
			}
			window = neoPiOllamaPositive(value)
			return window == 0
		})
	}
	return window
}

// neoPiOllamaToken is JSON.stringify of an entry's id.
func neoPiOllamaToken(value gjson.Result) string {
	if value.Type == gjson.String {
		var body bytes.Buffer
		encoder := json.NewEncoder(&body)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(value.Str)
		return strings.TrimSuffix(body.String(), "\n")
	}
	if value.Type == gjson.Number {
		return strconv.FormatFloat(value.Float(), 'f', -1, 64)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(value.Raw)); err != nil {
		return value.Raw
	}
	return compact.String()
}

// NeoPiOllamaListing resolves the Ollama server an account configures (or
// NeoPi's implicit one) into the rows NeoPi's discovery builds: one per
// /api/tags entry with an id (its model, else its name; its name, else the
// id, as the name; JavaScript truthiness), in roster order with duplicates,
// each enriched by its own /api/show, at the server's /v1 on the Responses
// API, then OLLAMA_CONTEXT_LENGTH applied to the built rows. A failed or
// unreadable roster, or one NeoPi cannot build (a null entry, a non-string
// id or name), is ErrNeoPiListing; an empty one is a NeoPiEmptyListing.
func NeoPiOllamaListing(configured string, getenv func(string) string, do NeoPiOllamaRequest) ([]*ModelInfo, error) {
	origin := NeoPiOllamaOrigin(configured, getenv)
	specs, err := neoPiOllamaRoster(origin, do)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, NeoPiOllamaProvider, err)
	}
	models, err := neoPiMergeListing(NeoPiOllamaProvider, origin+"/v1", specs, false)
	override := neoPiOllamaContextOverride(getenv)
	if err != nil || override == 0 {
		return models, err
	}
	// #normalizeDiscoverableModels overrides built rows, so the override wins
	// over the limits the catalog patches onto an id it knows.
	for i, info := range models {
		row := info.NeoPi.Clone()
		row.ContextWindow, row.MaxTokens = override, min(override, neoPiOllamaDefaultMax)
		models[i] = NeoPiCompletionsModelInfo(row, NeoPiOllamaProvider)
	}
	return models, nil
}

// neoPiOllamaRoster is discoverOllamaModels' fetch and mapping.
func neoPiOllamaRoster(origin string, do NeoPiOllamaRequest) ([]neoPiSpec, error) {
	status, data, err := do(origin+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("HTTP %d from %s/api/tags", status, origin)
	}
	if !gjson.ValidBytes(data) {
		return nil, errNeoPiOllamaRoster
	}
	payload := gjson.ParseBytes(data)
	if payload.Type == gjson.Null {
		return nil, errNeoPiOllamaRoster
	}
	var models gjson.Result
	if payload.IsObject() {
		models = payload.Get("models")
	}
	if !models.Exists() || models.Type == gjson.Null {
		return nil, nil
	}
	if !models.IsArray() {
		return nil, errNeoPiOllamaRoster
	}
	type entry struct{ id, name gjson.Result }
	var entries []entry
	for _, item := range models.Array() {
		if item.Type == gjson.Null {
			return nil, errNeoPiOllamaRoster
		}
		model, name := item.Get("model"), item.Get("name")
		id := model
		if !neoPiJSTruthy(model) {
			id = name
		}
		if !neoPiJSTruthy(id) {
			continue
		}
		if !neoPiJSTruthy(name) {
			name = id
		}
		entries = append(entries, entry{id: id, name: name})
	}
	// Every entry's own lookup, concurrently; a repeated id takes its last
	// lookup's answer, as NeoPi's Map does.
	shows := make([]*neoPiOllamaShow, len(entries))
	var wg sync.WaitGroup
	for i, item := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			shows[i] = neoPiOllamaFetchShow(origin, neoPiOllamaToken(item.id), do)
		}()
	}
	wg.Wait()
	byID := make(map[string]*neoPiOllamaShow, len(entries))
	for i, item := range entries {
		byID[neoPiOllamaToken(item.id)] = shows[i]
	}
	specs := make([]neoPiSpec, 0, len(entries))
	for _, item := range entries {
		if item.id.Type != gjson.String || item.name.Type != gjson.String {
			// buildModel cannot build it, and the whole discovery fails.
			return nil, errNeoPiOllamaRoster
		}
		spec := neoPiSpec{id: item.id.Str, name: item.name.Str, api: neopicompat.APIOpenAIResponses,
			input: []string{"text"}, contextWindow: neoPiOllamaDefaultContext, maxTokens: neoPiOllamaDefaultMax}
		if show := byID[neoPiOllamaToken(item.id)]; show != nil {
			spec.reasoning, spec.input = show.reasoning, show.input
			if show.contextWindow > 0 {
				// NeoPi keeps a fractional window; NeoProxy's limits are whole tokens.
				spec.contextWindow = int(show.contextWindow)
				spec.maxTokens = min(int(show.contextWindow), neoPiOllamaDefaultMax)
			}
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// neoPiJSTruthy is JavaScript truthiness for a JSON value (absent is
// undefined).
func neoPiJSTruthy(value gjson.Result) bool {
	switch value.Type {
	case gjson.Null, gjson.False:
		return false
	case gjson.Number:
		return value.Float() != 0
	case gjson.String:
		return value.Str != ""
	}
	return value.Exists()
}
