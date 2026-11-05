package registry

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopitaxonomy"
	"github.com/tidwall/gjson"
)

// neoPiMapVllm is vllmModelManagerOptions' mapModel: the served context is
// the listing's max_model_len, and Qwen 3.8+ open weights always think (vLLM
// reports no reasoning capability).
func neoPiMapVllm(entry gjson.Result, defaults neoPiSpec, reference *NeoPiModel) neoPiSpec {
	spec := neoPiMapWithBundledReference(entry, defaults, reference)
	spec.contextWindow = neoPiPositive(entry.Get("max_model_len"), spec.contextWindow)
	if identity := neopitaxonomy.Classify("vllm", spec.id); identity.Class == "qwen" && identity.AtLeast("3.8") {
		spec.reasoning = true
	}
	return spec
}

// neoPiLmStudioMetadata is LM Studio's native metadata for one model id
// (LmStudioNativeModelMetadata).
type neoPiLmStudioMetadata struct {
	input         []string
	contextWindow int
	decision      bool
}

// NeoPiLmStudioNativeBaseURL is toLmStudioNativeBaseUrl: the OpenAI base
// without its trailing /v1.
func NeoPiLmStudioNativeBaseURL(baseURL string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	return strings.TrimSuffix(trimmed, "/v1")
}

// neoPiLmStudioMetadataFrom is fetchLmStudioNativeModelMetadata's merge of
// GET /api/v0/models (input and served context per model) and GET
// /api/v1/models (decision models, keyed by model key and every loaded
// instance id). nil when neither answer carries metadata.
func neoPiLmStudioMetadataFrom(v0, v1 []byte) map[string]neoPiLmStudioMetadata {
	decisions := neoPiLmStudioDecisions(v1)
	v0Data := gjson.Result{}
	if gjson.ValidBytes(v0) {
		if payload := gjson.ParseBytes(v0); payload.IsObject() && payload.Get("data").IsArray() {
			v0Data = payload.Get("data")
		}
	}
	if !v0Data.Exists() && len(decisions) == 0 {
		return nil
	}
	metadata := map[string]neoPiLmStudioMetadata{}
	for _, entry := range v0Data.Array() {
		id := entry.Get("id")
		if !entry.IsObject() || id.Type != gjson.String || id.Str == "" {
			continue
		}
		metadata[id.Str] = neoPiLmStudioMetadata{input: neoPiLmStudioInput(entry), contextWindow: neoPiLmStudioContext(entry),
			decision: neoPiLmStudioType(entry) == "decision"}
	}
	for id, model := range decisions {
		metadata[id] = model
	}
	return metadata
}

// neoPiLmStudioType is getLmStudioModelType.
func neoPiLmStudioType(entry gjson.Result) string {
	if kind := entry.Get("type"); kind.Type == gjson.String {
		return strings.ToLower(kind.Str)
	}
	return ""
}

// neoPiLmStudioInput is getLmStudioNativeInput: a VLM or a vision/image
// capability reads images; a decision model says so through
// has_vision_adapter.
func neoPiLmStudioInput(entry gjson.Result) []string {
	kind := neoPiLmStudioType(entry)
	image := false
	if kind == "decision" {
		image = entry.Get("has_vision_adapter").Type == gjson.True
	} else {
		image = kind == "vlm"
		for _, capability := range neoPiStrings(entry.Get("capabilities")) {
			if lower := strings.ToLower(capability); lower == "vision" || lower == "image" {
				image = true
			}
		}
	}
	if image {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// neoPiLmStudioContext is getLmStudioNativeContextWindow: a loaded model's
// served window, else the maximum, the trained length, or max_model_len.
func neoPiLmStudioContext(entry gjson.Result) int {
	if entry.Get("state").Type == gjson.String && entry.Get("state").Str == "loaded" {
		if loaded := neoPiPositive(entry.Get("loaded_context_length"), 0); loaded > 0 {
			return loaded
		}
	}
	for _, field := range []string{"max_context_length", "context_length", "max_model_len"} {
		if window := neoPiPositive(entry.Get(field), 0); window > 0 {
			return window
		}
	}
	return 0
}

// neoPiLmStudioDecisions is getLmStudioV1DecisionModels.
func neoPiLmStudioDecisions(v1 []byte) map[string]neoPiLmStudioMetadata {
	decisions := map[string]neoPiLmStudioMetadata{}
	if !gjson.ValidBytes(v1) {
		return decisions
	}
	payload := gjson.ParseBytes(v1)
	if !payload.IsObject() || !payload.Get("models").IsArray() {
		return decisions
	}
	for _, entry := range payload.Get("models").Array() {
		if !entry.IsObject() || neoPiLmStudioType(entry) != "decision" {
			continue
		}
		var instances []gjson.Result
		if loaded := entry.Get("loaded_instances"); loaded.IsArray() {
			for _, instance := range loaded.Array() {
				if instance.IsObject() {
					instances = append(instances, instance)
				}
			}
		}
		window := 0
		for _, instance := range instances {
			if config := instance.Get("config"); config.IsObject() {
				if window = neoPiPositive(config.Get("context_length"), 0); window > 0 {
					break
				}
			}
		}
		if window == 0 {
			window = neoPiPositive(entry.Get("max_context_length"), 0)
		}
		model := neoPiLmStudioMetadata{input: neoPiLmStudioInput(entry), contextWindow: window, decision: true}
		ids := []gjson.Result{entry.Get("key")}
		for _, instance := range instances {
			ids = append(ids, instance.Get("id"))
		}
		for _, id := range ids {
			if id.Type == gjson.String && id.Str != "" {
				decisions[id.Str] = model
			}
		}
	}
	return decisions
}

// neoPiApplyLmStudioMetadata is lmStudioModelManagerOptions' merge of the
// native metadata into the listed rows: the input and served context, and a
// decision model becomes a judge on the Decisions API, which serves no chat.
func neoPiApplyLmStudioMetadata(specs []neoPiSpec, metadata map[string]neoPiLmStudioMetadata) {
	for i := range specs {
		model, ok := metadata[specs[i].id]
		if !ok {
			continue
		}
		if model.decision {
			specs[i].api, specs[i].kind, specs[i].reasoning = "openai-decisions", "judge", false
		}
		specs[i].input = model.input
		if model.contextWindow > 0 {
			specs[i].contextWindow = model.contextWindow
		}
	}
}

// NeoPiLmStudioListing is NeoPiCompletionsListing for LM Studio, with the
// native metadata its manager fetched beside the listing (v0 and v1 bodies;
// nil when unavailable).
func NeoPiLmStudioListing(baseURL string, body, v0, v1 []byte) ([]*ModelInfo, error) {
	return neoPiCompletionsListing("lm-studio", baseURL, body, neoPiListingExtras{lmStudio: neoPiLmStudioMetadataFrom(v0, v1)})
}

// NeoPiLmStudioListingUsable reports whether an LM Studio /models body is a
// listing NeoPi reads entries from (fetchOpenAICompatibleModels): its manager
// returns that failure before awaiting the native metadata.
func NeoPiLmStudioListingUsable(body []byte) bool {
	if !gjson.ValidBytes(body) {
		return false
	}
	_, ok := neoPiExtractEntries(gjson.ParseBytes(body))
	return ok
}
