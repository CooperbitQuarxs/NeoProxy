package registry

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
)

// factoryDroidGz is NeoPi's Factory Droid registry as its provider resolves
// it, exported by runtime/executor/testdata/capture_factory_droid_npi_profiles.ts:
// each model's account-independent build and, per upstream of its rotation,
// the route NeoPi's provider resolves per request.
//
//go:embed models/factory_droid_neopi.json.gz
var factoryDroidGz []byte

// FactoryDroidProvider is NeoPi's provider id for Factory Droid.
const FactoryDroidProvider = "factory-droid"

// FactoryDroidAPI is NeoPi's api for a Factory Droid row.
const FactoryDroidAPI = "factory-droid-agent"

// NeoPiFactoryDroid is the account scope a factory-droid-agent row carries:
// its organization, Standard Credits rate, upstream rotation and how live
// routing chose it.
type NeoPiFactoryDroid struct {
	Wire          string   `json:"wire"`
	OrgID         string   `json:"orgId,omitempty"`
	Credits       *float64 `json:"credits,omitempty"`
	APIProviders  []string `json:"apiProviders,omitempty"`
	RoutingSource string   `json:"routingSource,omitempty"`
}

// FactoryDroidLimits is FactoryDroidLimits: native token limits, nil for
// none.
type FactoryDroidLimits struct {
	ContextWindow *int `json:"contextWindow"`
	MaxTokens     *int `json:"maxTokens"`
}

// FactoryDroidEntitlement is FactoryDroidEntitlement: the account gates on
// one registry model.
type FactoryDroidEntitlement struct {
	FeatureFlag           string `json:"featureFlag,omitempty"`
	DeprecationFlag       string `json:"deprecationFlag,omitempty"`
	RequiresExplicitOptIn bool   `json:"requiresExplicitOptIn"`
	BaseVariant           string `json:"baseVariant,omitempty"`
}

// FactoryDroidPolicy is FactoryDroidModelPolicy: the registry facts for one
// Factory model.
type FactoryDroidPolicy struct {
	Wire            string                  `json:"wire"`
	Rotation        []string                `json:"rotation"`
	RegionUpstreams map[string][]string     `json:"regionUpstreams"`
	Limits          FactoryDroidLimits      `json:"limits"`
	EULimits        FactoryDroidLimits      `json:"euLimits"`
	RoutingFamily   string                  `json:"routingFamily,omitempty"`
	PolicyAliases   []string                `json:"policyAliases"`
	Entitlement     FactoryDroidEntitlement `json:"entitlement"`
	// DefaultReasoningOff: without a caller effort the native default is
	// thinking off.
	DefaultReasoningOff bool `json:"defaultReasoningOff"`
	// Image marks a generate_image model, gated by the org's image settings.
	Image bool `json:"image"`
}

// FactoryDroidRoute is resolveRoute for one upstream: the request dialect,
// the catalog axes, and the shared-transport compat and thinking (absent on
// the native Gemini wire).
type FactoryDroidRoute struct {
	Request   map[string]any  `json:"request"`
	Catalog   map[string]any  `json:"catalog"`
	Compat    json.RawMessage `json:"compat,omitempty"`
	Thinking  *NeoPiThinking  `json:"thinking,omitempty"`
	Reasoning bool            `json:"reasoning"`
}

// FactoryDroidModel is one registry model: its account-independent build
// and its routes.
type FactoryDroidModel struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     []string       `json:"input"`
	Cost      NeoPiCost      `json:"cost"`
	Reasoning bool           `json:"reasoning"`
	Thinking  *NeoPiThinking `json:"thinking,omitempty"`
	Credits   *float64       `json:"credits,omitempty"`
	// QuotaTier is the subscription pool the model bills (quota-tiers
	// provider="factory-droid"): "core" or "standard".
	QuotaTier string                       `json:"quotaTier,omitempty"`
	Policy    FactoryDroidPolicy           `json:"policy"`
	Routes    map[string]FactoryDroidRoute `json:"routes"`
}

var factoryDroidRegistry = sync.OnceValue(func() []FactoryDroidModel {
	reader, err := gzip.NewReader(bytes.NewReader(factoryDroidGz))
	if err != nil {
		panic(fmt.Sprintf("registry: factory_droid_neopi.json.gz: %v", err))
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		panic(fmt.Sprintf("registry: factory_droid_neopi.json.gz: %v", err))
	}
	var export struct {
		Models []FactoryDroidModel `json:"models"`
	}
	if err = json.Unmarshal(data, &export); err != nil {
		panic(fmt.Sprintf("registry: factory_droid_neopi.json.gz: %v", err))
	}
	return export.Models
})

// FactoryDroidRegistry is the registry roster in declaration order.
func FactoryDroidRegistry() []FactoryDroidModel { return factoryDroidRegistry() }

// FactoryDroidModelFor is resolveFactoryDroidPolicy's lookup: the registry
// model for an id; nil outside the registry.
func FactoryDroidModelFor(id string) *FactoryDroidModel {
	for i, model := range factoryDroidRegistry() {
		if model.ID == id {
			return &factoryDroidRegistry()[i]
		}
	}
	return nil
}

// FactoryDroidPoolModels are the registry models a subscription pool bills
// (quota-tiers provider="factory-droid"), in registry order.
func FactoryDroidPoolModels(tier string) []string {
	var out []string
	for _, model := range factoryDroidRegistry() {
		if model.QuotaTier == tier {
			out = append(out, model.ID)
		}
	}
	return out
}

// FactoryDroidCanonicalPolicyID is canonicalPolicyId: the registry id an
// organization policy id (or alias) names, else the id itself.
func FactoryDroidCanonicalPolicyID(id string) string {
	for _, model := range factoryDroidRegistry() {
		if model.ID == id || slices.Contains(model.Policy.PolicyAliases, id) {
			return model.ID
		}
	}
	return id
}

// FactoryDroidRotation is resolveFactoryDroidRotation: the registry order
// narrowed to the upstreams the inference region serves.
func FactoryDroidRotation(policy FactoryDroidPolicy, region string) []string {
	key := "global"
	if region == "eu" || region == "us" {
		key = region
	}
	eligible := policy.RegionUpstreams[key]
	out := []string{}
	for _, upstream := range policy.Rotation {
		if slices.Contains(eligible, upstream) {
			out = append(out, upstream)
		}
	}
	return out
}

// FactoryDroidRegionalLimits is factoryDroidRegionalLimits: EU overrides
// fall back to the registry defaults.
func FactoryDroidRegionalLimits(policy FactoryDroidPolicy, region string) FactoryDroidLimits {
	if region != "eu" {
		return policy.Limits
	}
	limits := policy.Limits
	if policy.EULimits.ContextWindow != nil {
		limits.ContextWindow = policy.EULimits.ContextWindow
	}
	if policy.EULimits.MaxTokens != nil {
		limits.MaxTokens = policy.EULimits.MaxTokens
	}
	return limits
}

// FactoryDroidScope is an account's scope: residency, inference region and
// Factory organization.
type FactoryDroidScope struct {
	Region          string
	InferenceRegion string
	OrgID           string
}

// FactoryDroidWireBaseURL is factoryDroidWireBaseUrl.
func FactoryDroidWireBaseURL(wire, region string) string {
	host := "https://api.factory.ai"
	if region == "eu" {
		host = "https://api.eu.factory.ai"
	}
	switch wire {
	case "anthropic-messages":
		return host + "/api/llm/a"
	case "google-generate":
		return host + "/api/llm/g/v1"
	default:
		return host + "/api/llm/o/v1"
	}
}

// BuildFactoryDroidModel is buildFactoryDroidModel: the account-scoped row
// for a registry model, on the wire host of the account's residency, with
// the inference region's limits and the resolved upstream rotation.
func BuildFactoryDroidModel(model *FactoryDroidModel, scope FactoryDroidScope, inferenceRegion string, apiProviders []string) *NeoPiModel {
	limits := FactoryDroidRegionalLimits(model.Policy, inferenceRegion)
	row := &NeoPiModel{
		ID:        model.ID,
		Name:      model.Name,
		API:       FactoryDroidAPI,
		Provider:  FactoryDroidProvider,
		BaseURL:   FactoryDroidWireBaseURL(model.Policy.Wire, scope.Region),
		Reasoning: model.Reasoning,
		Input:     slices.Clone(model.Input),
		Thinking:  model.Thinking.Clone(),
		cost:      model.Cost,
		FactoryDroid: &NeoPiFactoryDroid{
			Wire:         model.Policy.Wire,
			OrgID:        scope.OrgID,
			Credits:      model.Credits,
			APIProviders: slices.Clone(apiProviders),
		},
	}
	if limits.ContextWindow != nil {
		row.ContextWindow = *limits.ContextWindow
	}
	if limits.MaxTokens != nil {
		row.MaxTokens = *limits.MaxTokens
	}
	if len(row.FactoryDroid.APIProviders) == 0 {
		row.FactoryDroid.APIProviders = nil
	}
	return row
}

// FactoryDroidSettings is what discovery learned about an account: its
// Statsig gates, its organization's model policy and image settings, and
// live provider routing. A nil field is unknown.
type FactoryDroidSettings struct {
	Flags   map[string]bool
	Policy  *FactoryDroidOrgPolicy
	Images  *FactoryDroidImageSettings
	Routing *FactoryDroidRouting
}

// FactoryDroidOrgPolicy is FactoryModelPolicy: the organization policy
// subset that gates models, ids canonicalized to the registry.
type FactoryDroidOrgPolicy struct {
	AllowAllFactoryModels        *bool
	AllowedModelIDs              []string
	BlockedModelIDs              []string
	RequireExplicitOptInModelIDs []string
	IsFastModelsAllowed          *bool
}

// FactoryDroidImageSettings is FactoryImageSettings.
type FactoryDroidImageSettings struct {
	Enabled         bool
	AllowedModelIDs []string
	allowedSet      bool
}

// FactoryDroidRouting is FactoryProviderRouting: live per-model rotations.
type FactoryDroidRouting struct {
	Models           map[string][]string
	Defaults         map[string][]string
	BlockedProviders map[string][]string
}

// factoryDroidAvailable is isModelAvailable: feature flags first, then org
// model policy; image models also need the org's image settings.
func factoryDroidAvailable(model *FactoryDroidModel, settings FactoryDroidSettings, region string) bool {
	gates := model.Policy.Entitlement
	if len(FactoryDroidRotation(model.Policy, region)) == 0 {
		return false
	}
	images := settings.Images
	if model.Policy.Image && (images == nil || !images.Enabled || (images.allowedSet && !slices.Contains(images.AllowedModelIDs, model.ID))) {
		return false
	}
	if gates.FeatureFlag != "" && !settings.Flags[gates.FeatureFlag] {
		return false
	}
	if gates.DeprecationFlag != "" && settings.Flags[gates.DeprecationFlag] {
		return false
	}
	policy := settings.Policy
	if gates.BaseVariant != "" && policy != nil && policy.IsFastModelsAllowed != nil && !*policy.IsFastModelsAllowed {
		return false
	}
	if (policy != nil && slices.Contains(policy.RequireExplicitOptInModelIDs, model.ID)) || (policy == nil && gates.RequiresExplicitOptIn) {
		return false
	}
	allowlist := policy != nil && ((policy.AllowAllFactoryModels != nil && !*policy.AllowAllFactoryModels) || len(policy.AllowedModelIDs) > 0)
	if policy != nil && slices.Contains(policy.BlockedModelIDs, model.ID) {
		return false
	}
	if allowlist && !slices.Contains(policy.AllowedModelIDs, model.ID) {
		return false
	}
	return true
}

// FactoryDroidSeedModels is factoryDroidSeedModels: the registry narrowed by
// the availability rule with nothing known about the account yet.
func FactoryDroidSeedModels(scope FactoryDroidScope) []*NeoPiModel {
	region := factoryDroidInferenceRegion(scope)
	out := []*NeoPiModel{}
	for i := range factoryDroidRegistry() {
		model := &factoryDroidRegistry()[i]
		if !factoryDroidAvailable(model, FactoryDroidSettings{Flags: map[string]bool{}}, region) {
			continue
		}
		out = append(out, BuildFactoryDroidModel(model, scope, region, FactoryDroidRotation(model.Policy, region)))
	}
	return out
}

// FactoryDroidDiscoveredModels is fetchFactoryDroidModels' mapping: the
// registry narrowed by the account's gates and policy, each row on the live
// rotation intersected with registry eligibility and explicit blocks.
func FactoryDroidDiscoveredModels(scope FactoryDroidScope, settings FactoryDroidSettings) []*NeoPiModel {
	region := factoryDroidInferenceRegion(scope)
	out := []*NeoPiModel{}
	for i := range factoryDroidRegistry() {
		model := &factoryDroidRegistry()[i]
		if !factoryDroidAvailable(model, settings, region) {
			continue
		}
		var routed []string
		routedSet := false
		if settings.Routing != nil {
			if rotation, ok := settings.Routing.Models[model.ID]; ok {
				routed, routedSet = rotation, true
			} else if family := model.Policy.RoutingFamily; family != "" {
				routed, routedSet = settings.Routing.Defaults[family]
			}
		}
		var blocked []string
		if settings.Routing != nil {
			blocked = settings.Routing.BlockedProviders[model.ID]
		}
		eligible := []string{}
		for _, upstream := range FactoryDroidRotation(model.Policy, region) {
			if !slices.Contains(blocked, upstream) {
				eligible = append(eligible, upstream)
			}
		}
		rotation := eligible
		if routedSet {
			rotation = []string{}
			for _, upstream := range routed {
				if slices.Contains(eligible, upstream) {
					rotation = append(rotation, upstream)
				}
			}
		}
		if len(rotation) == 0 {
			continue
		}
		row := BuildFactoryDroidModel(model, scope, region, rotation)
		if routedSet {
			row.FactoryDroid.RoutingSource = "configured_order"
		}
		out = append(out, row)
	}
	return out
}

// FactoryDroidModelInfo is one account-scoped Factory Droid row as a
// registered model. Its thinking ladder is what the canonical thinking
// pipeline validates against; a requiresEffort ladder cannot turn off.
func FactoryDroidModelInfo(row *NeoPiModel) *ModelInfo {
	model := row.Clone()
	info := &ModelInfo{ID: model.ID, MetadataModelID: model.ID, Object: "model", OwnedBy: FactoryDroidProvider, Type: FactoryDroidProvider,
		DisplayName: model.Name, ContextLength: model.ContextWindow, MaxContextLength: model.ContextWindow, MaxCompletionTokens: model.MaxTokens,
		SupportedInputModalities: slices.Clone(model.Input), NeoPi: model}
	// A generate_image row is served through /v1/images, not chat.
	if entry := FactoryDroidModelFor(model.ID); entry != nil && entry.Policy.Image {
		info.Type = OpenAIImageModelType
		return info
	}
	if model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 {
		info.Thinking = &ThinkingSupport{Levels: slices.Clone(model.Thinking.Efforts), ZeroAllowed: !model.Thinking.RequiresEffort}
	}
	return info
}

// factoryDroidInferenceRegion is resolveFactoryDroidInferenceRegion.
func factoryDroidInferenceRegion(scope FactoryDroidScope) string {
	if scope.InferenceRegion != "" {
		return scope.InferenceRegion
	}
	if scope.Region == "eu" {
		return "eu"
	}
	return "global"
}

// ParseFactoryDroidSettings reads the feature-flags and managed-settings
// payloads as fetchFactoryDroidModels does. ok is false when the flags
// payload does not name a flags object, which leaves discovery unanswered.
// A nil settings body (unreadable or failed) leaves policy and image settings
// unknown.
func ParseFactoryDroidSettings(flagsBody, settingsBody []byte) (FactoryDroidSettings, bool) {
	var flagsPayload map[string]any
	if json.Unmarshal(flagsBody, &flagsPayload) != nil || flagsPayload == nil {
		return FactoryDroidSettings{}, false
	}
	raw, ok := flagsPayload["flags"].(map[string]any)
	if !ok {
		return FactoryDroidSettings{}, false
	}
	settings := FactoryDroidSettings{Flags: map[string]bool{}}
	for key, value := range raw {
		settings.Flags[key] = value == true
	}
	var managed map[string]any
	if settingsBody != nil && json.Unmarshal(settingsBody, &managed) == nil {
		if body, ok := managed["settings"].(map[string]any); ok {
			settings.Policy = readFactoryDroidOrgPolicy(body)
			images := &FactoryDroidImageSettings{Enabled: body["imageGenerationEnabled"] != false}
			if list, ok := body["imageGenerationAllowedModelIds"].([]any); ok {
				images.allowedSet = true
				images.AllowedModelIDs = []string{}
				for _, entry := range list {
					if id, ok := entry.(string); ok {
						images.AllowedModelIDs = append(images.AllowedModelIDs, id)
					}
				}
			}
			settings.Images = images
		}
	}
	if configs, ok := flagsPayload["configs"].(map[string]any); ok {
		if routing, ok := configs["provider_routing"].(map[string]any); ok {
			settings.Routing = &FactoryDroidRouting{
				Models:           factoryDroidRotationMap(routing["models"]),
				Defaults:         factoryDroidRotationMap(routing["defaults"]),
				BlockedProviders: factoryDroidRotationMap(routing["blockedProviders"]),
			}
		}
	}
	return settings, true
}

func readFactoryDroidOrgPolicy(settings map[string]any) *FactoryDroidOrgPolicy {
	policy, ok := settings["modelPolicy"].(map[string]any)
	if !ok {
		return nil
	}
	ids := func(key string) []string {
		list, ok := policy[key].([]any)
		if !ok {
			return nil
		}
		out := []string{}
		for _, entry := range list {
			if id, ok := entry.(string); ok {
				if canonical := FactoryDroidCanonicalPolicyID(id); !slices.Contains(out, canonical) {
					out = append(out, canonical)
				}
			}
		}
		return out
	}
	boolean := func(key string) *bool {
		value, ok := policy[key].(bool)
		if !ok {
			return nil
		}
		return &value
	}
	return &FactoryDroidOrgPolicy{
		AllowAllFactoryModels:        boolean("allowAllFactoryModels"),
		AllowedModelIDs:              ids("allowedModelIds"),
		BlockedModelIDs:              ids("blockedModelIds"),
		RequireExplicitOptInModelIDs: ids("requireExplicitOptInModelIds"),
		IsFastModelsAllowed:          boolean("isFastModelsAllowed"),
	}
}

// factoryDroidRotationMap keeps the entries whose value is an array of
// strings.
func factoryDroidRotationMap(value any) map[string][]string {
	out := map[string][]string{}
	object, ok := value.(map[string]any)
	if !ok {
		return out
	}
	for key, entry := range object {
		list, ok := entry.([]any)
		if !ok {
			continue
		}
		providers := make([]string, 0, len(list))
		valid := true
		for _, item := range list {
			provider, ok := item.(string)
			if !ok {
				valid = false
				break
			}
			providers = append(providers, provider)
		}
		if valid {
			out[key] = providers
		}
	}
	return out
}
