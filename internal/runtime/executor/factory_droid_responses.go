package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// responsesRow is the shared transport's openai-responses model for the
// attempt's route (resolveRoute's inner model).
func (a *factoryDroidAttempt) responsesRow() (*registry.NeoPiModel, error) {
	var compat registry.NeoPiCompat
	if err := json.Unmarshal(a.route.Compat, &compat); err != nil {
		return nil, fmt.Errorf("factory-droid: route compat for %s: %w", a.model.ID, err)
	}
	omit, _ := a.route.Catalog["omitMaxOutputTokens"].(bool)
	return &registry.NeoPiModel{ID: a.model.ID, Name: a.model.Name, API: "openai-responses", Provider: factorydroid.Provider, BaseURL: a.baseURL,
		Reasoning: a.route.Reasoning, Input: slices.Clone(a.model.Input), ContextWindow: a.contextWindow, MaxTokens: a.maxTokens,
		Thinking: a.route.Thinking.Clone(), Compat: &compat, OmitMaxOutputTokens: omit}, nil
}

// responsesExecutor is the shared Responses port bound to one attempt
// (streamResponsesWire): droid's identity prefix, every function tool
// non-strict, the selected effort with an auto summary, no encrypted
// reasoning when reasoning is off, the family's default tool choice, and the
// route's request extras in native's order.
func (e *FactoryDroidExecutor) responsesExecutor(a *factoryDroidAttempt) (*NeoPiResponsesExecutor, error) {
	row, err := a.responsesRow()
	if err != nil {
		return nil, err
	}
	served := false
	exec := NewNeoPiResponsesExecutor(factorydroid.Provider, e.cfg)
	exec.binding = &neoPiResponsesBinding{model: row, token: a.token, session: a.session,
		shape: func(conversation *openaiwire.Context, options *openaiwire.Options) {
			// The caller's effort as openaiwire fitted it to the model; an
			// effort outside the ladder was already refused.
			requested := options.Effort
			if options.DisableReasoning {
				requested = string(thinking.LevelNone)
			}
			effort, off := a.effortFor(requested)
			for _, item := range conversation.Input {
				served = served || factoryDroidResponsesServed(item)
			}
			conversation.SystemPrompts = append([]string{helps.FactoryDroidSystemPrefix}, conversation.SystemPrompts...)
			for index, tool := range conversation.Tools {
				if gjson.GetBytes(tool, "type").String() == "function" {
					conversation.Tools[index] = neoPiSetRaw(tool, "strict", jsjson.Marshal(false))
				}
			}
			verbosity := options.TextVerbosity
			if verbosity == "" {
				verbosity = a.policyString("responsesVerbosity")
			}
			// The forwarded allowance: the caller's, else the model's own.
			if options.MaxTokens == nil && a.maxTokens > 0 {
				maxTokens := float64(a.maxTokens)
				options.MaxTokens = &maxTokens
			}
			options.Effort, options.DisableReasoning = effort, off
			options.SummarySet, options.ReasoningSummary = effort != "", ""
			if effort != "" {
				options.ReasoningSummary = "auto"
			}
			options.IncludeEncryptedReasoning = nil
			if off {
				include := false
				options.IncludeEncryptedReasoning = &include
			}
			// Native sends no sampling here, and verbosity rides the extras.
			options.Temperature, options.TopP, options.TextVerbosity = nil, nil, ""
			if options.ToolChoice == nil && a.policyBool("responsesToolChoiceAuto") {
				options.ToolChoice = jsjson.Marshal("auto")
			}
			extra := []jsjson.Field{{Name: "prompt_cache_key", Value: jsjson.Marshal(a.session)}}
			if a.policyBool("responsesCacheRetention") {
				extra = append(extra, jsjson.Field{Name: "prompt_cache_retention", Value: jsjson.Marshal("24h")})
			}
			if len(conversation.Tools) > 0 {
				parallel, set := a.route.Request["responsesParallelToolCalls"].(bool)
				extra = append(extra, jsjson.Field{Name: "parallel_tool_calls", Value: jsjson.Marshal(!set || parallel)})
			}
			if tier := a.policyString("responsesServiceTier"); tier != "" {
				extra = append(extra, jsjson.Field{Name: "service_tier", Value: jsjson.Marshal(tier)})
			}
			if verbosity != "" {
				extra = append(extra, jsjson.Field{Name: "text", Value: jsjson.Object(jsjson.Field{Name: "verbosity", Value: jsjson.Marshal(verbosity)})})
			}
			if a.policyBool("responsesSafetyIdentifier") {
				extra = append(extra, jsjson.Field{Name: "safety_identifier", Value: jsjson.Marshal(a.session)})
			}
			options.ExtraBody = extra
		},
		headers: func(ctx context.Context, auth *cliproxyauth.Auth, header, clientHeaders http.Header) {
			header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
			header.Set("Connection", "keep-alive")
			factoryDroidApplyIdentity(ctx, auth, clientHeaders, header, a.identityHeaders(served))
		}}
	return exec, nil
}

// factoryDroidResponsesServed reports a Responses input item from a turn
// already served: an assistant message, a tool call or its reasoning.
func factoryDroidResponsesServed(item json.RawMessage) bool {
	root := gjson.ParseBytes(item)
	if root.Get("role").String() == "assistant" {
		return true
	}
	switch root.Get("type").String() {
	case "function_call", "custom_tool_call", "reasoning":
		return true
	}
	return false
}
