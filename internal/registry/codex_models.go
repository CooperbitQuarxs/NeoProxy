package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// CodexModel is NeoPi's normalized, account-scoped model specification. Keep
// transport metadata with the route rather than in a provider-wide union.
type CodexModel struct {
	ID               string                        `json:"id"`
	Name             string                        `json:"name"`
	API              string                        `json:"api"`
	Provider         string                        `json:"provider"`
	BaseURL          string                        `json:"baseUrl"`
	AccountAccess    map[string]CodexAccountAccess `json:"accountAccess,omitempty"`
	Reasoning        bool                          `json:"reasoning"`
	Input            []string                      `json:"input"`
	Cost             CodexModelCost                `json:"cost"`
	RemoteCompaction CodexRemoteCompaction         `json:"remoteCompaction"`
	ContextWindow    int                           `json:"contextWindow"`
	MaxContextWindow *int                          `json:"maxContextWindow,omitempty"`
	MaxTokens        int                           `json:"maxTokens"`
	PreferWebsockets bool                          `json:"preferWebsockets,omitempty"`
	UseResponsesLite bool                          `json:"useResponsesLite,omitempty"`
	ToolMode         string                        `json:"toolMode,omitempty"`
	ServiceTiers     *[]string                     `json:"serviceTiers,omitempty"`
	Priority         *float64                      `json:"priority,omitempty"`
	Thinking         *CodexModelThinking           `json:"thinking,omitempty"`
	Compat           *CodexModelCompat             `json:"compat,omitempty"`
	Kind             string                        `json:"kind,omitempty"`
	ModelsETag       string                        `json:"-"`
}

type CodexAccountAccess struct {
	CyberPrograms *[]string `json:"cyberPrograms,omitempty"`
}

type CodexModelCost struct {
	Input       float64               `json:"input"`
	Output      float64               `json:"output"`
	CacheRead   float64               `json:"cacheRead"`
	CacheWrite  float64               `json:"cacheWrite"`
	LongContext *CodexLongContextCost `json:"longContext,omitempty"`
}

type CodexLongContextCost struct {
	InputThreshold int     `json:"inputThreshold"`
	Input          float64 `json:"input"`
	Output         float64 `json:"output"`
	CacheRead      float64 `json:"cacheRead"`
	CacheWrite     float64 `json:"cacheWrite"`
}

type CodexRemoteCompaction struct {
	Enabled            bool   `json:"enabled"`
	API                string `json:"api"`
	V2StreamingEnabled bool   `json:"v2StreamingEnabled"`
}

type CodexModelThinking struct {
	Mode           string   `json:"mode"`
	Efforts        []string `json:"efforts"`
	RequiresEffort bool     `json:"requiresEffort,omitempty"`
}

// These are the source policies used by the Codex request transformer. Other
// provider APIs do not reinterpret these fields.
type CodexModelCompat struct {
	SupportsReasoningSummary             bool `json:"supportsReasoningSummary"`
	SupportsAllTurnsReasoningContext     bool `json:"supportsAllTurnsReasoningContext"`
	SupportsConfigurationUpdate          bool `json:"supportsConfigurationUpdate"`
	SupportsSteering                     bool `json:"supportsSteering"`
	RequiresReasoningOffJuiceInstruction bool `json:"requiresReasoningOffJuiceInstruction"`
	SupportsSamplingParams               bool `json:"supportsSamplingParams"`
	IncludeEncryptedReasoning            bool `json:"includeEncryptedReasoning"`
}

//go:embed models/codex_neopi.json
var codexBundledJSON []byte

var codexBundledModels = func() []CodexModel {
	var models []CodexModel
	if err := json.Unmarshal(codexBundledJSON, &models); err != nil {
		panic(fmt.Errorf("invalid embedded NeoPi Codex catalog: %w", err))
	}
	return models
}()

// BundledCodexModelIDs is also NeoPi's safety gate for worker-mode aliases.
func BundledCodexModelIDs() map[string]bool {
	ids := make(map[string]bool, len(codexBundledModels))
	for _, model := range codexBundledModels {
		ids[model.ID] = true
	}
	return ids
}

// codexBundledNames maps bundled ids to their display names (keepStaticNames).
var codexBundledNames = func() map[string]string {
	names := make(map[string]string, len(codexBundledModels))
	for _, model := range codexBundledModels {
		names[model.ID] = model.Name
	}
	return names
}()

// GetCodexModels is the NeoPi fallback used only before live discovery succeeds
// or while discovery is unavailable. It is not a subscription-plan allowlist.
func GetCodexModels() []*ModelInfo {
	models := make([]*ModelInfo, 0, len(codexBundledModels))
	for _, model := range codexBundledModels {
		models = append(models, model.ModelInfo())
	}
	return models
}

func (model *CodexModel) Clone() *CodexModel {
	if model == nil {
		return nil
	}
	cloned := *model
	cloned.Input = slices.Clone(model.Input)
	if model.Cost.LongContext != nil {
		cost := *model.Cost.LongContext
		cloned.Cost.LongContext = &cost
	}
	if model.MaxContextWindow != nil {
		value := *model.MaxContextWindow
		cloned.MaxContextWindow = &value
	}
	if model.Priority != nil {
		value := *model.Priority
		cloned.Priority = &value
	}
	if model.ServiceTiers != nil {
		tiers := slices.Clone(*model.ServiceTiers)
		cloned.ServiceTiers = &tiers
	}
	cloned.AccountAccess = maps.Clone(model.AccountAccess)
	for id, access := range cloned.AccountAccess {
		if access.CyberPrograms != nil {
			programs := slices.Clone(*access.CyberPrograms)
			access.CyberPrograms = &programs
			cloned.AccountAccess[id] = access
		}
	}
	if model.Thinking != nil {
		thinking := *model.Thinking
		thinking.Efforts = slices.Clone(thinking.Efforts)
		cloned.Thinking = &thinking
	}
	if model.Compat != nil {
		compat := *model.Compat
		cloned.Compat = &compat
	}
	return &cloned
}

// The Codex deployment uses OpenAI's taxonomy, including bare o-series
// product names (no revision) and the reviewed, exact Daybreak aliases.
func codexModelIdentity(id string) (family string, revision [3]int, hasRevision, openai bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	bare := id[strings.LastIndexByte(id, '/')+1:]
	oSeries := len(bare) >= 2 && bare[0] == 'o' && strings.ContainsRune("134", rune(bare[1])) &&
		(len(bare) == 2 || bare[2] == '-' || bare[2] == '.')
	switch {
	case strings.HasPrefix(bare, "gpt-"):
		family, openai = "gpt", true
	case strings.HasPrefix(bare, "chatgpt-"):
		family, openai = "chatgpt", true
	case strings.HasPrefix(bare, "codex-"):
		family, openai = "codex", true
	case oSeries:
		family, openai = "o-series", true
	}
	if !openai {
		for part := range strings.FieldsFuncSeq(id, func(r rune) bool { return r == '/' || r == '.' || r == ':' }) {
			if strings.HasPrefix(part, "openai") && (len(part) == 6 || strings.ContainsRune("-_.:0123456789", rune(part[6]))) {
				openai = true
				break
			}
		}
	}
	if openai && strings.Contains(bare, "codex") {
		family = "codex"
		if strings.Contains(bare, "codex-spark") {
			family = "codex-spark"
		}
	}
	switch bare {
	case "daybreak-blue-latest", "daybreak-red-latest", "gpt-daybreak-blue-latest", "gpt-daybreak-red-latest":
		return family, [3]int{5, 6, 0}, true, openai
	}
	if !openai || (oSeries && len(bare) == 2) {
		return
	}
	tail := ""
	switch {
	case strings.HasPrefix(bare, "chatgpt-"):
		tail = bare[len("chatgpt-"):]
	case strings.Contains(bare, "gpt-"):
		tail = bare[strings.Index(bare, "gpt-")+len("gpt-"):]
	case strings.HasPrefix(bare, "o"):
		tail = bare[1:]
	}
	for len(tail) > 0 && (tail[0] < '0' || tail[0] > '9') {
		tail = tail[1:]
	}
	for component := range revision {
		digits, value := 0, 0
		for digits < len(tail) && tail[digits] >= '0' && tail[digits] <= '9' {
			value = value*10 + int(tail[digits]-'0')
			if value > 255 {
				return
			}
			digits++
		}
		if digits == 0 || (digits < len(tail) && tail[digits] >= 'a' && tail[digits] <= 'z') {
			return
		}
		revision[component], hasRevision = value, true
		if digits+1 >= len(tail) || (tail[digits] != '.' && tail[digits] != '-') || tail[digits+1] < '0' || tail[digits+1] > '9' {
			return
		}
		tail = tail[digits+1:]
	}
	return
}

func codexMandatoryThinking(id string) bool {
	id = strings.ToLower(id)
	isToken := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
	for _, token := range []string{"-thinking", "-reasoning", "-reasoner"} {
		for from := 0; from < len(id); {
			found := strings.Index(id[from:], token)
			if found < 0 {
				break
			}
			index := from + found
			end, start := index+len(token), index
			for start > 0 && isToken(id[start-1]) {
				start--
			}
			previous := id[start:index]
			if (end == len(id) || !isToken(id[end])) && previous != "non" && previous != "no" {
				return true
			}
			from = index + 1
		}
	}
	return false
}

// ModelInfo materializes the relevant NeoPi openai lineage and openai-codex
// provider rules without consulting CLIProxyAPI's static subscription tiers.
func (model CodexModel) ModelInfo() *ModelInfo {
	resolved := model.Clone()
	// openai-codex declares keep-bundled-names: a discovered row whose id is in
	// the bundled catalog keeps the bundled display name ("GPT-6.1 Sol", not
	// the backend's "GPT-6.1-Sol").
	if name, ok := codexBundledNames[model.ID]; ok {
		resolved.Name = name
	}
	if resolved.Compat == nil {
		resolved.Compat = &CodexModelCompat{SupportsReasoningSummary: true, SupportsSamplingParams: true, IncludeEncryptedReasoning: true}
	}
	family, revision, hasRevision, openai := codexModelIdentity(model.ID)
	major, minor := revision[0], revision[1]
	atLeast := func(wantMajor, wantMinor int) bool {
		return hasRevision && (major > wantMajor || (major == wantMajor && minor >= wantMinor))
	}
	if model.Thinking == nil && model.Reasoning {
		var efforts []string
		switch {
		case openai && atLeast(5, 6):
			efforts = []string{"low", "medium", "high", "xhigh", "max"}
		case openai && atLeast(5, 2):
			efforts = []string{"low", "medium", "high", "xhigh"}
		case revision == [3]int{5, 1, 0} && family == "codex" && strings.Contains(model.ID, "codex-mini"):
			efforts = []string{"medium", "high"}
		case hasRevision && (family == "gpt" || family == "codex"):
			efforts = []string{"minimal", "low", "medium", "high"}
		default:
			efforts = []string{"minimal", "low", "medium", "high", "xhigh"}
		}
		resolved.Thinking = &CodexModelThinking{Mode: "effort", Efforts: efforts,
			RequiresEffort: family == "o-series" || codexMandatoryThinking(model.ID)}
	}
	if family == "o-series" {
		resolved.Compat.SupportsSamplingParams = false
	}
	if hasRevision {
		resolved.Compat.SupportsAllTurnsReasoningContext = atLeast(5, 4)
		resolved.Compat.RequiresReasoningOffJuiceInstruction = atLeast(5, 6)
		if openai {
			if major >= 5 {
				resolved.Compat.SupportsSamplingParams = false
			}
			resolved.Compat.SupportsReasoningSummary = atLeast(5, 4)
			resolved.Compat.SupportsConfigurationUpdate = major == 6
			resolved.Compat.SupportsSteering = major >= 6
			if family == "codex" && major >= 1 {
				resolved.ContextWindow = 272000
			}
			if family == "gpt" && revision == [3]int{5, 4, 0} {
				priority := float64(0)
				if strings.Contains(model.ID, "-mini") {
					priority, resolved.ContextWindow = 1, 272000
				}
				if strings.Contains(model.ID, "-nano") {
					priority, resolved.ContextWindow = 2, 272000
				}
				resolved.Priority = &priority
			}
		}
	}
	canonical := strings.TrimSuffix(model.ID, "-wm")
	switch canonical {
	case "gpt-6-astra":
		resolved.ContextWindow = 272000
		resolved.Cost = CodexModelCost{Input: 10, Output: 50, CacheRead: 1}
	case "gpt-6.1-sol":
		resolved.ContextWindow = 500000
		resolved.Cost = CodexModelCost{Input: 2, Output: 10, CacheRead: 0.1}
	case "gpt-6-sol":
		resolved.Cost = CodexModelCost{Input: 2, Output: 10, CacheRead: 0.2}
	case "gpt-6-luna":
		resolved.Cost = CodexModelCost{Input: 0.1, Output: 0.5, CacheRead: 0.01}
	case "gpt-daybreak-blue-latest":
		resolved.Cost = CodexModelCost{Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25}
	case "gpt-daybreak-red-latest":
		resolved.Cost = CodexModelCost{Input: 12.5, Output: 75, CacheRead: 1.25, CacheWrite: 15.625}
	}
	if resolved.MaxContextWindow == nil && (canonical == "gpt-6-astra" || canonical == "gpt-6.1-sol") {
		ceiling := 922000
		resolved.MaxContextWindow = &ceiling
	}
	switch {
	case strings.HasPrefix(canonical, "gpt-5.6-luna"):
		resolved.Cost.LongContext = &CodexLongContextCost{272000, 0.4, 1.8, 0.04, 0.5}
	case canonical == "gpt-5.6" || strings.HasPrefix(canonical, "gpt-5.6-sol"):
		resolved.Cost.LongContext = &CodexLongContextCost{272000, 10, 45, 1, 12.5}
	case strings.HasPrefix(canonical, "gpt-5.6-terra"):
		resolved.Cost.LongContext = &CodexLongContextCost{272000, 4, 18, 0.4, 5}
	}
	info := &ModelInfo{ID: model.ID, Object: "model", OwnedBy: "openai", Type: "codex", Name: model.ID, DisplayName: resolved.Name,
		ContextLength: resolved.ContextWindow, MaxCompletionTokens: resolved.MaxTokens, SupportedInputModalities: slices.Clone(model.Input),
		SupportConfigurationUpdate: resolved.Compat.SupportsConfigurationUpdate, Codex: resolved,
		NativeCapabilities: &NativeCapabilities{WebSearch: boolPointer(true)}}
	if resolved.MaxContextWindow != nil {
		info.MaxContextLength = *resolved.MaxContextWindow
	}
	if resolved.ServiceTiers != nil {
		info.ServiceTiers = slices.Clone(*resolved.ServiceTiers)
	}
	if resolved.Thinking != nil {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(resolved.Thinking.Efforts), ZeroAllowed: !resolved.Thinking.RequiresEffort}
	}
	if model.Kind == "image" || model.ID == "gpt-image-2" {
		info.Type = OpenAIImageModelType
		info.SupportedOutputModalities = []string{"image"}
	}
	return info
}
