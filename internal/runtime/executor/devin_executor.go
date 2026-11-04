package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// DevinExecutor serves a Devin sign-in the way NeoPi's devin-agent provider
// does: GetUserJwt, AssignModel for router models, then one GetChatMessage
// Connect stream per turn, answered here as Anthropic Messages events.
type DevinExecutor struct {
	cfg *config.Config
	// newID mints execution and fallback cascade ids (crypto.randomUUID).
	newID func() string
}

// NewDevinExecutor builds the Devin executor.
func NewDevinExecutor(cfg *config.Config) *DevinExecutor {
	return &DevinExecutor{cfg: cfg, newID: uuid.NewString}
}

// Identifier returns the provider key.
func (e *DevinExecutor) Identifier() string { return "devin" }

// SupportsApplyPatch reports the executor contract.
func (e *DevinExecutor) SupportsApplyPatch() bool { return e != nil }

// devinAPIKey is the stored session credential.
func devinAPIKey(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	for _, key := range []string{"api_key", "session_token", "token"} {
		if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
			return value
		}
	}
	for _, key := range []string{"api_key", "session_token", "access_token", "token"} {
		if value, _ := auth.Metadata[key].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// devinModel is the NeoPi record of the routed account's model, else the
// bundled seed row.
func devinModel(req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil && info.NeoPi.API == "devin-agent" {
		return info.NeoPi
	}
	return registry.DevinBundledNeoPiModel(model)
}

// Refresh re-reads the account's plan and quota from GetUserStatus, as
// NeoPi's usage provider does. NeoPi's Devin credential has no refresh grant,
// so the session token is kept; the observation feeds the conductor's quota
// cooldown and restoration.
func (e *DevinExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, errors.New("devin executor: auth is nil")
	}
	baseURL := ""
	if auth.Attributes != nil {
		baseURL = auth.Attributes["base_url"]
	}
	usage, err := devinauth.FetchUsage(ctx, helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0), baseURL, devinAPIKey(auth))
	if err != nil {
		return auth, err
	}
	updated := auth.Clone()
	now := time.Now()
	devinauth.ApplyUsage(updated, usage, now)
	updated.LastRefreshedAt = now
	return updated, nil
}

// HttpRequest sends an arbitrary request through the account's proxy.
func (e *DevinExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, request *http.Request) (*http.Response, error) {
	if request == nil {
		return nil, fmt.Errorf("devin executor: request is nil")
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(request)
}

// CountTokens estimates locally; Cascade has no counting RPC.
func (e *DevinExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return NewOpenAICompatExecutor(e.Identifier(), e.cfg).CountTokens(ctx, auth, req, opts)
}

type devinPrepared struct {
	model      *registry.NeoPiModel
	body       gjson.Result
	translated []byte
	messages   []devinMessage
	// chatUID is the chat model uid sent upstream; open replaces it with the
	// AssignModel result for router models.
	chatUID   string
	cascadeID string
	apiKey    string
	// finalize applies payload rules to the native business view of the
	// final protobuf: the last semantic step before the request is sent.
	finalize func([]byte) []byte
}

func (e *DevinExecutor) prepare(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*devinPrepared, error) {
	apiKey := devinAPIKey(auth)
	if apiKey == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "devin: account has no credential"}
	}
	modelID := thinking.ParseSuffix(req.Model).ModelName
	model := devinModel(req, modelID)
	if model == nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("devin: model %s is not in the account's Devin catalog", modelID)}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatClaude, modelID, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "claude", e.Identifier()); err != nil {
		return nil, err
	}
	modelForRules := modelID
	finalize := func(view []byte) []byte {
		original := helps.DevinPayloadDefaultsSource(view, originalTranslated)
		return helps.NewPayloadFinalizer(e.cfg, e.Identifier(), modelForRules, "devin", "", original, req, opts)(view)
	}
	body := gjson.ParseBytes(translated)

	effort := devinEffort(body)
	// NeoPi's gateway fits a client's effort to the model's ladder
	// (normalizeGatewayThinking); a model without one keeps the requested
	// effort for the strict check below.
	if effort != "" && model.Reasoning && model.Thinking != nil && len(model.Thinking.Efforts) > 0 &&
		!containsString(model.Thinking.Efforts, effort) && model.Thinking.EffortMap[effort] == "" {
		if clamped := codexwire.ClampEffort(model.Thinking.Efforts, effort); clamped != "" {
			effort = clamped
		}
	}
	if effort == "" && model.Reasoning && model.Thinking != nil && model.Thinking.RequiresEffort {
		effort = devinDefaultEffort(model)
	}
	if effort != "" {
		if !model.Reasoning {
			return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("Model devin/%s does not support thinking", model.ID)}
		}
		if model.Thinking == nil || !containsString(model.Thinking.Efforts, effort) {
			var supported []string
			if model.Thinking != nil {
				supported = model.Thinking.Efforts
			}
			return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("Thinking effort %s is not supported by devin/%s. Supported efforts: %s", effort, model.ID, strings.Join(supported, ", "))}
		}
	}
	cascadeID := neoPiSession(opts, translated)
	if cascadeID == "" {
		cascadeID = e.newID()
	}
	return &devinPrepared{model: model, body: body, translated: translated, messages: devinTransform(devinMessages(body)),
		chatUID: devinWireModelID(model, effort), cascadeID: cascadeID, apiKey: apiKey, finalize: finalize}, nil
}

// devinUnary posts one unary Connect RPC with NeoPi's fetch identity and
// returns the decoded body. Inference RPCs carry NeoPi's user agent;
// discovery carries Bun's default one.
func devinUnary(ctx context.Context, client *http.Client, url, userAgent string, body []byte) (int, http.Header, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Accept", "*/*")
	request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	request.Header.Set("Connect-Protocol-Version", "1")
	request.Header.Set("Connection", "keep-alive")
	request.Header.Set("Content-Type", "application/proto")
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return response.StatusCode, response.Header, nil, err
	}
	defer func() { _ = decoded.Close() }()
	payload, err := io.ReadAll(io.LimitReader(decoded, devinwire.MaxFramePayload))
	return response.StatusCode, response.Header, payload, err
}

// FetchDevinModels lists the account's models as NeoPi's Devin discovery
// does: GetCliModelConfigs with the dev-channel identity and every display
// slot, normalized into NeoPi records. A configured base_url serves its own
// Cascade deployment, so its records carry that host.
func FetchDevinModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	apiKey := devinAPIKey(auth)
	if apiKey == "" {
		return nil, fmt.Errorf("devin models: account has no credential")
	}
	baseOverride := ""
	if auth.Attributes != nil {
		baseOverride = strings.TrimSpace(auth.Attributes["base_url"])
	}
	baseURL := strings.TrimRight(baseOverride, "/")
	if baseURL == "" {
		baseURL = devinwire.DefaultBaseURL
	}
	metadata := devinwire.DiscoveryMetadata(apiKey)
	metadata.SupportedModelDisplays = devinwire.SupportedModelDisplays
	status, _, payload, err := devinUnary(ctx, helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0), baseURL+devinwire.CliModelConfigsPath,
		devinwire.BunUserAgent, devinwire.EncodeCliModelConfigsRequest(metadata))
	if err != nil {
		return nil, fmt.Errorf("devin models: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("devin models: GetCliModelConfigs returned HTTP %d", status)
	}
	response, ok := devinwire.DecodeUnary(payload, devinwire.DecodeCliModelConfigs)
	if !ok {
		return nil, fmt.Errorf("devin models: GetCliModelConfigs response did not decode")
	}
	models := registry.DevinDiscoveredModels(response, baseOverride)
	if len(models) == 0 {
		// The backend gates the catalog on the pinned client identity; an
		// empty-but-200 response is the signature of a stale pin.
		return nil, fmt.Errorf("devin models: empty model catalog; the pinned client identity may be stale")
	}
	return models, nil
}

var devinHTMLBody = regexp.MustCompile(`(?i)^\s*(?:<!doctype\s+html\b|<html\b)`)

// devinErrorDetail is devinErrorDetail: a bounded readable error, never HTML
// or binary protobuf.
func devinErrorDetail(header http.Header, payload []byte) string {
	if strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/html") || !utf8Valid(payload) {
		return ""
	}
	text := strings.TrimSpace(string(payload))
	var decoded map[string]any
	if json.Unmarshal([]byte(text), &decoded) == nil {
		switch value := decoded["error"].(type) {
		case map[string]any:
			if message, ok := value["message"].(string); ok {
				text = strings.TrimSpace(message)
			} else if message, ok := decoded["message"].(string); ok {
				text = strings.TrimSpace(message)
			}
		case string:
			text = strings.TrimSpace(value)
		default:
			if message, ok := decoded["message"].(string); ok {
				text = strings.TrimSpace(message)
			}
		}
	}
	normalized := strings.Join(strings.Fields(text), " ")
	if normalized == "" || devinHTMLBody.MatchString(normalized) || hasControlCharacters(normalized) {
		return ""
	}
	runes := []rune(normalized)
	if len(runes) > 4096 {
		return string(runes[:4096])
	}
	return normalized
}

func utf8Valid(payload []byte) bool {
	return strings.ToValidUTF8(string(payload), "\uFFFD") == string(payload)
}

func hasControlCharacters(text string) bool {
	for _, r := range text {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

func devinHTTPError(operation string, status int, header http.Header, payload []byte, auth *cliproxyauth.Auth) error {
	msg := fmt.Sprintf("Devin %s error %d", operation, status)
	if text := http.StatusText(status); text != "" {
		msg += " " + text
	}
	if detail := devinErrorDetail(header, payload); detail != "" {
		msg += ": " + detail
	}
	err := statusErr{code: status, msg: msg}
	if status == http.StatusTooManyRequests {
		if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
			if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && seconds >= 0 {
				delay := time.Duration(seconds) * time.Second
				err.retryAfter = &delay
			}
		}
	}
	return devinQuotaScopedStatusError(err, auth, time.Now())
}

// devinAccountCap is NeoPi's ACCOUNT_SCOPED_403_PATTERN: a permission
// rejection naming an account cap that resets is a usage limit.
var devinAccountCap = regexp.MustCompile(`(?i)\b(?:overall|account|organization|team|workspace)\b[^\n]{0,40}\b(?:message |request )?rate.?limit\b|\byour\b[^\n]{0,30}\b(?:limit )?will reset\b`)

// devinTrailerStatus maps a Connect error code onto the HTTP status NeoProxy's
// scheduler reads.
func devinTrailerStatus(code, message string) int {
	switch strings.ToLower(code) {
	case "invalid_argument", "failed_precondition", "out_of_range":
		return http.StatusBadRequest
	case "unauthenticated":
		return http.StatusUnauthorized
	case "permission_denied":
		if devinAccountCap.MatchString(message) {
			return http.StatusTooManyRequests
		}
		return http.StatusForbidden
	case "resource_exhausted":
		return http.StatusTooManyRequests
	case "unavailable":
		return http.StatusServiceUnavailable
	case "deadline_exceeded":
		return http.StatusGatewayTimeout
	case "canceled":
		return 499
	case "not_found":
		return http.StatusNotFound
	}
	return http.StatusBadGateway
}

// devinQuotaScopedStatusError upgrades a 429 naming exhausted daily/weekly
// usage quota to credential scope, retrying at the observed reset.
func devinQuotaScopedStatusError(err statusErr, auth *cliproxyauth.Auth, now time.Time) statusErr {
	if err.code != http.StatusTooManyRequests {
		return err
	}
	period := helps.DevinUsageQuotaExhaustionPeriod(err.msg)
	if period == helps.DevinQuotaPeriodNone {
		return err
	}
	err.credentialScoped = true
	if err.retryAfter == nil && auth != nil {
		key := "daily_quota_reset_at"
		if period == helps.DevinQuotaPeriodWeekly {
			key = "weekly_quota_reset_at"
		}
		err.retryAfter = helps.DevinQuotaRetryAfter(auth.Quota.Signals[key], now)
	}
	return err
}

// open runs GetUserJwt, AssignModel for routers, and opens the chat stream.
func (e *DevinExecutor) open(ctx context.Context, client *http.Client, auth *cliproxyauth.Auth, prepared *devinPrepared) (*http.Response, error) {
	baseURL := strings.TrimRight(prepared.model.BaseURL, "/")
	if baseURL == "" {
		baseURL = devinwire.DefaultBaseURL
	}
	status, header, payload, err := devinUnary(ctx, client, baseURL+devinwire.UserJWTPath, openaiwire.NeoPiUserAgent, devinwire.EncodeUserJWTRequest(devinwire.CLIMetadata(prepared.apiKey, "")))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, devinHTTPError("auth", status, header, payload, auth)
	}
	jwt, ok := devinwire.DecodeUnary(payload, devinwire.DecodeUserJWTResponse)
	if !ok || jwt.UserJWT == "" {
		return nil, statusErr{code: http.StatusBadGateway, msg: "Devin auth error: GetUserJwt returned an empty user JWT"}
	}
	chatBase := baseURL
	if custom := strings.TrimRight(strings.TrimSpace(jwt.CustomAPIServerURL), "/"); custom != "" {
		chatBase = custom
	}
	var assignment *devinwire.Assignment
	chatUID := prepared.chatUID
	if prepared.model.DevinCompat != nil && prepared.model.DevinCompat.ModelRouter {
		router := prepared.model.RequestModelID
		if router == "" {
			router = prepared.model.ID
		}
		body := devinwire.EncodeAssignModelRequest(devinwire.CLIMetadata(prepared.apiKey, ""), router, prepared.cascadeID, devinRouterPrompt(prepared.messages))
		status, header, payload, err = devinUnary(ctx, client, chatBase+devinwire.AssignModelPath, openaiwire.NeoPiUserAgent, body)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, devinHTTPError("AssignModel", status, header, payload, auth)
		}
		assigned, decoded := devinwire.DecodeUnary(payload, devinwire.DecodeAssignModelResponse)
		if !decoded || assigned.JWT == "" || assigned.ModelUID == "" {
			return nil, statusErr{code: http.StatusBadGateway, msg: "Devin AssignModel error: response carried no assignment JWT and model uid"}
		}
		assignment, chatUID = &assigned, assigned.ModelUID
		prepared.chatUID = chatUID
	}
	request := devinChatRequest(prepared.model, prepared.body, prepared.messages, prepared.apiKey, jwt.UserJWT, prepared.cascadeID, e.newID(), chatUID, assignment)
	protoBytes, view, err := helps.FinalizeDevinPayload(request.Encode(), prepared.finalize)
	if err != nil {
		return nil, err
	}
	compress := len(protoBytes) >= devinwire.MinCompressedBodyBytes
	chatBody, flags := protoBytes, byte(0)
	if compress {
		if chatBody, err = devinwire.Gzip(protoBytes); err != nil {
			return nil, err
		}
		flags = devinwire.FlagCompressed
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, chatBase+devinwire.ChatPath, bytes.NewReader(devinwire.Frame(chatBody, flags)))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Accept", "*/*")
	httpRequest.Header.Set("Accept-Encoding", "identity")
	httpRequest.Header.Set("Connect-Accept-Encoding", "gzip")
	if compress {
		httpRequest.Header.Set("Connect-Content-Encoding", "gzip")
	}
	httpRequest.Header.Set("Connect-Protocol-Version", "1")
	httpRequest.Header.Set("Connection", "keep-alive")
	httpRequest.Header.Set("Content-Type", "application/connect+proto")
	httpRequest.Header.Set("User-Agent", "connect-go/1.18.1 (go1.26.3)")
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: httpRequest.URL.String(), Method: http.MethodPost, Headers: httpRequest.Header.Clone(),
		Body: view, Provider: e.Identifier(), AuthID: authID(auth)})
	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer func() { _ = response.Body.Close() }()
		payload, _ := io.ReadAll(io.LimitReader(response.Body, devinwire.MaxFramePayload))
		return nil, devinHTTPError("API", response.StatusCode, response.Header, payload, auth)
	}
	return response, nil
}

func authID(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

// devinEvents turns Cascade response frames into Anthropic Messages events.
type devinEvents struct {
	emit       func([]byte) error
	model      string
	started    bool
	messageID  string
	open       string // "", "text", "thinking", "tool"
	index      int
	signature  string
	toolID     string
	toolJSON   map[string]string
	toolNames  map[string]string
	sawTool    bool
	stopReason int
	usage      *devinwire.Usage
}

func (d *devinEvents) send(event string) error { return d.emit([]byte(event)) }

func (d *devinEvents) start() error {
	if d.started {
		return nil
	}
	d.started = true
	id := d.messageID
	if id == "" {
		id = "msg_devin"
	}
	return d.send(string(jsjson.Object(
		jsjson.Field{Name: "type", Value: jsjson.Marshal("message_start")},
		jsjson.Field{Name: "message", Value: jsjson.Object(
			jsjson.Field{Name: "id", Value: jsjson.Marshal(id)},
			jsjson.Field{Name: "type", Value: jsjson.Marshal("message")},
			jsjson.Field{Name: "role", Value: jsjson.Marshal("assistant")},
			jsjson.Field{Name: "model", Value: jsjson.Marshal(d.model)},
			jsjson.Field{Name: "content", Value: json.RawMessage("[]")},
			jsjson.Field{Name: "stop_reason", Value: json.RawMessage("null")},
			jsjson.Field{Name: "stop_sequence", Value: json.RawMessage("null")},
			jsjson.Field{Name: "usage", Value: json.RawMessage(`{"input_tokens":0,"output_tokens":0}`)},
		)},
	)))
}

func (d *devinEvents) closeBlock() error {
	if d.open == "" {
		return nil
	}
	if d.open == "thinking" && d.signature != "" {
		if err := d.send(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"signature_delta","signature":%s}}`, d.index, jsjson.Marshal(d.signature))); err != nil {
			return err
		}
	}
	d.open, d.signature, d.toolID = "", "", ""
	err := d.send(fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, d.index))
	d.index++
	return err
}

func (d *devinEvents) openBlock(kind string, contentBlock string) error {
	if err := d.closeBlock(); err != nil {
		return err
	}
	d.open = kind
	return d.send(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":%s}`, d.index, contentBlock))
}

func (d *devinEvents) frame(msg devinwire.ChatResponse) error {
	if d.messageID == "" && msg.MessageID != "" {
		d.messageID = msg.MessageID
	}
	if err := d.start(); err != nil {
		return err
	}
	if msg.DeltaThinking != "" {
		if d.open != "thinking" {
			if err := d.openBlock("thinking", `{"type":"thinking","thinking":""}`); err != nil {
				return err
			}
		}
		if msg.DeltaSignature != "" {
			d.signature = msg.DeltaSignature
		}
		if err := d.send(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"thinking_delta","thinking":%s}}`, d.index, jsjson.Marshal(msg.DeltaThinking))); err != nil {
			return err
		}
	}
	if msg.DeltaText != "" {
		if d.open != "text" {
			if err := d.openBlock("text", `{"type":"text","text":""}`); err != nil {
				return err
			}
		}
		if err := d.send(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%s}}`, d.index, jsjson.Marshal(msg.DeltaText))); err != nil {
			return err
		}
	}
	for _, call := range msg.DeltaToolCalls {
		id := call.ID
		if id == "" {
			id = d.toolID
		}
		if id == "" {
			continue
		}
		if call.Name != "" {
			d.toolNames[id] = call.Name
		}
		if d.open != "tool" || d.toolID != id {
			d.sawTool = true
			block := fmt.Sprintf(`{"type":"tool_use","id":%s,"name":%s,"input":{}}`, jsjson.Marshal(id), jsjson.Marshal(d.toolNames[id]))
			if err := d.openBlock("tool", block); err != nil {
				return err
			}
			d.toolID = id
		}
		if call.ArgumentsJSON == "" {
			continue
		}
		previous := d.toolJSON[id]
		accumulated := previous + call.ArgumentsJSON
		if strings.HasPrefix(call.ArgumentsJSON, previous) {
			accumulated = call.ArgumentsJSON
		}
		d.toolJSON[id] = accumulated
		if delta := accumulated[len(previous):]; delta != "" {
			if err := d.send(fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%s}}`, d.index, jsjson.Marshal(delta))); err != nil {
				return err
			}
		}
	}
	if msg.StopReason != devinwire.StopReasonUnspecified {
		d.stopReason = msg.StopReason
	}
	if msg.Usage != nil {
		d.usage = msg.Usage
	}
	return nil
}

func (d *devinEvents) finish() error {
	if err := d.start(); err != nil {
		return err
	}
	if err := d.closeBlock(); err != nil {
		return err
	}
	reason := "end_turn"
	switch {
	case d.sawTool:
		reason = "tool_use"
	case d.stopReason == devinwire.StopReasonMaxTokens:
		reason = "max_tokens"
	}
	usage := devinwire.Usage{}
	if d.usage != nil {
		usage = *d.usage
	}
	if err := d.send(fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}`,
		reason, usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite)); err != nil {
		return err
	}
	return d.send(`{"type":"message_stop"}`)
}

// consume reads the Connect stream and emits Anthropic events. served
// receives the model uid the server reports actually answering (NeoPi's
// upstreamModel).
func (e *DevinExecutor) consume(response *http.Response, prepared *devinPrepared, model string, served func(string), emit func([]byte) error) error {
	defer func() { _ = response.Body.Close() }()
	events := &devinEvents{emit: emit, model: model, toolJSON: map[string]string{}, toolNames: map[string]string{}}
	frames := devinwire.NewFrameReader(response.Body)
	firstToken := false
	for {
		flags, payload, err := frames.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if flags&devinwire.FlagCompressed != 0 {
			if payload, err = devinwire.Gunzip(payload); err != nil {
				return err
			}
		}
		if flags&devinwire.FlagEndStream != 0 {
			trailer := devinwire.ParseTrailerError(strings.TrimSpace(string(payload)))
			if trailer == nil {
				continue
			}
			log.WithFields(log.Fields{"provider": e.Identifier(), "model": prepared.model.ID, "code": trailer.Code, "message": trailer.Message,
				"raw_trailer": trailer.Raw, "had_output": firstToken}).Warn("devin: stream rejected via Connect trailer")
			return devinQuotaScopedStatusError(statusErr{code: devinTrailerStatus(trailer.Code, trailer.Message), msg: trailer.Formatted}, nil, time.Now())
		}
		msg, err := devinwire.DecodeChatResponse(payload)
		if err != nil {
			return err
		}
		if msg.ActualModelUID != "" {
			served(msg.ActualModelUID)
		}
		if msg.DeltaText != "" || msg.DeltaThinking != "" || len(msg.DeltaToolCalls) > 0 {
			firstToken = true
		}
		if err = events.frame(msg); err != nil {
			return err
		}
	}
	return events.finish()
}

func (e *DevinExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (result cliproxyexecutor.Response, err error) {
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return result, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := e.open(ctx, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)), auth, prepared)
	if err != nil {
		return result, err
	}
	reporter.SetUpstreamModel(prepared.chatUID)
	headers := response.Header.Clone()
	var transcript bytes.Buffer
	var usage helps.StreamUsageBuffer
	err = e.consume(response, prepared, req.Model, reporter.SetResponseModel, func(data []byte) error {
		line := append([]byte("data: "), data...)
		usage.ObserveClaudeStream(line)
		transcript.Write(neoPiFrame(data))
		return nil
	})
	if err != nil {
		if se, ok := err.(statusErr); ok && auth != nil {
			err = devinQuotaScopedStatusError(se, auth, time.Now())
		}
		usage.PublishFailure(ctx, reporter, err)
		return result, err
	}
	data := transcript.Bytes()
	format := cliproxyexecutor.ResponseFormatOrSource(opts)
	if format == sdktranslator.FormatClaude {
		if data, err = neoPiAggregateMessage(data); err != nil {
			return result, err
		}
	} else {
		var state any
		data = sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatClaude, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.translated, data, &state)
		if helps.ApplyPatchTranslationError(state) != nil {
			return result, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		if len(data) == 0 {
			return result, errors.New("devin: response translation failed")
		}
	}
	usage.Publish(ctx, reporter)
	return cliproxyexecutor.Response{Payload: data, Headers: headers}, nil
}

func (e *DevinExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, err := e.prepare(ctx, auth, req, opts)
	if err != nil {
		return nil, err
	}
	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.model.ID, auth)
	defer reporter.TrackFailure(ctx, &err)
	response, err := e.open(ctx, reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)), auth, prepared)
	if err != nil {
		return nil, err
	}
	reporter.SetUpstreamModel(prepared.chatUID)
	headers := response.Header.Clone()
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		var usage helps.StreamUsageBuffer
		var state any
		format := cliproxyexecutor.ResponseFormatOrSource(opts)
		helps.InitializeApplyPatchStream(ctx, sdktranslator.FormatClaude, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.translated, &state)
		consumeErr := e.consume(response, prepared, req.Model, reporter.SetResponseModel, func(data []byte) error {
			line := append([]byte("data: "), data...)
			usage.ObserveClaudeStream(line)
			chunks := [][]byte{neoPiFrame(data)}
			if format != sdktranslator.FormatClaude {
				chunks = sdktranslator.TranslateStream(ctx, sdktranslator.FormatClaude, format, req.Model, helps.ApplyPatchOriginalRequest(req, opts), prepared.translated, line, &state)
			}
			for _, chunk := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: bytes.Clone(chunk)}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if helps.ApplyPatchTranslationError(state) != nil {
				return statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
			}
			return nil
		})
		for _, chunk := range helps.FinalizeApplyPatchStream(state) {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
			}
		}
		if helps.ApplyPatchTranslationError(state) != nil {
			consumeErr = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
		}
		if consumeErr != nil {
			if se, ok := consumeErr.(statusErr); ok && auth != nil {
				consumeErr = devinQuotaScopedStatusError(se, auth, time.Now())
			}
			usage.PublishFailure(ctx, reporter, consumeErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: consumeErr}:
			case <-ctx.Done():
			}
			return
		}
		usage.Publish(ctx, reporter)
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}
