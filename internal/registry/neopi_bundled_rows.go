package registry

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// neoPiBundledRows is an embedded export of NeoPi's own bundled rows for one
// provider, each with the compat NeoPi resolved for the API it is served
// over. It decodes once and hands out clones.
type neoPiBundledRows struct {
	name string
	file []byte
	once sync.Once
	rows []*NeoPiModel
	err  error
}

// neoPiBundledCatalog is the export's shape.
type neoPiBundledCatalog struct {
	Models []struct {
		ID             string          `json:"id"`
		Name           string          `json:"name"`
		API            string          `json:"api"`
		Provider       string          `json:"provider"`
		BaseURL        string          `json:"baseUrl"`
		Reasoning      bool            `json:"reasoning"`
		Input          []string        `json:"input"`
		ContextWindow  int             `json:"contextWindow"`
		MaxTokens      int             `json:"maxTokens"`
		RequestModelID string          `json:"requestModelId"`
		Thinking       json.RawMessage `json:"thinking"`
		Compat         json.RawMessage `json:"compat"`
		Cost           NeoPiCost       `json:"cost"`
	} `json:"models"`
}

// models returns the rows, cloned.
func (b *neoPiBundledRows) models() ([]*NeoPiModel, error) {
	b.once.Do(func() { b.rows, b.err = decodeNeoPiBundledRows(b.name, b.file) })
	if b.err != nil {
		return nil, b.err
	}
	out := make([]*NeoPiModel, 0, len(b.rows))
	for _, row := range b.rows {
		out = append(out, row.Clone())
	}
	return out, nil
}

func decodeNeoPiBundledRows(name string, file []byte) ([]*NeoPiModel, error) {
	reader, err := gzip.NewReader(bytes.NewReader(file))
	if err != nil {
		return nil, fmt.Errorf("registry: %s: %w", name, err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("registry: %s: %w", name, err)
	}
	var catalog neoPiBundledCatalog
	if err = json.Unmarshal(data, &catalog); err != nil {
		return nil, fmt.Errorf("registry: %s: %w", name, err)
	}
	rows := make([]*NeoPiModel, 0, len(catalog.Models))
	for _, entry := range catalog.Models {
		thinking, requiresEffortSet, errThinking := neoPiDecodeThinking(entry.Thinking)
		if errThinking != nil {
			return nil, fmt.Errorf("registry: %s %s: %w", name, entry.ID, errThinking)
		}
		row := &NeoPiModel{ID: entry.ID, Name: entry.Name, API: entry.API, Provider: entry.Provider, BaseURL: entry.BaseURL,
			Reasoning: entry.Reasoning, Input: entry.Input, ContextWindow: entry.ContextWindow, MaxTokens: entry.MaxTokens,
			Thinking: thinking, RequestModelID: entry.RequestModelID, cost: entry.Cost, thinkingRequiresEffortSet: requiresEffortSet}
		// The compat belongs to the row's own API.
		var target any
		switch entry.API {
		case "anthropic-messages":
			row.AnthropicCompat = &NeoPiAnthropicCompat{}
			target = row.AnthropicCompat
		case "openai-completions":
			row.CompletionsCompat = &NeoPiCompletionsCompat{}
			target = row.CompletionsCompat
		case "openai-responses":
			row.Compat = &NeoPiCompat{}
			target = row.Compat
		default:
			return nil, fmt.Errorf("registry: %s %s: unknown api %s", name, entry.ID, entry.API)
		}
		if errCompat := json.Unmarshal(entry.Compat, target); errCompat != nil {
			return nil, fmt.Errorf("registry: %s %s compat: %w", name, entry.ID, errCompat)
		}
		rows = append(rows, row)
	}
	return rows, nil
}
