package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const ModelsClientVersion = "0.159.0"
const BackendURL = "https://chatgpt.com/backend-api"

type ModelDiscoveryOptions struct {
	AccessToken   string
	AccountID     string
	BaseURL       string
	ClientVersion string
}

var codexModelVersionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// ModelCatalog distinguishes an authoritative empty roster and a rejected
// credential from a transient failure. ETag is metadata, not If-None-Match:
// NeoPi does not condition its model-list requests on the previous ETag.
type ModelCatalog struct {
	Models         []registry.CodexModel `json:"models"`
	ETag           string                `json:"etag,omitempty"`
	RejectedStatus int                   `json:"rejectedStatus,omitempty"`
	Raw            json.RawMessage       `json:"-"` // unmodified upstream document for the catalog export utility
}

// FetchModels ports NeoPi's fetchCodexModels. It never refreshes credentials.
// The caller owns cancellation and retains its last roster on a transient error.
func FetchModels(ctx context.Context, client *http.Client, options ModelDiscoveryOptions) (*ModelCatalog, error) {
	token, accountID := options.AccessToken, options.AccountID
	version := strings.TrimSpace(options.ClientVersion)
	if !codexModelVersionPattern.MatchString(version) {
		version = ModelsClientVersion
	}
	base := strings.TrimRight(strings.TrimSpace(options.BaseURL), "/")
	if base == "" {
		base = BackendURL
	}
	var lastErr error
	for _, path := range []string{"/codex/models", "/models"} {
		endpoint, errURL := url.Parse(base + path)
		if errURL != nil {
			return nil, fmt.Errorf("Codex models URL: %w", errURL)
		}
		query := endpoint.Query()
		query.Set("client_version", version)
		endpoint.RawQuery = query.Encode()
		request, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if errRequest != nil {
			return nil, fmt.Errorf("Codex models request: %w", errRequest)
		}
		codexFetchHeaders(request)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		if strings.TrimSpace(accountID) != "" {
			request.Header.Set("Chatgpt-Account-Id", accountID)
		}
		request.Header.Set("OpenAI-Beta", "responses=experimental")
		request.Header.Set("Originator", "omp")
		request.Header.Set("Version", version)
		response, errResponse := client.Do(request)
		if errResponse != nil {
			lastErr = errResponse
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		status := response.StatusCode
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			closeCodexModelBody(response.Body)
			return &ModelCatalog{Models: []registry.CodexModel{}, RejectedStatus: status}, nil
		}
		if status < 200 || status >= 300 {
			closeCodexModelBody(response.Body)
			lastErr = fmt.Errorf("Codex models returned HTTP %d", status)
			continue
		}
		body, errDecode := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		if errDecode != nil {
			lastErr = errDecode
			continue
		}
		data, errRead := io.ReadAll(body)
		closeCodexModelBody(body)
		if errRead != nil {
			lastErr = errRead
			continue
		}
		models, errModels := normalizeCodexModels(data, base, accountID)
		if errModels != nil {
			lastErr = errModels
			continue
		}
		return &ModelCatalog{Models: models, ETag: strings.TrimSpace(response.Header.Get("ETag")), Raw: data}, nil
	}
	return nil, fmt.Errorf("Codex model discovery failed: %w", lastErr)
}

func closeCodexModelBody(body io.Closer) {
	if errClose := body.Close(); errClose != nil {
		log.WithError(errClose).Warn("closing Codex models response")
	}
}

func normalizeCodexModels(data []byte, base, account string) ([]registry.CodexModel, error) {
	var payload map[string]any
	if errJSON := json.Unmarshal(data, &payload); errJSON != nil || payload == nil {
		return nil, fmt.Errorf("invalid Codex model-list object")
	}
	for _, key := range []string{"models", "data"} {
		if value, exists := payload[key]; exists {
			if _, ok := value.([]any); !ok {
				return nil, fmt.Errorf("invalid Codex model-list %s", key)
			}
		}
	}
	entries, ok := payload["models"].([]any)
	if !ok {
		entries, _ = payload["data"].([]any)
	}
	parsed := make([]registry.CodexModel, 0, len(entries))
	advertised := make(map[string]bool, len(entries))
	for _, entry := range entries {
		row, okRow := entry.(map[string]any)
		if !okRow {
			continue
		}
		model, okModel := parseCodexModel(row, base, account)
		if !okModel {
			continue
		}
		parsed = append(parsed, model)
		advertised[model.ID] = true
	}
	bundled := registry.BundledCodexModelIDs()
	models := make([]registry.CodexModel, 0, len(parsed))
	for _, model := range parsed {
		canonical := model.ID
		plain, worker := strings.CutSuffix(model.ID, "-wm")
		if worker && bundled[plain] {
			canonical = plain
		}
		if model.ContextWindow < 0 {
			model.ContextWindow = 272000
			if canonical == "gpt-5.6" || strings.HasPrefix(canonical, "gpt-5.6-") || strings.HasPrefix(canonical, "gpt-5.6.") {
				model.ContextWindow = 372000
			}
		}
		switch canonical {
		case "gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra":
			model.ContextWindow = max(model.ContextWindow, 1000000)
		}
		model.MaxTokens = min(128000, model.ContextWindow)
		models = append(models, model)
		if canonical != model.ID && !advertised[canonical] {
			model.ID = canonical
			models = append(models, model)
		}
	}
	// localeCompare in NeoPi uses the default ICU collation, not byte ordering.
	ordering := collate.New(language.English)
	slices.SortStableFunc(models, func(left, right registry.CodexModel) int {
		lp, rp := float64(9007199254740991), float64(9007199254740991)
		if left.Priority != nil {
			lp = *left.Priority
		}
		if right.Priority != nil {
			rp = *right.Priority
		}
		if lp < rp {
			return -1
		}
		if lp > rp {
			return 1
		}
		return ordering.CompareString(left.ID, right.ID)
	})
	return models, nil
}

func parseCodexModel(row map[string]any, base, account string) (registry.CodexModel, bool) {
	id := codexModelString(row["slug"])
	if id == "" {
		id = codexModelString(row["id"])
	}
	visibility := strings.ToLower(codexModelString(row["visibility"]))
	if id == "" || visibility == "hide" || visibility == "hidden" {
		return registry.CodexModel{}, false
	}
	model := registry.CodexModel{ID: id, Name: codexModelString(row["display_name"]), API: "openai-codex-responses", Provider: "openai-codex", BaseURL: base, ContextWindow: -1}
	if model.Name == "" {
		model.Name = id
	}
	model.RemoteCompaction.Enabled = true
	model.RemoteCompaction.API = model.API
	model.RemoteCompaction.V2StreamingEnabled = true
	if value := codexPositiveModelInt(row["context_window"]); value != nil {
		model.ContextWindow = *value
	}
	model.MaxContextWindow = codexPositiveModelInt(row["max_context_window"])
	if priority, ok := row["priority"].(float64); ok && !math.IsInf(priority, 0) && !math.IsNaN(priority) && priority != 9007199254740991 {
		model.Priority = &priority
	}
	level := strings.ToLower(codexModelString(row["default_reasoning_level"]))
	model.Reasoning = level != "" && level != "none"
	if levels, ok := row["supported_reasoning_levels"].([]any); ok {
		for _, value := range levels {
			if preset, okPreset := value.(map[string]any); okPreset {
				effort := strings.ToLower(codexModelString(preset["effort"]))
				model.Reasoning = model.Reasoning || (effort != "" && effort != "none")
			}
		}
	}
	var text, image bool
	if modalities, ok := row["input_modalities"].([]any); ok {
		for _, value := range modalities {
			switch strings.ToLower(codexModelString(value)) {
			case "text":
				text = true
			case "image":
				image = true
			}
		}
	}
	if !text && !image {
		text, image = true, true
	}
	if text {
		model.Input = append(model.Input, "text")
	}
	if image {
		model.Input = append(model.Input, "image")
	}
	model.PreferWebsockets, _ = row["prefer_websockets"].(bool)
	model.UseResponsesLite, _ = row["use_responses_lite"].(bool)
	if row["tool_mode"] == "code_mode_only" {
		model.ToolMode = "code_mode_only"
	}
	if tiers, ok := row["service_tiers"].([]any); ok {
		ids := make([]string, 0, len(tiers))
		for _, value := range tiers {
			if tier, okTier := value.(map[string]any); okTier {
				id := codexModelString(tier["id"])
				if id != "" && !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
		}
		model.ServiceTiers = &ids
	}
	if strings.TrimSpace(account) != "" {
		access := registry.CodexAccountAccess{}
		if programs, ok := row["available_access_programs"].(map[string]any); ok {
			if cyber, okCyber := programs["cyber"].([]any); okCyber {
				ids := make([]string, 0, len(cyber))
				for _, value := range cyber {
					if id := codexModelString(value); id != "" {
						ids = append(ids, id)
					}
				}
				access.CyberPrograms = &ids
			}
		}
		model.AccountAccess = map[string]registry.CodexAccountAccess{account: access}
	}
	return model, true
}

func codexModelString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func codexPositiveModelInt(value any) *int {
	number, ok := value.(float64)
	if !ok || number <= 0 || math.IsInf(number, 0) || math.IsNaN(number) {
		return nil
	}
	integer := int(math.Trunc(number))
	return &integer
}
