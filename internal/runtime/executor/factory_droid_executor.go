package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// FactoryDroidExecutor is NeoPi's streamFactoryDroid: a signed-in Factory
// account's WorkOS bearer on Factory's LLM proxy, which multiplexes wire
// protocols by model family. Each attempt binds to the account serving it
// (host, organization, eligible upstream and native limits), resolves the
// route's request dialect for that upstream, and sends droid's client
// identity and Droid system prefix over the route's wire.
type FactoryDroidExecutor struct {
	cfg *config.Config
	// newID mints a request's x-assistant-message-id (crypto.randomUUID).
	newID func() string
	// completions is the shared Chat Completions port each attempt copies
	// with its own route hooks.
	completions *NeoPiCompletionsExecutor
}

// NewFactoryDroidExecutor builds the Factory Droid executor.
func NewFactoryDroidExecutor(cfg *config.Config) *FactoryDroidExecutor {
	return &FactoryDroidExecutor{cfg: cfg, newID: uuid.NewString,
		completions: NewNeoPiCompletionsExecutor(factorydroid.Provider, cfg, NeoPiCompletionsProfile{})}
}

// Identifier is the provider key.
func (e *FactoryDroidExecutor) Identifier() string { return factorydroid.Provider }

// factoryDroidAttempt is one request attempt bound to the account serving it.
type factoryDroidAttempt struct {
	model    *registry.FactoryDroidModel
	row      *registry.NeoPiModel
	wire     factorydroid.Wire
	route    registry.FactoryDroidRoute
	upstream string
	baseURL  string
	orgID    string
	token    string
	// contextWindow and maxTokens are the native limits narrowed to the
	// account's inference region.
	contextWindow int
	maxTokens     int
	session       string
	requestID     string
}

// attempt resolves the account scope for one request (scopeToAccount).
func (e *FactoryDroidExecutor) attempt(auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*factoryDroidAttempt, error) {
	var credential *factorydroid.Credential
	if auth != nil {
		credential, _ = factorydroid.RecordCredential(auth.Metadata)
	}
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "No Factory Droid credentials found. Sign in to Factory Droid (WorkOS device code)."}
	}
	id := thinking.ParseSuffix(req.Model).ModelName
	model := registry.FactoryDroidModelFor(id)
	if model == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("factory-droid: model %s is not in the Factory Droid registry", id)}
	}
	row := factoryDroidRow(req, model, credential)
	wire := factorydroid.Wire(model.Policy.Wire)
	// Custom gateways keep their URL; Factory's own hosts follow residency.
	baseURL := row.BaseURL
	if baseURL == "" || baseURL == registry.FactoryDroidWireBaseURL(model.Policy.Wire, "global") || baseURL == registry.FactoryDroidWireBaseURL(model.Policy.Wire, "eu") {
		baseURL = registry.FactoryDroidWireBaseURL(model.Policy.Wire, credential.Region)
	}
	inferenceRegion := registry.FactoryDroidInferenceRegion(credential.Region, credential.InferenceRegion)
	eligible := registry.FactoryDroidRotation(model.Policy, inferenceRegion)
	candidates := eligible
	if row.FactoryDroid != nil && row.FactoryDroid.APIProviders != nil {
		candidates = row.FactoryDroid.APIProviders
	}
	upstream := ""
	for _, candidate := range candidates {
		if slices.Contains(eligible, candidate) {
			upstream = candidate
			break
		}
	}
	route, routed := model.Routes[upstream]
	if upstream == "" || !routed {
		return nil, statusErr{code: http.StatusBadRequest, msg: "Factory Droid model is unavailable in this account region."}
	}
	limits := registry.FactoryDroidRegionalLimits(model.Policy, inferenceRegion)
	if row.ContextWindow > 0 && limits.ContextWindow != nil && *limits.ContextWindow < row.ContextWindow {
		// No authoritative count exists for this exact request, so the
		// caller's context management must check or compact the history.
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("Factory Droid %s now has a %d-token context window in %s, smaller than the selected model's %d. "+
			"Rediscover and select the regional model so context management can check or compact the history before retrying.",
			model.ID, *limits.ContextWindow, inferenceRegion, row.ContextWindow)}
	}
	contextWindow := row.ContextWindow
	if contextWindow == 0 && limits.ContextWindow != nil {
		contextWindow = *limits.ContextWindow
	}
	maxTokens := row.MaxTokens
	if limits.MaxTokens != nil && (maxTokens == 0 || *limits.MaxTokens < maxTokens) {
		maxTokens = *limits.MaxTokens
	}
	requestID := e.newID()
	// The proxy expects v4-shaped ids; a session id maps to a stable one.
	session := neoPiSession(opts, nil)
	if session == "" {
		session = helps.ProviderSessionUUID(e.Identifier(), req.Metadata, opts.Metadata)
	}
	sessionUUID := requestID
	if session != "" {
		sessionUUID = helps.FactoryDroidDeterministicUUID(session)
	}
	return &factoryDroidAttempt{model: model, row: row, wire: wire, route: route, upstream: upstream, baseURL: baseURL,
		orgID: credential.OrgID, token: strings.TrimSpace(credential.AccessToken), contextWindow: contextWindow, maxTokens: maxTokens,
		session: sessionUUID, requestID: requestID}, nil
}

// factoryDroidRow is the account-scoped row the request selected: the row
// discovery registered for the account, else the registry model built for
// the credential's scope on its registry rotation.
func factoryDroidRow(req cliproxyexecutor.Request, model *registry.FactoryDroidModel, credential *factorydroid.Credential) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == registry.FactoryDroidAPI && info.NeoPi.ID == model.ID {
		return info.NeoPi
	}
	region := registry.FactoryDroidInferenceRegion(credential.Region, credential.InferenceRegion)
	scope := registry.FactoryDroidScope{Region: credential.Region, InferenceRegion: credential.InferenceRegion, OrgID: credential.OrgID}
	return registry.BuildFactoryDroidModel(model, scope, region, registry.FactoryDroidRotation(model.Policy, region))
}

// identityHeaders is buildIdentityHeaders for the attempt. The upstream
// locks for the session once a turn has been served.
func (a *factoryDroidAttempt) identityHeaders(served bool) http.Header {
	routingSource := "registry_default"
	if a.row.FactoryDroid != nil && a.row.FactoryDroid.RoutingSource != "" {
		routingSource = a.row.FactoryDroid.RoutingSource
	}
	if served || a.policyBool("routingSessionLock") {
		routingSource = "session_lock"
	}
	platform, _ := a.route.Request["openaiPlatformHeader"].(string)
	return helps.FactoryDroidIdentityHeaders(helps.FactoryDroidIdentity{Wire: a.wire, Upstream: a.upstream, RoutingSource: routingSource,
		SessionUUID: a.session, RequestID: a.requestID, OrgID: a.orgID, OpenAIPlatform: platform})
}

func (a *factoryDroidAttempt) policyBool(name string) bool {
	value, _ := a.route.Request[name].(bool)
	return value
}

func (a *factoryDroidAttempt) policyString(name string) string {
	value, _ := a.route.Request[name].(string)
	return value
}

// effort is normalizeMandatoryReasoningOptions and selectEffort over the
// effort the canonical thinking pipeline settled for the request (suffix
// over body, validated against the row's ladder).
func (a *factoryDroidAttempt) effort(translated []byte, format string) (string, bool) {
	requested := thinking.ExtractTranslatedReasoningEffort(translated, format)
	disabled := requested == string(thinking.LevelNone)
	if disabled || requested == string(thinking.LevelAuto) {
		requested = ""
	}
	// normalizeMandatoryReasoningOptions: a requiresEffort model runs an off
	// or effort-less request at its floor effort.
	options := neoPiCompletionsOptions{reasoning: requested, disableReasoning: disabled}
	neoPiNormalizeMandatoryReasoning(a.row, &options)
	if options.disableReasoning {
		return "", true
	}
	// selectEffort: without a caller effort the native default applies,
	// which can be thinking off.
	selected := options.reasoning
	if selected == "" {
		if a.model.Policy.DefaultReasoningOff {
			selected = "off"
		} else if a.row.Thinking != nil {
			selected = a.row.Thinking.DefaultLevel
		}
	}
	if selected == "none" || selected == "off" {
		return "", true
	}
	return selected, false
}

// messagesRow is the shared transport's anthropic-messages model for the
// attempt's route (resolveRoute's inner model).
func (a *factoryDroidAttempt) messagesRow() (*registry.NeoPiModel, error) {
	var compat registry.NeoPiAnthropicCompat
	if err := json.Unmarshal(a.route.Compat, &compat); err != nil {
		return nil, fmt.Errorf("factory-droid: route compat for %s: %w", a.model.ID, err)
	}
	return &registry.NeoPiModel{ID: a.model.ID, Name: a.model.Name, API: "anthropic-messages", Provider: factorydroid.Provider, BaseURL: a.baseURL,
		Reasoning: a.route.Reasoning, Input: slices.Clone(a.model.Input), ContextWindow: a.contextWindow, MaxTokens: a.maxTokens,
		Thinking: a.route.Thinking.Clone(), AnthropicCompat: &compat}, nil
}

// messagesExecutor is the shared Messages port bound to one attempt
// (streamMessagesWire).
func (e *FactoryDroidExecutor) messagesExecutor(a *factoryDroidAttempt) (*ClaudeExecutor, error) {
	row, err := a.messagesRow()
	if err != nil {
		return nil, err
	}
	compat := gjson.ParseBytes(a.route.Compat)
	var fallbacks []string
	if entries, ok := a.route.Catalog["serverSideFallbackModels"].([]any); ok {
		for _, entry := range entries {
			if name, isString := entry.(string); isString {
				fallbacks = append(fallbacks, name)
			}
		}
	}
	messages := NewClaudeExecutor(e.cfg)
	messages.provider = factorydroid.Provider
	messages.thirdParty = &neoPiThirdPartyProvider{
		catalog:    func(*cliproxyauth.Auth, cliproxyexecutor.Request, string) *registry.NeoPiModel { return row },
		credential: neoPiAccountBearer(func(*cliproxyauth.Auth) string { return a.token }),
		factory: func(translated []byte) ([]byte, *neoPiFactoryMessages, http.Header, error) {
			effort, off := a.effort(translated, "claude")
			model := helps.FactoryDroidAnthropicModel{
				Reasoning:                       row.Reasoning,
				DisabledThinking:                compat.Get("disabledThinking").String(),
				BetweenToolsEffort:              compat.Get("betweenToolsEffort").String(),
				SupportsBetweenToolsThinking:    compat.Get("supportsBetweenToolsThinking").Bool(),
				DisableAdaptiveThinking:         compat.Get("disableAdaptiveThinking").Bool(),
				RequiresThinkingEnabled:         compat.Get("requiresThinkingEnabled").Bool(),
				SupportsOutputEffort:            compat.Get("supportsOutputEffort").Bool(),
				SupportsThinkingBindingControls: compat.Get("supportsThinkingBindingControls").Bool(),
				StripThinkingHistory:            compat.Get("stripThinkingHistory").Bool(),
				AnthropicThinking:               a.policyString("anthropicThinking"),
			}
			if ladder := row.Thinking; ladder != nil {
				model.Mode, model.SupportsDisplay, model.PrefixBinding = ladder.Mode, ladder.SupportsDisplay, ladder.PrefixBinding
				model.EffortBudgets = ladder.EffortBudgets
				model.TagOnly = ladder.Mode == "anthropic-adaptive" && len(ladder.Efforts) > 0
				for _, level := range ladder.Efforts {
					model.TagOnly = model.TagOnly && ladder.EffortMap[level] == "adaptive"
				}
			}
			hide := gjson.GetBytes(translated, "thinking.display").String() == "omitted"
			resolved := helps.FactoryDroidResolveAnthropicThinking(model, effort, off,
				helps.FactoryDroidThinkingHistoryStripped([]byte(gjson.GetBytes(translated, "messages").Raw)), hide)
			served := false
			for _, message := range gjson.GetBytes(translated, "messages").Array() {
				served = served || message.Get("role").String() == "assistant"
			}
			eager := compat.Get("supportsEagerToolInputStreaming").Bool() && !compat.Get("officialEndpoint").Bool()
			return factoryDroidMessagesSystem(translated), &neoPiFactoryMessages{thinking: resolved, maxTokens: int64(a.maxTokens), fallbacks: fallbacks,
				toolStreamingBeta: a.policyBool("anthropicToolStreamingBeta"), effortBeta: compat.Get("effortBeta").Bool(),
				fastMode: compat.Get("fastMode").Bool(), eagerTools: eager, disableStrictTools: compat.Get("disableStrictTools").Bool()}, a.identityHeaders(served), nil
		},
	}
	return messages, nil
}

// factoryDroidMessagesSystem puts the Droid identity sentence ahead of the
// caller's system prompt as its own block.
func factoryDroidMessagesSystem(payload []byte) []byte {
	text := func(value string) json.RawMessage {
		return jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("text")}, jsjson.Field{Name: "text", Value: jsjson.Marshal(value)})
	}
	blocks := []json.RawMessage{text(helps.FactoryDroidSystemPrefix)}
	system := gjson.GetBytes(payload, "system")
	if system.Type == gjson.String {
		if system.String() != "" {
			blocks = append(blocks, text(system.String()))
		}
	} else {
		for _, block := range system.Array() {
			blocks = append(blocks, json.RawMessage(block.Raw))
		}
	}
	return neoPiSetRaw(payload, "system", jsjson.Marshal(blocks))
}

// completionsRow is the shared transport's openai-completions model for the
// attempt's route, its compat carrying the route dialect's reasoning fields
// as extraBody.
func (a *factoryDroidAttempt) completionsRow() (*registry.NeoPiModel, error) {
	var compat registry.NeoPiCompletionsCompat
	if err := json.Unmarshal(a.route.Compat, &compat); err != nil {
		return nil, fmt.Errorf("factory-droid: route compat for %s: %w", a.model.ID, err)
	}
	return &registry.NeoPiModel{ID: a.model.ID, Name: a.model.Name, API: "openai-completions", Provider: factorydroid.Provider, BaseURL: a.baseURL,
		Reasoning: a.route.Reasoning, Input: slices.Clone(a.model.Input), ContextWindow: a.contextWindow, MaxTokens: a.maxTokens,
		Thinking: a.route.Thinking.Clone(), CompletionsCompat: &compat}, nil
}

// completionsExecutor is the shared Chat Completions port bound to one
// attempt (streamCompletionsWire): the route dialect owns every reasoning
// field, temperature defaults to droid's 1, tool names over the route's
// bound go out in the CLI's capped form and come back under the caller's.
func (e *FactoryDroidExecutor) completionsExecutor(a *factoryDroidAttempt) (*NeoPiCompletionsExecutor, error) {
	row, err := a.completionsRow()
	if err != nil {
		return nil, err
	}
	maxLength := 64
	if bound, ok := a.route.Catalog["toolCallNameMaxLength"].(float64); ok {
		maxLength = int(bound)
	}
	encode := func(name string) string { return helps.FactoryDroidCompletionsToolName(name, maxLength) }
	var byWire map[string]string
	served := false
	exec := *e.completions
	exec.profile = NeoPiCompletionsProfile{
		Provider: factorydroid.Provider,
		Row:      func(string, string) *registry.NeoPiModel { return row },
		APIKey:   func(*cliproxyauth.Auth) string { return a.token },
		URL: func(row *registry.NeoPiModel) string {
			return strings.TrimRight(row.BaseURL, "/") + "/chat/completions"
		},
		Request: func(_ context.Context, _ *cliproxyauth.Auth, row *registry.NeoPiModel, translated []byte) ([]byte, error) {
			effort, off := a.effort(translated, "openai")
			extra := map[string]any{}
			if reasoning := helps.FactoryDroidCompletionsReasoning(a.policyString("completionsReasoningMode"), a.policyString("completionsReasoningHistory"), effort, off); len(reasoning) > 0 {
				if errExtra := json.Unmarshal(reasoning, &extra); errExtra != nil {
					return nil, errExtra
				}
			}
			row.CompletionsCompat.ExtraBody = extra
			var names []string
			for _, tool := range gjson.GetBytes(translated, "tools").Array() {
				names = append(names, tool.Get("function.name").String())
			}
			var errNames error
			if byWire, errNames = helps.FactoryDroidToolNamesByWire(names, encode); errNames != nil {
				return nil, statusErr{code: http.StatusBadRequest, msg: errNames.Error()}
			}
			for _, message := range gjson.GetBytes(translated, "messages").Array() {
				served = served || message.Get("role").String() == "assistant"
			}
			return factoryDroidCompletionsRequest(translated, encode), nil
		},
		SessionID: func(cliproxyexecutor.Request, cliproxyexecutor.Options) string { return a.session },
		Headers: func(ctx context.Context, auth *cliproxyauth.Auth, incoming http.Header, _ string, _ *registry.NeoPiModel, headers http.Header, _ string) {
			factoryDroidApplyIdentity(ctx, auth, incoming, headers, a.identityHeaders(served))
		},
		Line: func(line []byte) []byte { return factoryDroidDecodeToolNames(line, byWire) },
	}
	return &exec, nil
}

// factoryDroidCompletionsRequest is what streamCompletionsWire hands the
// shared builder: the Droid prefix ahead of the system prompt, the route
// dialect's reasoning in place of the caller's, droid's temperature unless
// the caller set one, and tool names in their wire form.
func factoryDroidCompletionsRequest(translated []byte, encode func(string) string) []byte {
	for _, name := range []string{"reasoning_effort", "reasoning"} {
		translated = neoPiDelete(translated, name)
	}
	if !gjson.GetBytes(translated, "temperature").Exists() {
		translated = neoPiSetRaw(translated, "temperature", jsjson.Marshal(helps.FactoryDroidCompletionsTemperature))
	}
	messages := []json.RawMessage{jsjson.Object(jsjson.Field{Name: "role", Value: jsjson.Marshal("system")}, jsjson.Field{Name: "content", Value: jsjson.Marshal(helps.FactoryDroidSystemPrefix)})}
	for _, message := range gjson.GetBytes(translated, "messages").Array() {
		raw := []byte(message.Raw)
		for index, call := range message.Get("tool_calls").Array() {
			raw = neoPiSet(raw, fmt.Sprintf("tool_calls.%d.function.name", index), encode(call.Get("function.name").String()))
		}
		messages = append(messages, raw)
	}
	translated = neoPiSetRaw(translated, "messages", jsjson.Marshal(messages))
	for index, tool := range gjson.GetBytes(translated, "tools").Array() {
		translated = neoPiSet(translated, fmt.Sprintf("tools.%d.function.name", index), encode(tool.Get("function.name").String()))
	}
	if name := gjson.GetBytes(translated, "tool_choice.function.name"); name.Exists() {
		translated = neoPiSet(translated, "tool_choice.function.name", encode(name.String()))
	} else if name := gjson.GetBytes(translated, "tool_choice.name"); name.Exists() {
		translated = neoPiSet(translated, "tool_choice.name", encode(name.String()))
	}
	return translated
}

// factoryDroidDecodeToolNames returns streamed tool calls under the caller's
// declared name.
func factoryDroidDecodeToolNames(line []byte, byWire map[string]string) []byte {
	payload, framed := bytes.CutPrefix(line, []byte("data: "))
	if !framed || !gjson.ValidBytes(payload) {
		return line
	}
	changed := false
	for choice, entry := range gjson.GetBytes(payload, "choices").Array() {
		for call, toolCall := range entry.Get("delta.tool_calls").Array() {
			name := toolCall.Get("function.name")
			if caller, ok := byWire[name.String()]; ok && name.Exists() && caller != name.String() {
				payload = neoPiSet(payload, fmt.Sprintf("choices.%d.delta.tool_calls.%d.function.name", choice, call), caller)
				changed = true
			}
		}
	}
	if !changed {
		return line
	}
	return append([]byte("data: "), payload...)
}

// factoryDroidApplyIdentity puts droid's identity headers on a request: NeoPi
// spreads the caller's headers over the identity set, so the account's
// configured headers win where they name the same header.
func factoryDroidApplyIdentity(ctx context.Context, auth *cliproxyauth.Auth, incoming, headers, identity http.Header) {
	configured := make(http.Header)
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs((&http.Request{Header: configured}).WithContext(ctx), auth.Attributes, incoming)
	}
	for name, values := range identity {
		if value := configured.Get(name); value != "" {
			headers.Set(name, value)
			continue
		}
		headers[name] = values
	}
}

// factoryDroidWireExecutor is the shared transport one route's wire runs on.
type factoryDroidWireExecutor interface {
	Execute(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	ExecuteStream(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

// wireExecutor is streamWire: the attempt's wire on its shared transport.
func (e *FactoryDroidExecutor) wireExecutor(a *factoryDroidAttempt) (factoryDroidWireExecutor, error) {
	switch a.wire {
	case factorydroid.WireMessages:
		return e.messagesExecutor(a)
	case factorydroid.WireCompletions:
		return e.completionsExecutor(a)
	case factorydroid.WireGoogle:
		return &factoryDroidGemini{executor: e, attempt: a}, nil
	default:
		return nil, statusErr{code: http.StatusNotImplemented, msg: fmt.Sprintf("factory-droid: %s wire is not served", a.wire)}
	}
}

// Execute runs one non-streaming request.
func (e *FactoryDroidExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if openAICompatImageEndpointPath(opts) != "" {
		return e.executeImage(ctx, auth, req, opts)
	}
	a, err := e.attempt(auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	wire, err := e.wireExecutor(a)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	response, err := wire.Execute(ctx, auth, req, opts)
	return response, factoryDroidRegionError(a.model.ID, err)
}

// ExecuteStream relays the upstream stream in the client's format.
func (e *FactoryDroidExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if openAICompatImageEndpointPath(opts) != "" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "factory-droid: image generation does not stream; send the request without stream"}
	}
	a, err := e.attempt(auth, req, opts)
	if err != nil {
		return nil, err
	}
	wire, err := e.wireExecutor(a)
	if err != nil {
		return nil, err
	}
	result, err := wire.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		return nil, factoryDroidRegionError(a.model.ID, err)
	}
	return factoryDroidRegionStream(a.model.ID, result), nil
}

// factoryDroidRegionError is asRegionUnavailableError: a region rejection
// explains itself without excluding the model.
func factoryDroidRegionError(modelID string, err error) error {
	if err == nil {
		return nil
	}
	message, ok := helps.FactoryDroidRegionMessage(modelID, err.Error())
	if !ok {
		return err
	}
	code := http.StatusBadRequest
	var coded interface{ StatusCode() int }
	if errors.As(err, &coded) && coded.StatusCode() > 0 {
		code = coded.StatusCode()
	}
	return statusErr{code: code, msg: message}
}

// factoryDroidRegionStream rewrites a region rejection the stream reports.
func factoryDroidRegionStream(modelID string, result *cliproxyexecutor.StreamResult) *cliproxyexecutor.StreamResult {
	if result == nil {
		return nil
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		for chunk := range result.Chunks {
			chunk.Err = factoryDroidRegionError(modelID, chunk.Err)
			out <- chunk
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers, Chunks: out}
}

// CountTokens estimates locally from the Chat Completions form: Factory's
// proxy has no counting endpoint.
func (e *FactoryDroidExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.completions.CountTokens(ctx, auth, req, opts)
}

// Refresh renews the WorkOS session (the refresh-token grant scoped to the
// credential's organization, then the region lookup), merging what the
// refresh omitted from the stored record.
func (e *FactoryDroidExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "factory-droid executor: auth is nil"}
	}
	if cliproxyauth.IsRefreshLocked(auth) {
		return nil, cliproxyauth.ErrRefreshLocked
	}
	stored, ok := factorydroid.RecordCredential(auth.Metadata)
	if !ok || stored.RefreshToken == "" {
		return auth, nil
	}
	refreshed, err := factorydroid.NewAuth(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)).Refresh(ctx, stored)
	if err != nil {
		var oauthErr *factorydroid.Error
		if errors.As(err, &oauthErr) && (oauthErr.Status == http.StatusBadRequest || oauthErr.Status == http.StatusUnauthorized) {
			return nil, statusErr{code: http.StatusUnauthorized, msg: err.Error()}
		}
		return nil, err
	}
	merged := factorydroid.Merge(stored, refreshed)
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	// Organization scope the refresh dropped is dropped from the record too.
	for _, key := range []string{"email", "account_id", "org_id", "active_organization_id", "region", "inference_region"} {
		delete(updated.Metadata, key)
	}
	for key, value := range factorydroid.Metadata(merged) {
		updated.Metadata[key] = value
	}
	now := time.Now()
	updated.Metadata["last_refresh"] = now.UTC().Format(time.RFC3339)
	updated.LastRefreshedAt = now
	return updated, nil
}

// HttpRequest sends an arbitrary request with the account's bearer.
func (e *FactoryDroidExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("factory-droid: request is nil")
	}
	var credential *factorydroid.Credential
	if auth != nil {
		credential, _ = factorydroid.RecordCredential(auth.Metadata)
	}
	if credential == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "factory-droid: account has no credential"}
	}
	request = request.Clone(ctx)
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// factoryDroidAPIBaseURL is factoryDroidApiBaseUrl; tests point it at a
// loopback server.
var factoryDroidAPIBaseURL = registry.FactoryDroidAPIBaseURL

// FetchFactoryDroidModels is fetchFactoryDroidModels: Factory has no
// model-listing endpoint, so the registry is narrowed live with the
// account's Statsig gates (GET /api/feature-flags) and its organization's
// model policy and image settings (GET /api/organization/managed-settings).
// A failed or malformed flags answer leaves discovery unanswered; an
// unreadable settings body leaves policy and image settings unknown.
func FetchFactoryDroidModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	var credential *factorydroid.Credential
	if auth != nil {
		credential, _ = factorydroid.RecordCredential(auth.Metadata)
	}
	if credential == nil || strings.TrimSpace(credential.AccessToken) == "" {
		return nil, errors.New("factory-droid models: account has no access token")
	}
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	base := factoryDroidAPIBaseURL(credential.Region)
	get := func(path string) (int, []byte, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return 0, nil, err
		}
		// Bun's fetch defaults beside the init headers NeoPi passes.
		request.Header.Set("Accept", "*/*")
		request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		request.Header.Set("Connection", "keep-alive")
		request.Header.Set("User-Agent", factorydroid.BunUserAgent)
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential.AccessToken))
		for name, values := range factorydroid.ClientHeaders(credential.OrgID) {
			request.Header[name] = values
		}
		response, err := client.Do(request)
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = response.Body.Close() }()
		decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
		if err != nil {
			return response.StatusCode, nil, err
		}
		defer func() { _ = decoded.Close() }()
		body, err := io.ReadAll(decoded)
		return response.StatusCode, body, err
	}
	type answer struct {
		status int
		body   []byte
		err    error
	}
	settingsDone := make(chan answer, 1)
	go func() {
		status, body, err := get("/api/organization/managed-settings")
		settingsDone <- answer{status, body, err}
	}()
	flagsStatus, flagsBody, flagsErr := get("/api/feature-flags")
	settingsAnswer := <-settingsDone
	if flagsErr != nil {
		return nil, fmt.Errorf("factory-droid models: feature flags: %w", flagsErr)
	}
	if flagsStatus < 200 || flagsStatus >= 300 {
		return nil, fmt.Errorf("factory-droid models: feature flags answered %d", flagsStatus)
	}
	var settingsBody []byte
	if settingsAnswer.err == nil && settingsAnswer.status >= 200 && settingsAnswer.status < 300 {
		settingsBody = settingsAnswer.body
	}
	settings, ok := registry.ParseFactoryDroidSettings(flagsBody, settingsBody)
	if !ok {
		return nil, errors.New("factory-droid models: feature flags answer names no flags")
	}
	scope := registry.FactoryDroidScope{Region: credential.Region, InferenceRegion: credential.InferenceRegion, OrgID: credential.OrgID}
	rows := registry.FactoryDroidDiscoveredModels(scope, settings)
	models := make([]*registry.ModelInfo, 0, len(rows))
	for _, row := range rows {
		models = append(models, registry.FactoryDroidModelInfo(row))
	}
	return models, nil
}

// FactoryDroidSeedModels is factoryDroidSeedModels for one account: the
// registry narrowed offline for the credential's scope, served until
// discovery answers.
func FactoryDroidSeedModels(auth *cliproxyauth.Auth) []*registry.ModelInfo {
	scope := registry.FactoryDroidScope{}
	if auth != nil {
		if credential, ok := factorydroid.RecordCredential(auth.Metadata); ok {
			scope = registry.FactoryDroidScope{Region: credential.Region, InferenceRegion: credential.InferenceRegion, OrgID: credential.OrgID}
		}
	}
	rows := registry.FactoryDroidSeedModels(scope)
	models := make([]*registry.ModelInfo, 0, len(rows))
	for _, row := range rows {
		models = append(models, registry.FactoryDroidModelInfo(row))
	}
	return models
}
