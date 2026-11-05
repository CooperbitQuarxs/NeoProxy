package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	cursorauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/cursor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"golang.org/x/sync/singleflight"
)

// CursorExecutor serves a Cursor sign-in the way NeoPi's cursor-agent
// provider serves an auth-gateway client: the request is read as an OpenAI
// chat request, run as one Cursor Agent turn over the Run bidi stream
// (internal/cursorwire), and answered as OpenAI chat completion chunks. Calls
// Cursor hands back as MCP tool calls go to the client; calls Cursor runs
// itself stay server-side.
type CursorExecutor struct {
	cfg *config.Config
	// newID mints the ids NeoPi takes from crypto.randomUUID.
	newID func() string
}

// cursorConversations is NeoPi's process-wide conversation cache and the
// #8345 rotation state, shared by every Cursor account.
var cursorConversations = cursorwire.NewConversations()

var cursorRefreshGroup singleflight.Group

// NewCursorExecutor constructs a Cursor executor.
func NewCursorExecutor(cfg *config.Config) *CursorExecutor {
	return &CursorExecutor{cfg: cfg, newID: uuid.NewString}
}

// Identifier returns the provider key "cursor".
func (e *CursorExecutor) Identifier() string { return "cursor" }

func cursorMetadataString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil {
		return ""
	}
	if value, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(auth.Attributes[key])
}

// cursorAccessToken is the stored access token Cursor accepts as its API key.
func cursorAccessToken(auth *cliproxyauth.Auth) string {
	return cursorMetadataString(auth, "access_token")
}

// cursorBaseURL is the account's base_url override, "" for the default host.
func cursorBaseURL(auth *cliproxyauth.Auth) string {
	value := strings.TrimRight(cursorMetadataString(auth, "base_url"), "/")
	if value == cursorwire.DefaultBaseURL {
		return ""
	}
	return value
}

// PrepareRequest injects Cursor credentials into an outgoing request.
func (e *CursorExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	contentType := req.Header.Get("Content-Type")
	if contentType == "" {
		contentType = cursorwire.ContentTypeProto
	}
	cursorwire.SetClientHeaders(req.Header, cursorAccessToken(auth), contentType)
	return nil
}

// HttpRequest injects Cursor credentials and executes the request.
func (e *CursorExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("cursor executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// Refresh renews the access token the way NeoPi's refreshCursorHook does.
// Imported credentials are refresh-locked: NeoPi stays their only refresher.
func (e *CursorExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, statusErr{code: http.StatusInternalServerError, msg: "cursor executor: auth is nil"}
	}
	if cliproxyauth.IsRefreshLocked(auth) {
		return nil, cliproxyauth.ErrRefreshLocked
	}
	stored := cursorauth.Credential{Access: cursorAccessToken(auth), Refresh: cursorMetadataString(auth, "refresh_token"),
		Email: cursorMetadataString(auth, "email")}
	if stored.Refresh == "" {
		return auth, nil
	}
	result, err, _ := cursorRefreshGroup.Do(stored.Refresh, func() (any, error) {
		return cursorauth.NewAuthWithProxyURL(e.cfg, auth.ProxyURL).Refresh(ctx, stored)
	})
	if err != nil {
		if errors.Is(err, cursorauth.ErrSessionEnded) {
			return nil, statusErr{code: http.StatusUnauthorized, msg: err.Error()}
		}
		return nil, err
	}
	credential := result.(*cursorauth.Credential)
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = map[string]any{}
	}
	now := time.Now()
	updated.Metadata["type"] = "cursor"
	updated.Metadata["access_token"] = credential.Access
	updated.Metadata["refresh_token"] = credential.Refresh
	updated.Metadata["expired"] = credential.Expires.UTC().Format(time.RFC3339)
	updated.Metadata["last_refresh"] = now.UTC().Format(time.RFC3339)
	if credential.Email != "" {
		updated.Metadata["email"] = credential.Email
	}
	if id := cursorauth.UserID(credential.Access); id != "" {
		updated.Metadata["user_id"] = id
	}
	updated.LastRefreshedAt = now
	return updated, nil
}

// CountTokens estimates locally; Cursor has no counting RPC.
func (e *CursorExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewOpenAICompatExecutor(e.Identifier(), e.cfg).CountTokens(ctx, auth, req, opts)
}

// cursorModel is the NeoPi record of the routed account's model, else the
// bundled row.
func cursorModel(req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "cursor-agent" {
		return info.NeoPi
	}
	return registry.CursorBundledNeoPiModel(model)
}

// cursorWireModel is the Model<"cursor-agent"> view the transport reads.
func cursorWireModel(model *registry.NeoPiModel) *cursorwire.Model {
	out := &cursorwire.Model{ID: model.ID, Name: model.Name, Provider: model.Provider, BaseURL: model.BaseURL, RequestModelID: model.RequestModelID}
	if model.Thinking != nil {
		out.EffortRouting = model.Thinking.EffortRouting
	}
	if c := model.Cursor; c != nil {
		out.MaxMode, out.MaxModeRoutes, out.ModelRoutes = c.MaxMode, c.MaxModeRoutes, c.ModelRoutes
		out.ModelParameters, out.RequiresToolSchemaProjection = c.ModelParameters, c.RequiresToolSchemaProjection
	}
	return out
}

// cursorEffort is the gateway's normalizeGatewayThinking followed by
// streamSimple's cursor branch: the client's effort fitted to the model's
// ladder, then required to be one the model serves.
func cursorEffort(model *registry.NeoPiModel, request *cursorwire.Request) (string, error) {
	effort := request.Reasoning
	if effort == "" || request.ForceReasoningOff || !model.Reasoning {
		return "", nil
	}
	var efforts []string
	if model.Thinking != nil {
		efforts = model.Thinking.Efforts
	}
	if len(efforts) > 0 && !containsString(efforts, effort) && (model.Thinking == nil || model.Thinking.EffortMap[effort] == "") {
		if clamped := codexwire.ClampEffort(efforts, effort); clamped != "" {
			effort = clamped
		}
	}
	if !containsString(efforts, effort) {
		return "", statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("Thinking effort %s is not supported by %s/%s. Supported efforts: %s",
			effort, model.Provider, model.ID, strings.Join(efforts, ", "))}
	}
	return effort, nil
}

// cursorWireModelID is resolveWireModelId.
func cursorWireModelID(model *registry.NeoPiModel, effort string) string {
	key := effort
	if key == "" {
		key = "off"
	}
	if model.Thinking != nil {
		if routed, ok := model.Thinking.EffortRouting[key]; ok && routed != "" {
			return routed
		}
	}
	if model.RequestModelID != "" {
		return model.RequestModelID
	}
	return model.ID
}

// cursorCacheKey is resolvePromptCacheKey over the client's own request: the
// body's prompt_cache_key or metadata session fields, then the session headers.
func cursorCacheKey(original []byte, headers http.Header) string {
	body := gjson.ParseBytes(original)
	if value := body.Get("prompt_cache_key"); value.Type == gjson.String && value.String() != "" {
		return value.String()
	}
	if metadata := body.Get("metadata"); metadata.IsObject() {
		for _, field := range []string{"prompt_cache_key", "session_id", "conversation_id"} {
			if value := metadata.Get(field); value.Type == gjson.String && value.String() != "" {
				return value.String()
			}
		}
	}
	for _, name := range []string{"X-Prompt-Cache-Key", "Session_id", "Conversation_id", "X-Session-Id", "X-Conversation-Id"} {
		if value := headers.Get(name); value != "" {
			return value
		}
	}
	return ""
}

type cursorPrepared struct {
	model       *registry.NeoPiModel
	wire        *cursorwire.Model
	request     *cursorwire.Request
	wireModelID string
	key         string
	apiKey      string
	translated  []byte
	finalize    helps.PayloadFinalizer
}

func (e *CursorExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*cursorPrepared, error) {
	apiKey := cursorAccessToken(auth)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "Cursor API key (access token) is required"}
	}
	modelID := thinking.ParseSuffix(req.Model).ModelName
	model := cursorModel(req, modelID)
	if model == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("cursor: model %s is not in the account's Cursor catalog", modelID)}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	translated, err := helps.TranslateRequestReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatOpenAI, modelID, bytes.Clone(req.Payload), stream, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "openai", e.Identifier()); err != nil {
		return nil, err
	}
	request, err := cursorwire.ParseOpenAIChat(translated, opts.Headers.Get)
	if err != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: err.Error()}
	}
	effort, err := cursorEffort(model, request)
	if err != nil {
		return nil, err
	}
	key := cursorCacheKey(original, opts.Headers)
	if key == "" {
		key = request.PromptCacheKey
	}
	if key == "" {
		key = cursorwire.DeriveSessionID(request.ModelID, &request.Context)
	}
	wire := cursorWireModel(model)
	if override := cursorBaseURL(auth); override != "" {
		wire.BaseURL = override
	}
	wireModelID := cursorWireModelID(model, effort)
	return &cursorPrepared{model: model, wire: wire, request: request, wireModelID: wireModelID, key: key, apiKey: apiKey, translated: translated,
		finalize: helps.NewPayloadFinalizer(e.cfg, e.Identifier(), modelID, "cursor", "", translated, req, opts)}, nil
}

// toolSchema is toolWireSchema, projected for models Cursor's catalog
// otherwise rejects.
func (p *cursorPrepared) toolSchema(tool cursorwire.Tool) any {
	schema := codexwire.ToolWireSchema(gjson.Parse(cursorwire.StringifyJSON(tool.Parameters)))
	if p.wire.RequiresToolSchemaProjection {
		schema = codexwire.SanitizeSchemaForCursor(schema)
	}
	value, ok := cursorwire.ParseJSON(string(schema))
	if !ok {
		return nil
	}
	return value
}

func (e *CursorExecutor) run(ctx context.Context, auth *cliproxyauth.Auth, prepared *cursorPrepared, reporter *helps.UsageReporter, sink cursorwire.Sink) (*cursorwire.Turn, error) {
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	turn, err := cursorConversations.Run(ctx, &cursorwire.RunInput{
		Model: prepared.wire, Context: &prepared.request.Context, WireModelID: prepared.wireModelID, ConversationKey: prepared.key,
		APIKey: prepared.apiKey, Do: client.Do, Sink: sink, NewUUID: e.newID, ToolSchema: prepared.toolSchema,
		OnPayload: func(run *cursorwire.AgentRunRequest) { helps.FinalizeCursorPayload(run, prepared.finalize) },
		Decode:    util.DecodeResponseBody,
		OnRequest: func(url string, header http.Header, body []byte) {
			helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: url, Method: http.MethodPost, Headers: header.Clone(),
				Body:     []byte(fmt.Sprintf("<cursor run request: %d bytes protobuf, model %s>", len(body), prepared.wireModelID)),
				Provider: e.Identifier(), AuthID: cursorAuthField(auth, false), AuthLabel: cursorAuthField(auth, true)})
		},
	})
	if err != nil {
		var runErr *cursorwire.RunError
		if errors.As(err, &runErr) {
			return turn, statusErr{code: runErr.HTTPStatus(), msg: runErr.Error()}
		}
		return turn, err
	}
	return turn, nil
}

func cursorAuthField(a *cliproxyauth.Auth, label bool) string {
	switch {
	case a == nil:
		return ""
	case label:
		return a.Label
	default:
		return a.ID
	}
}

// Execute runs one turn and answers with a chat completion.
func (e *CursorExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	prepared, err := e.prepare(ctx, auth, req, opts, false)
	if err != nil {
		return resp, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetUpstreamModel(prepared.wireModelID)
	collector := &cursorCollector{}
	turn, err := e.run(ctx, auth, prepared, reporter, collector)
	if err != nil {
		return resp, err
	}
	completion := encodeCursorCompletion(turn.Output, collector, req.Model)
	helps.AppendAPIResponseChunk(ctx, e.cfg, completion)
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	var state any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.translated, completion, &state)
	if helps.ApplyPatchTranslationError(state) != nil {
		return resp, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
	}
	if len(out) == 0 {
		return resp, statusErr{code: http.StatusBadGateway, msg: "cursor: response translation produced no output"}
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(completion))
	if format == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: http.Header{"Content-Type": {"application/json"}}}, nil
}

// ExecuteStream runs one turn as chat completion chunks. A failure before
// the first chunk is returned directly, so the caller can try another account.
func (e *CursorExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	prepared, err := e.prepare(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetUpstreamModel(prepared.wireModelID)

	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	original := helps.ApplyPatchOriginalRequest(req, opts)
	lines := make(chan []byte, 64)
	done := make(chan error, 1)
	encoder := newCursorChunkEncoder(req.Model, func(chunk []byte) error {
		select {
		case lines <- chunk:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	go func() {
		turn, errRun := e.run(ctx, auth, prepared, reporter, encoder)
		if errRun == nil {
			errRun = encoder.finish(turn.Output)
		}
		close(lines)
		done <- errRun
	}()

	// A successful turn always writes its finish chunk, so a stream that
	// closes without one failed before producing anything.
	first, ok := <-lines
	if !ok {
		if errRun := <-done; errRun != nil {
			return nil, errRun
		}
		return nil, statusErr{code: http.StatusBadGateway, msg: "cursor: stream produced no output"}
	}
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		var usage helps.StreamUsageBuffer
		var state any
		claudeInput := helps.NewClaudeInputTokenState(opts.SourceFormat, sdktranslator.FormatOpenAI, format, original)
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatOpenAI, format, req.Model, original, prepared.translated, &state)
		forward := func(chunk []byte) bool {
			line := append([]byte("data: "), chunk...)
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			usage.ObserveOpenAIStream(line)
			helps.ObserveChatTokenEvent(reporter, chunk)
			for _, translated := range helps.TranslateStreamWithClaudeInputTokens(ctx, sdktranslator.FormatOpenAI, format, req.Model, original, prepared.translated, line, &state, claudeInput) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(translated)}:
				case <-ctx.Done():
					return false
				}
			}
			return helps.ApplyPatchTranslationError(state) == nil
		}
		healthy := forward(first)
		for chunk := range lines {
			if healthy {
				healthy = forward(chunk)
			}
		}
		errRun := <-done
		for _, chunk := range helps.FinalizeApplyPatchStream(state) {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
			}
		}
		if helps.ApplyPatchTranslationError(state) != nil {
			errRun = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		if errRun != nil {
			if ctx.Err() != nil {
				return
			}
			helps.RecordAPIResponseError(ctx, e.cfg, errRun)
			usage.PublishFailure(ctx, reporter, errRun)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errRun}:
			case <-ctx.Done():
			}
			return
		}
		usage.Publish(ctx, reporter)
	}()
	return &cliproxyexecutor.StreamResult{Headers: http.Header{"Content-Type": {"text/event-stream"}}, Chunks: out}, nil
}

// ---------------------------------------------------------------------------
// OpenAI chat encoding (openai-chat-server encodeResponse / encodeStream)
// ---------------------------------------------------------------------------

// cursorClientCall is a call Cursor handed to the client: one carrying no
// kCursorExecResolved marker (anthropic-messages-server isClientToolUse).
func cursorClientCall(block *cursorwire.OutBlock) bool {
	return block.Type == "toolCall" && !block.Resolved
}

// cursorStringifyArgs is stringifyArgs: an unparseable inbound __raw passes
// through verbatim.
func cursorStringifyArgs(arguments any) string {
	if object, ok := arguments.(*cursorwire.JSONObject); ok && object.Len() == 1 {
		if raw, isString := func() (string, bool) { value, _ := object.Get("__raw"); text, ok := value.(string); return text, ok }(); isString {
			return raw
		}
	}
	if arguments == nil {
		return "{}"
	}
	return cursorwire.StringifyJSON(arguments)
}

func cursorUsageJSON(usage cursorwire.Usage) map[string]any {
	prompt := usage.Input + usage.CacheRead + usage.CacheWrite
	out := map[string]any{
		"prompt_tokens": prompt, "completion_tokens": usage.Output, "total_tokens": prompt + usage.Output,
		"prompt_tokens_details": map[string]any{"cached_tokens": usage.CacheRead},
	}
	if usage.Reasoning != nil {
		out["completion_tokens_details"] = map[string]any{"reasoning_tokens": *usage.Reasoning}
	}
	return out
}

func cursorFinishReason(stopReason string, hasClientCalls bool) string {
	switch {
	case stopReason == "toolUse" || (hasClientCalls && stopReason == "stop"):
		return "tool_calls"
	case stopReason == "length":
		return "length"
	}
	return "stop"
}

func cursorJSON(value any) []byte {
	data, _ := json.Marshal(value)
	return data
}

// cursorCollector gathers the non-streaming message (flattenAssistant).
type cursorCollector struct {
	text, reasoning strings.Builder
	calls           []*cursorwire.OutBlock
}

func (c *cursorCollector) TextDelta(delta string)     { c.text.WriteString(delta) }
func (c *cursorCollector) ThinkingDelta(delta string) { c.reasoning.WriteString(delta) }
func (c *cursorCollector) ToolCallStart(block *cursorwire.OutBlock) {
	if cursorClientCall(block) {
		c.calls = append(c.calls, block)
	}
}
func (c *cursorCollector) ToolCallDelta(*cursorwire.OutBlock, string) {}
func (c *cursorCollector) ToolCallEnd(*cursorwire.OutBlock)           {}

// encodeCursorCompletion is encodeResponse.
func encodeCursorCompletion(output *cursorwire.Output, collected *cursorCollector, model string) []byte {
	message := map[string]any{"role": "assistant", "content": nil, "refusal": nil}
	if text := collected.text.String(); text != "" {
		message["content"] = text
	}
	if reasoning := collected.reasoning.String(); reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if len(collected.calls) > 0 {
		calls := make([]any, len(collected.calls))
		for i, call := range collected.calls {
			calls[i] = map[string]any{"id": call.ID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": cursorStringifyArgs(call.Arguments)}}
		}
		message["tool_calls"] = calls
	}
	return cursorJSON(map[string]any{
		"id": "chatcmpl-" + uuid.NewString(), "object": "chat.completion", "created": time.Now().Unix(), "model": model, "system_fingerprint": nil,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": cursorFinishReason(output.StopReason, len(collected.calls) > 0), "logprobs": nil}},
		"usage":   cursorUsageJSON(output.Usage),
	})
}

type cursorSentTool struct {
	id, name         string
	hasArgumentBytes bool
}

// cursorChunkEncoder is encodeStream for client calls only. The role chunk
// goes out with the first event, so a turn that fails before producing
// anything has written nothing.
type cursorChunkEncoder struct {
	id      string
	created int64
	model   string
	emit    func([]byte) error
	err     error
	started bool
	index   map[*cursorwire.OutBlock]int
	sent    []*cursorSentTool
}

func newCursorChunkEncoder(model string, emit func([]byte) error) *cursorChunkEncoder {
	return &cursorChunkEncoder{id: "chatcmpl-" + uuid.NewString(), created: time.Now().Unix(), model: model, emit: emit, index: map[*cursorwire.OutBlock]int{}}
}

func (c *cursorChunkEncoder) write(delta map[string]any, finish any) {
	if c.err != nil {
		return
	}
	if !c.started {
		c.started = true
		c.write(map[string]any{"role": "assistant"}, nil)
	}
	c.err = c.emit(cursorJSON(map[string]any{"id": c.id, "object": "chat.completion.chunk", "created": c.created, "model": c.model,
		"system_fingerprint": nil, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil}}}))
}

func (c *cursorChunkEncoder) TextDelta(delta string) {
	if delta != "" {
		c.write(map[string]any{"content": delta}, nil)
	}
}

func (c *cursorChunkEncoder) ThinkingDelta(delta string) {
	if delta != "" {
		c.write(map[string]any{"reasoning_content": delta}, nil)
	}
}

func (c *cursorChunkEncoder) ToolCallStart(block *cursorwire.OutBlock) {
	if !cursorClientCall(block) {
		return
	}
	index := len(c.sent)
	c.index[block] = index
	c.sent = append(c.sent, &cursorSentTool{id: block.ID, name: block.Name})
	c.write(map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": block.ID, "type": "function",
		"function": map[string]any{"name": block.Name, "arguments": ""}}}}, nil)
}

func (c *cursorChunkEncoder) ToolCallDelta(block *cursorwire.OutBlock, delta string) {
	index, ok := c.index[block]
	if !ok {
		return
	}
	if delta != "" {
		c.sent[index].hasArgumentBytes = true
	}
	c.write(map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]any{"arguments": delta}}}}, nil)
}

func (c *cursorChunkEncoder) ToolCallEnd(block *cursorwire.OutBlock) {
	index, ok := c.index[block]
	if !ok {
		return
	}
	sent := c.sent[index]
	call := map[string]any{"index": index}
	function := map[string]any{}
	if sent.id == "" && block.ID != "" {
		call["id"] = block.ID
	}
	if sent.name == "" && block.Name != "" {
		function["name"] = block.Name
	}
	if !sent.hasArgumentBytes {
		function["arguments"] = cursorStringifyArgs(block.Arguments)
	}
	if len(function) > 0 {
		call["function"] = function
	}
	if len(call) > 1 {
		c.write(map[string]any{"tool_calls": []any{call}}, nil)
	}
}

// finish writes the finish chunk, the usage chunk and [DONE].
func (c *cursorChunkEncoder) finish(output *cursorwire.Output) error {
	c.write(map[string]any{}, cursorFinishReason(output.StopReason, len(c.sent) > 0))
	if c.err != nil {
		return c.err
	}
	if c.err = c.emit(cursorJSON(map[string]any{"id": c.id, "object": "chat.completion.chunk", "created": c.created, "model": c.model,
		"system_fingerprint": nil, "choices": []any{}, "usage": cursorUsageJSON(output.Usage)})); c.err != nil {
		return c.err
	}
	c.err = c.emit([]byte("[DONE]"))
	return c.err
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// cursorDiscoveryTimeout is fetchCursorUsableModels' default per-RPC deadline;
// AvailableModels gets at most 2s of it.
const cursorDiscoveryTimeout = 5 * time.Second

// FetchCursorModels lists the account's models as NeoPi's cursor discovery
// does: GetUsableModels, AvailableModels and GetDefaultModelForCli in
// parallel, resolved into NeoPi records.
func FetchCursorModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	apiKey := cursorAccessToken(auth)
	if apiKey == "" {
		return nil, errors.New("cursor models: account has no access token")
	}
	override := cursorBaseURL(auth)
	baseURL := override
	if baseURL == "" {
		baseURL = cursorwire.DefaultBaseURL
	}
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	available := &cursorwire.AvailableModelsRequest{IncludeLongContextModels: true, UseModelParameters: ptrTrue(), DoNotUseMarkdown: ptrTrue(),
		UseCloudAgentEffortModes: ptrTrue()}
	type result struct {
		index int
		body  []byte
	}
	calls := []struct {
		path    string
		body    []byte
		timeout time.Duration
	}{
		{cursorwire.GetUsableModelsPath, nil, cursorDiscoveryTimeout},
		{cursorwire.AvailableModelsPath, available.Marshal(), 2 * time.Second},
		{cursorwire.GetDefaultModelPath, nil, cursorDiscoveryTimeout},
	}
	results := make(chan result, len(calls))
	for i, call := range calls {
		go func() {
			results <- result{index: i, body: cursorUnary(ctx, client, baseURL+call.path, apiKey, call.body, call.timeout)}
		}()
	}
	bodies := make([][]byte, len(calls))
	for range calls {
		r := <-results
		bodies[r.index] = r.body
	}
	models, ok := registry.CursorDiscoveredModels(registry.CursorDiscoveryResponses{Usable: bodies[0], Available: bodies[1], Default: bodies[2]}, override, time.Now())
	if !ok {
		return nil, errors.New("cursor models: neither GetUsableModels nor AvailableModels answered")
	}
	return models, nil
}

func ptrTrue() *bool {
	value := true
	return &value
}

// cursorUnary is fetchCursorUnary: one unary RPC under its deadline; a
// transport failure or non-2xx status yields nil.
func cursorUnary(ctx context.Context, client *http.Client, url, apiKey string, body []byte, timeout time.Duration) []byte {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	cursorwire.SetFetchHeaders(request.Header)
	cursorwire.SetClientHeaders(request.Header, apiKey, cursorwire.ContentTypeProto)
	request.Header.Set("Connect-Protocol-Version", "1")
	request.Header.Set("X-Request-Id", uuid.NewString())
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return nil
	}
	defer func() { _ = decoded.Close() }()
	payload, err := io.ReadAll(decoded)
	if err != nil {
		return nil
	}
	if payload == nil {
		payload = []byte{}
	}
	return payload
}
