package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/factorydroid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
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
}

// NewFactoryDroidExecutor builds the Factory Droid executor.
func NewFactoryDroidExecutor(cfg *config.Config) *FactoryDroidExecutor {
	return &FactoryDroidExecutor{cfg: cfg, newID: uuid.NewString}
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
	if baseURL == "" || baseURL == factorydroid.WireBaseURL(wire, "global") || baseURL == factorydroid.WireBaseURL(wire, "eu") {
		baseURL = factorydroid.WireBaseURL(wire, credential.Region)
	}
	inferenceRegion := factorydroid.InferenceRegion(credential.Region, credential.InferenceRegion)
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
	region := factorydroid.InferenceRegion(credential.Region, credential.InferenceRegion)
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
		catalog: func(*cliproxyauth.Auth, cliproxyexecutor.Request, string) *registry.NeoPiModel { return row },
		bearer:  func(*cliproxyauth.Auth) string { return a.token },
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

// Execute runs one non-streaming request.
func (e *FactoryDroidExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	a, err := e.attempt(auth, req, opts)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	switch a.wire {
	case factorydroid.WireMessages:
		messages, errMessages := e.messagesExecutor(a)
		if errMessages != nil {
			return cliproxyexecutor.Response{}, errMessages
		}
		response, errExecute := messages.Execute(ctx, auth, req, opts)
		return response, factoryDroidRegionError(a.model.ID, errExecute)
	default:
		return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: fmt.Sprintf("factory-droid: %s wire is not served", a.wire)}
	}
}

// ExecuteStream relays the upstream stream in the client's format.
func (e *FactoryDroidExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	a, err := e.attempt(auth, req, opts)
	if err != nil {
		return nil, err
	}
	switch a.wire {
	case factorydroid.WireMessages:
		messages, errMessages := e.messagesExecutor(a)
		if errMessages != nil {
			return nil, errMessages
		}
		result, errStream := messages.ExecuteStream(ctx, auth, req, opts)
		if errStream != nil {
			return nil, factoryDroidRegionError(a.model.ID, errStream)
		}
		return factoryDroidRegionStream(a.model.ID, result), nil
	default:
		return nil, statusErr{code: http.StatusNotImplemented, msg: fmt.Sprintf("factory-droid: %s wire is not served", a.wire)}
	}
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

// CountTokens is not served: Factory's proxy has no counting endpoint.
func (e *FactoryDroidExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "factory-droid: token counting is not supported"}
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
