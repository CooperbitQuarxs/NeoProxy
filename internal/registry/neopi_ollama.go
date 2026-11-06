package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicompat"
	"github.com/tidwall/gjson"
)

// NeoPi's local Ollama model manager (provider-models/openai-compat.ts
// ollamaModelManagerOptions): the server's OpenAI-compatible /v1/models
// listing, each listed model enriched from Ollama's native POST /api/show
// (served window, thinking, vision), and the native GET /api/tags roster when
// the listing fails or names nothing. Every row runs on the Responses API.

// NeoPiOllamaProvider is NeoPi's local Ollama provider id.
const NeoPiOllamaProvider = "ollama"

const (
	neoPiOllamaDefaultBase = "http://127.0.0.1:11434/v1"
	// OLLAMA_FALLBACK_CONTEXT_WINDOW and OLLAMA_DEFAULT_MAX_TOKENS.
	neoPiOllamaFallbackContext = 128_000
	neoPiOllamaDefaultMax      = 8192
)

// errNeoPiOllamaNative is a native /api/tags answer NeoPi cannot read; it
// reads that answer outside its guards, so the whole discovery fails.
var errNeoPiOllamaNative = errors.New("unreadable /api/tags answer")

// NeoPiOllamaRequest performs one discovery request: a GET of endpoint when
// body is nil, else a POST of the JSON body. authorized marks the one request
// NeoPi sends the key on (the /v1/models listing, as a bearer token when
// there is a key). It returns the status and body, or a transport error.
type NeoPiOllamaRequest func(endpoint string, body []byte, authorized bool) (status int, data []byte, err error)

// NeoPiOllamaBaseURL is normalizeOllamaBaseUrl: the configured base trimmed,
// without one trailing slash and ending in /v1; empty is Ollama's default.
func NeoPiOllamaBaseURL(base string) string {
	value := neoPiJSTrim(base)
	if value == "" {
		return neoPiOllamaDefaultBase
	}
	value = strings.TrimSuffix(value, "/")
	if strings.HasSuffix(value, "/v1") {
		return value
	}
	return value + "/v1"
}

// NeoPiOllamaShows is createOllamaMetadataResolver's cache: the /api/show
// answers that succeeded, kept for the manager's lifetime so a later refresh
// asks only for models it has no answer for. A failed lookup is asked again
// next time. Safe for concurrent use.
type NeoPiOllamaShows struct {
	mu    sync.Mutex
	known map[string]neoPiOllamaShow
}

// NewNeoPiOllamaShows is a fresh metadata cache, one per configured server.
func NewNeoPiOllamaShows() *NeoPiOllamaShows {
	return &NeoPiOllamaShows{known: map[string]neoPiOllamaShow{}}
}

// neoPiOllamaShow is fetchOllamaShowMetadata's answer: a zero contextWindow
// is none reported; nil reasoning and input mean the answer carried no
// capabilities list.
type neoPiOllamaShow struct {
	contextWindow int
	reasoning     *bool
	input         []string
}

// window is the resolved context window: the reported one, else the fallback.
func (s neoPiOllamaShow) window() int {
	if s.contextWindow > 0 {
		return s.contextWindow
	}
	return neoPiOllamaFallbackContext
}

// resolve answers each id once (a duplicate shares its first lookup), from
// the cache or one concurrent /api/show per uncached id; an id whose lookup
// failed answers the zero show (the fallback window, nothing else).
func (s *NeoPiOllamaShows) resolve(native string, ids []string, do NeoPiOllamaRequest) map[string]neoPiOllamaShow {
	out := make(map[string]neoPiOllamaShow, len(ids))
	var pending []string
	s.mu.Lock()
	for _, id := range ids {
		if _, asked := out[id]; asked {
			continue
		}
		show, cached := s.known[id]
		out[id] = show
		if !cached {
			pending = append(pending, id)
		}
	}
	s.mu.Unlock()
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, id := range pending {
		wg.Add(1)
		go func() {
			defer wg.Done()
			show, ok := neoPiOllamaFetchShow(native, id, do)
			if !ok {
				return
			}
			mu.Lock()
			out[id] = show
			mu.Unlock()
			s.mu.Lock()
			s.known[id] = show
			s.mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// neoPiOllamaFetchShow is fetchOllamaShowMetadata: POST {"model": id} to
// /api/show, ok false when the server does not answer with readable JSON.
func neoPiOllamaFetchShow(native, id string, do NeoPiOllamaRequest) (neoPiOllamaShow, bool) {
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]string{"model": id}); err != nil {
		return neoPiOllamaShow{}, false
	}
	status, data, err := do(native+"/api/show", bytes.TrimSuffix(body.Bytes(), []byte("\n")), false)
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(data) {
		return neoPiOllamaShow{}, false
	}
	payload := gjson.ParseBytes(data)
	if payload.Type == gjson.Null {
		// null.capabilities throws inside NeoPi's guard: a failed lookup.
		return neoPiOllamaShow{}, false
	}
	var show neoPiOllamaShow
	if !payload.IsObject() {
		return show, true
	}
	if capabilities := payload.Get("capabilities"); capabilities.IsArray() {
		var thinking, vision bool
		for _, capability := range capabilities.Array() {
			if capability.Type != gjson.String {
				continue
			}
			switch capability.Str {
			case "thinking":
				thinking = true
			case "vision":
				vision = true
			}
		}
		show.reasoning = &thinking
		show.input = []string{"text"}
		if vision {
			show.input = append(show.input, "image")
		}
	}
	// getOllamaContextWindow: the first positive number under a key naming
	// the served window, in the answer's key order.
	if info := payload.Get("model_info"); info.IsObject() {
		info.ForEach(func(key, value gjson.Result) bool {
			if value.Type != gjson.Number || value.Float() <= 0 {
				return true
			}
			name := key.String()
			if strings.HasSuffix(name, ".context_length") || strings.HasSuffix(name, ".num_ctx") || strings.HasSuffix(name, ".context_window") {
				show.contextWindow = int(value.Float())
				return false
			}
			return true
		})
	}
	return show, true
}

// NeoPiOllamaListing resolves a local Ollama server at baseURL into the
// roster NeoPi's model manager produces, fetching through do and caching
// /api/show answers in shows. When neither the listing nor the native roster
// answers, or the native roster is unreadable, the error is ErrNeoPiListing;
// an empty answer is a NeoPiEmptyListing.
func NeoPiOllamaListing(baseURL string, shows *NeoPiOllamaShows, do NeoPiOllamaRequest) ([]*ModelInfo, error) {
	base := NeoPiOllamaBaseURL(baseURL)
	native := strings.TrimSuffix(base, "/v1")
	listed, ok := neoPiOllamaListed(base, do)
	if ok && len(listed) > 0 {
		answers := shows.resolve(native, neoPiOllamaSpecIDs(listed), do)
		for i := range listed {
			show := answers[listed[i].id]
			listed[i].contextWindow = show.window()
			if show.reasoning != nil {
				// The effort ladder is rule-owned (providers/ollama.kdl),
				// derived from the reasoning flag.
				listed[i].reasoning = *show.reasoning
				listed[i].thinking, listed[i].thinkingRequiresEffortSet = nil, false
			}
			if show.input != nil {
				listed[i].input = show.input
			}
		}
		return neoPiMergeListing(NeoPiOllamaProvider, base, listed, false)
	}
	fallback, err := neoPiOllamaNative(native, shows, do)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrNeoPiListing, NeoPiOllamaProvider, err)
	}
	if len(fallback) > 0 {
		return neoPiMergeListing(NeoPiOllamaProvider, base, fallback, false)
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s: neither /v1/models nor /api/tags answered", ErrNeoPiListing, NeoPiOllamaProvider)
	}
	// The listing answered with no models: an empty roster.
	return neoPiMergeListing(NeoPiOllamaProvider, base, nil, false)
}

// neoPiOllamaListed is fetchOpenAICompatibleModels over <base>/models on the
// Responses API: each entry the bundled reference's spec when NeoPi bundles
// the id, else the listing's id and name with Ollama's default limits; a
// repeated id keeps its first position and its last entry, and the roster is
// sorted by id. ok false when the listing fails or is unreadable.
func neoPiOllamaListed(base string, do NeoPiOllamaRequest) ([]neoPiSpec, bool) {
	status, data, err := do(base+"/models", nil, true)
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(data) {
		return nil, false
	}
	entries, ok := neoPiExtractEntries(neoPiJSONParse(string(data)))
	if !ok {
		return nil, false
	}
	var order []string
	byID := map[string]neoPiSpec{}
	for _, entry := range entries {
		defaults := neoPiDefaultSpec(entry)
		var spec neoPiSpec
		if reference := neoPiBundledRow(NeoPiOllamaProvider, defaults.id); reference != nil {
			spec = neoPiMapWithBundledReference(entry, defaults, reference)
		} else {
			spec = defaults
			spec.name = neoPiModelName(entry.Get("name"), defaults.name)
			spec.contextWindow, spec.maxTokens = neoPiOllamaFallbackContext, neoPiOllamaDefaultMax
		}
		spec.api = neopicompat.APIOpenAIResponses
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

// neoPiOllamaNative is fetchOllamaNativeModels: GET <native>/api/tags, one
// row per entry (its model, else its name, as the id; its name, else the id,
// as the name), each enriched from /api/show, duplicates kept, sorted by id.
// nil without error when the server does not answer; an answer NeoPi cannot
// read (unparsable, null, a non-list models member, a null entry) is an
// error, after the lookups NeoPi had already started.
func neoPiOllamaNative(native string, shows *NeoPiOllamaShows, do NeoPiOllamaRequest) ([]neoPiSpec, error) {
	status, data, err := do(native+"/api/tags", nil, false)
	if err != nil || status < 200 || status > 299 {
		return nil, nil
	}
	if !gjson.ValidBytes(data) {
		return nil, errNeoPiOllamaNative
	}
	payload := gjson.ParseBytes(data)
	if payload.Type == gjson.Null {
		return nil, errNeoPiOllamaNative
	}
	models := payload.Get("models")
	if !payload.IsObject() || !models.Exists() || models.Type == gjson.Null {
		return nil, nil
	}
	if !models.IsArray() {
		return nil, errNeoPiOllamaNative
	}
	var specs []neoPiSpec
	var failed error
	for _, entry := range models.Array() {
		if entry.Type == gjson.Null {
			failed = errNeoPiOllamaNative
			break
		}
		model, name := entry.Get("model"), entry.Get("name")
		id := model
		if !model.Exists() || model.Type == gjson.Null {
			id = name
		}
		if id.Type != gjson.String || id.Str == "" {
			continue
		}
		spec := neoPiSpec{id: id.Str, name: id.Str, api: neopicompat.APIOpenAIResponses, maxTokens: neoPiOllamaDefaultMax}
		if name.Type == gjson.String {
			spec.name = name.Str
		}
		specs = append(specs, spec)
	}
	answers := shows.resolve(native, neoPiOllamaSpecIDs(specs), do)
	if failed != nil {
		return nil, failed
	}
	kept := specs[:0]
	for _, spec := range specs {
		// isModelLike: a spec without a name never builds.
		if spec.name == "" {
			continue
		}
		show := answers[spec.id]
		spec.contextWindow = show.window()
		spec.input = []string{"text"}
		if show.reasoning != nil {
			spec.reasoning = *show.reasoning
		}
		if show.input != nil {
			spec.input = show.input
		}
		kept = append(kept, spec)
	}
	neoPiSortStableByLocale(kept, func(spec neoPiSpec) string { return spec.id })
	return kept, nil
}

func neoPiOllamaSpecIDs(specs []neoPiSpec) []string {
	ids := make([]string, len(specs))
	for i, spec := range specs {
		ids[i] = spec.id
	}
	return ids
}
