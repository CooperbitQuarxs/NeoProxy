package registry

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopicatalog"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// NeoPi's catalog facilities discovery mappers share: the api-routes rules
// (compat/behavior.ts apiRouteFor) and the global bundled-reference fallback
// (provider-models/bundled-references.ts createReferenceResolver). Both are
// exported from NeoPi by testdata/export_neopi_global_references.ts.

//go:embed models/neopi_discovery/api_routes.json
var neoPiAPIRoutesJSON []byte

//go:embed models/neopi_discovery/global_references.json.gz
var neoPiGlobalReferencesGz []byte

type neoPiRouteMatch struct {
	Exact     []string `json:"exact"`
	Prefix    []string `json:"prefix"`
	Substring []string `json:"substring"`
	Token     []string `json:"token"`
	Glob      []string `json:"glob"`
}

type neoPiAPIRouteTable struct {
	Provider string `json:"provider"`
	Routes   []struct {
		API         string          `json:"api"`
		Match       neoPiRouteMatch `json:"match"`
		StripPrefix bool            `json:"stripPrefix"`
	} `json:"routes"`
	Default string `json:"default"`
}

type neoPiGlobalReferenceRow struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	API            string          `json:"api"`
	Provider       string          `json:"provider"`
	Reasoning      bool            `json:"reasoning"`
	Input          []string        `json:"input"`
	ContextWindow  int             `json:"contextWindow"`
	MaxTokens      int             `json:"maxTokens"`
	RequestModelID string          `json:"requestModelId"`
	Thinking       json.RawMessage `json:"thinking"`
	Cost           NeoPiCost       `json:"cost"`
}

var (
	neoPiReferencesOnce sync.Once
	neoPiAPIRoutes      []neoPiAPIRouteTable
	neoPiGlobalRefs     map[string]*NeoPiModel
	neoPiReferencesErr  error
)

func loadNeoPiReferences() {
	if err := json.Unmarshal(neoPiAPIRoutesJSON, &neoPiAPIRoutes); err != nil {
		neoPiReferencesErr = fmt.Errorf("registry: api_routes.json: %w", err)
		return
	}
	reader, err := gzip.NewReader(bytes.NewReader(neoPiGlobalReferencesGz))
	if err != nil {
		neoPiReferencesErr = err
		return
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		neoPiReferencesErr = err
		return
	}
	var file struct {
		References []neoPiGlobalReferenceRow `json:"references"`
	}
	if err = json.Unmarshal(raw, &file); err != nil {
		neoPiReferencesErr = fmt.Errorf("registry: global_references.json.gz: %w", err)
		return
	}
	neoPiGlobalRefs = make(map[string]*NeoPiModel, len(file.References))
	for _, ref := range file.References {
		thinking, requiresEffortSet, errThinking := neoPiDecodeThinking(ref.Thinking)
		if errThinking != nil {
			neoPiReferencesErr = fmt.Errorf("registry: global reference %s: %w", ref.ID, errThinking)
			return
		}
		neoPiGlobalRefs[ref.ID] = &NeoPiModel{ID: ref.ID, Name: ref.Name, API: ref.API, Provider: ref.Provider, Reasoning: ref.Reasoning,
			Input: ref.Input, ContextWindow: ref.ContextWindow, MaxTokens: ref.MaxTokens, RequestModelID: ref.RequestModelID,
			Thinking: thinking, thinkingRequiresEffortSet: requiresEffortSet, cost: ref.Cost}
	}
}

// neoPiAPIRouteFor is apiRouteFor: the API the provider's route table sends
// model on, matched in declaration order (exact, prefix and substring on the
// id as given; token and glob on its lowercase form), else the table's
// default; empty when the provider has no table.
func neoPiAPIRouteFor(provider, model string) (string, error) {
	neoPiReferencesOnce.Do(loadNeoPiReferences)
	if neoPiReferencesErr != nil {
		return "", neoPiReferencesErr
	}
	lower := strings.ToLower(model)
	for _, table := range neoPiAPIRoutes {
		if table.Provider != provider {
			continue
		}
		for _, route := range table.Routes {
			if neoPiRouteMatches(route.Match, model, lower) {
				return route.API, nil
			}
		}
		return table.Default, nil
	}
	return "", nil
}

// neoPiRouteMatches is matchesList.
func neoPiRouteMatches(match neoPiRouteMatch, model, lower string) bool {
	if slices.Contains(match.Exact, model) {
		return true
	}
	for _, prefix := range match.Prefix {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	for _, sub := range match.Substring {
		if strings.Contains(model, sub) {
			return true
		}
	}
	if len(match.Token) > 0 {
		parts := strings.FieldsFunc(lower, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
		for _, token := range match.Token {
			if slices.Contains(parts, token) {
				return true
			}
		}
	}
	for _, pattern := range match.Glob {
		if neopicatalog.GlobMatch(pattern, lower) {
			return true
		}
	}
	return false
}

// neoPiGlobalReference is createReferenceResolver's global fallback for an
// id: the bundled row of a bare-id reference provider NeoPi picks for it, as
// a spec-stage row (its own provider kept), or nil.
func neoPiGlobalReference(id string) (*NeoPiModel, error) {
	neoPiReferencesOnce.Do(loadNeoPiReferences)
	if neoPiReferencesErr != nil {
		return nil, neoPiReferencesErr
	}
	if ref := neoPiGlobalRefs[id]; ref != nil {
		return ref.Clone(), nil
	}
	return nil, nil
}

// neoPiCollator is the root collation String.prototype.localeCompare orders
// ids by; a Collator is not safe for concurrent use.
var neoPiCollator = struct {
	sync.Mutex
	*collate.Collator
}{Collator: collate.New(language.Und)}

// neoPiSortStableByLocale is a stable sort of items by key under
// String.prototype.localeCompare.
func neoPiSortStableByLocale[T any](items []T, key func(T) string) {
	neoPiCollator.Lock()
	defer neoPiCollator.Unlock()
	sort.SliceStable(items, func(i, j int) bool { return neoPiCollator.CompareString(key(items[i]), key(items[j])) < 0 })
}
