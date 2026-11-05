package registry

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"sync"
)

// NeoPi's bundled catalog for each provider NeoProxy serves through NeoPi's
// OpenAI Chat Completions provider, exported by
// runtime/executor/testdata/capture_neopi_completions.ts: every row with the
// compat NeoPi resolves for it (deduplicated), plus the compat NeoPi derives
// for a discovered id the bundle does not hold.
//
//go:embed models/neopi_completions/*.json.gz
var neopiCompletionsCatalogFiles embed.FS

// NeoPiCompletionsCatalog is one provider's exported catalog.
type NeoPiCompletionsCatalog struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl"`
	// Compats are the resolved completions compats the rows share.
	Compats []*NeoPiCompletionsCompat `json:"compats"`
	// Discovery holds the model manager's authority flags: how a provider's
	// listing replaces its bundled rows (mergeDynamicModels). Listing is
	// false when NeoPi never fetches one and serves the bundled roster.
	Discovery struct {
		Listing bool `json:"listing"`
		// Unauthenticated is set when the listing also runs without a key,
		// sending no Authorization (Charm Hyper's public /v1/models).
		Unauthenticated        bool `json:"unauthenticated"`
		ModelsAuthoritative    bool `json:"modelsAuthoritative"`
		ReasoningAuthoritative bool `json:"reasoningAuthoritative"`
		InputAuthoritative     bool `json:"inputAuthoritative"`
		CostAuthoritative      bool `json:"costAuthoritative"`
	} `json:"discovery"`
	Models []struct {
		ID            string            `json:"id"`
		Name          string            `json:"name"`
		API           string            `json:"api"`
		Kind          string            `json:"kind"`
		Provider      string            `json:"provider"`
		BaseURL       string            `json:"baseUrl"`
		Reasoning     bool              `json:"reasoning"`
		Input         []string          `json:"input"`
		ContextWindow int               `json:"contextWindow"`
		MaxTokens     int               `json:"maxTokens"`
		Thinking      json.RawMessage   `json:"thinking"`
		Headers       map[string]string `json:"headers"`
		CompatConfig  map[string]any    `json:"compatConfig"`
		CompatIndex   int               `json:"completionsCompatIndex"`
		// Cost is the bundled row's token tariff.
		Cost NeoPiCost `json:"cost"`
	} `json:"models"`
}

// NeoPiCost is NeoPi's TokenCost: USD per million tokens per leg.
type NeoPiCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// priced is NeoPi's "input or output is nonzero".
func (c NeoPiCost) priced() bool {
	return c.Input != 0 || c.Output != 0
}

var (
	neopiCompletionsOnce    sync.Once
	neopiCompletionsCatalog map[string]*NeoPiCompletionsCatalog
	neopiCompletionsRows    map[string][]*NeoPiModel
	neopiCompletionsLoadErr error
)

func loadNeoPiCompletionsCatalogs() {
	catalogs := map[string]*NeoPiCompletionsCatalog{}
	rows := map[string][]*NeoPiModel{}
	entries, err := fs.ReadDir(neopiCompletionsCatalogFiles, "models/neopi_completions")
	if err != nil {
		neopiCompletionsLoadErr = err
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.gz") {
			continue
		}
		data, readErr := readGzipFile("models/neopi_completions/" + name)
		if readErr != nil {
			neopiCompletionsLoadErr = readErr
			return
		}
		catalog := &NeoPiCompletionsCatalog{}
		if unmarshalErr := json.Unmarshal(data, catalog); unmarshalErr != nil {
			neopiCompletionsLoadErr = fmt.Errorf("registry: %s: %w", name, unmarshalErr)
			return
		}
		catalogs[catalog.Provider] = catalog
		providerRows := make([]*NeoPiModel, 0, len(catalog.Models))
		for i := range catalog.Models {
			row := catalog.Models[i]
			// A runner row (image, speech) carries no completions compat.
			var compat *NeoPiCompletionsCompat
			if !neoPiRunnerAPI(row.API) {
				if row.CompatIndex < 0 || row.CompatIndex >= len(catalog.Compats) {
					neopiCompletionsLoadErr = fmt.Errorf("registry: %s: %s: compat index %d out of range", name, row.ID, row.CompatIndex)
					return
				}
				compat = catalog.Compats[row.CompatIndex]
			}
			thinking, requiresEffortSet, thinkingErr := neoPiDecodeThinking(row.Thinking)
			if thinkingErr != nil {
				neopiCompletionsLoadErr = fmt.Errorf("registry: %s: %s: %w", name, row.ID, thinkingErr)
				return
			}
			providerRows = append(providerRows, &NeoPiModel{
				ID:                        row.ID,
				Name:                      row.Name,
				API:                       row.API,
				Kind:                      row.Kind,
				Provider:                  row.Provider,
				BaseURL:                   row.BaseURL,
				Reasoning:                 row.Reasoning,
				Input:                     row.Input,
				ContextWindow:             row.ContextWindow,
				MaxTokens:                 row.MaxTokens,
				Thinking:                  thinking,
				CompletionsCompat:         compat,
				CompatConfig:              row.CompatConfig,
				cost:                      row.Cost,
				thinkingRequiresEffortSet: requiresEffortSet,
			})
		}
		rows[catalog.Provider] = providerRows
	}
	neopiCompletionsCatalog, neopiCompletionsRows = catalogs, rows
}

func readGzipFile(name string) ([]byte, error) {
	file, err := neopiCompletionsCatalogFiles.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	return io.ReadAll(reader)
}

// NeoPiCompletionsProvider reports whether provider has an exported catalog.
func NeoPiCompletionsProvider(provider string) (*NeoPiCompletionsCatalog, bool) {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	if neopiCompletionsLoadErr != nil {
		return nil, false
	}
	catalog, ok := neopiCompletionsCatalog[provider]
	return catalog, ok
}

// NeoPiCompletionsProviders lists the providers with an exported catalog.
func NeoPiCompletionsProviders() []string {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	out := make([]string, 0, len(neopiCompletionsCatalog))
	for provider := range neopiCompletionsCatalog {
		out = append(out, provider)
	}
	slices.Sort(out)
	return out
}

// NeoPiCompletionsBundledModels is the provider's bundled roster as registry
// models, each carrying the NeoPi record its executor shapes requests from.
func NeoPiCompletionsBundledModels(provider, ownedBy string) []*ModelInfo {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	out := make([]*ModelInfo, 0, len(neopiCompletionsRows[provider]))
	for _, row := range neopiCompletionsRows[provider] {
		out = append(out, NeoPiCompletionsModelInfo(row, ownedBy))
	}
	return out
}

// NeoPiCompletionsModel resolves a provider's model id to NeoPi's record: the
// bundled row, else the row NeoPi builds for an id the bundle lacks (a bare
// non-reasoning spec on the provider's endpoint). Nil when the provider is
// unknown or NeoPi cannot build the id.
func NeoPiCompletionsModel(provider, id string) *NeoPiModel {
	neopiCompletionsOnce.Do(loadNeoPiCompletionsCatalogs)
	for _, row := range neopiCompletionsRows[provider] {
		if row.ID == id {
			return row.Clone()
		}
	}
	catalog, ok := neopiCompletionsCatalog[provider]
	if !ok {
		return nil
	}
	row, err := neoPiBuildRow(provider, catalog.BaseURL, &neoPiSpec{id: id, name: id, input: []string{"text"}})
	if err != nil {
		return nil
	}
	return row
}

// NeoPiCompletionsModelInfo materializes a NeoPi record as a registered
// model: an openai-images row registers as an image model served through
// /v1/images, an openai-speech row as a speech model served through
// /v1/audio/speech, every other row as a chat model.
func NeoPiCompletionsModelInfo(row *NeoPiModel, ownedBy string) *ModelInfo {
	model := row.Clone()
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: ownedBy, Type: "openai-compatibility",
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	switch model.API {
	case "openai-images":
		info.Type = OpenAIImageModelType
		return info
	case "openai-speech":
		info.Type = OpenAISpeechModelType
		return info
	}
	if model.Reasoning && model.Thinking != nil {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort || model.Thinking.SuppressWhenOff}
	}
	return info
}

// neoPiRunnerAPI reports whether api is a runner transport NeoProxy serves
// for NeoPi rows: image generation or speech synthesis.
func neoPiRunnerAPI(api string) bool {
	return api == "openai-images" || api == "openai-speech"
}

// neoPiServed reports whether NeoProxy serves a NeoPi row: a chat row over
// Chat Completions, or an image or speech row over its runner transport.
// NeoPi keeps rows of other kinds (a rule-classified image or embedding id
// still on the completions transport) out of chat, and no runner serves them
// here.
func neoPiServed(row *NeoPiModel) bool {
	if neoPiRunnerAPI(row.API) {
		return true
	}
	return row.Kind == "" || row.Kind == "chat"
}
