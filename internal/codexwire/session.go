package codexwire

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

// Wire identity pinned to the captured NeoPi source.
const (
	ClientVersion   = "0.159.0"
	UserAgent       = codexauth.NeoPiUserAgent
	Originator      = "omp"
	DefaultBaseURL  = codexauth.DefaultBackendURL
	betaResponses   = "responses=experimental"
	betaWebsocketV2 = "responses_websockets=2026-02-06"
	jwtClaimPath    = "https://api.openai.com/auth"
)

// Transport selects the Codex transport framing a request is shaped for.
type Transport string

const (
	TransportSSE       Transport = "sse"
	TransportWebsocket Transport = "websocket"
)

var reservedMetadataKeys = map[string]bool{
	"installation_id": true, "x-codex-installation-id": true, "session_id": true, "thread_id": true,
	"turn_id": true, "window_id": true, "x-codex-window-id": true, "x-codex-turn-metadata": true,
	"x-codex-parent-thread-id": true, "x-openai-subagent": true, "request_kind": true, "compaction": true,
	"code_mode_tool_names": true, "tool_namespaces_info": true, "turn_started_at_unix_ms": true,
	"forked_from_thread_id": true, "parent_thread_id": true, "parent_turn_id": true, "subagent_kind": true,
	"thread_source": true, "sandbox": true, "workspaces": true,
}

type metadataSession struct {
	threadID      string
	windowID      string
	turnID        string
	turnStartedAt *int64
	turnStates    map[string]string
	// compactionOperationID and reuseTurn track the compaction lifecycle
	// (resolveCodexStartNewTurn).
	compactionOperationID string
	reuseTurn             bool
}

// Compaction is CodexCompactionRequestContext: one logical compaction's
// lifecycle metadata.
type Compaction struct {
	OperationID    string
	Trigger        string
	Reason         string
	Implementation string
	Phase          string
	Strategy       string
}

// StandaloneCompaction is the context of a client-requested compaction that
// owns its own throwaway turn: NeoPi's manual /compact
// (createCodexCompactionContext) on the V2 streaming implementation.
func StandaloneCompaction(operationID string) *Compaction {
	return &Compaction{
		OperationID: operationID, Trigger: "manual", Reason: "user_requested",
		Implementation: "responses_compaction_v2", Phase: "standalone_turn", Strategy: "memento",
	}
}

// Sessions holds NeoPi's process-wide Codex provider session state: per
// session thread/window identity, the current turn, and sticky turn-state.
type Sessions struct {
	mu       sync.Mutex
	metadata map[string]*metadataSession
	newID    func() string
	now      func() time.Time
	efforts  effortControls
	// transports is keyed by the credential/backend/model/session key.
	transports map[string]*transportState
}

// NewSessions returns empty provider session state.
func NewSessions() *Sessions {
	return &Sessions{metadata: map[string]*metadataSession{}, newID: uuid.NewString, now: time.Now}
}

// Call is one upstream Codex request's credential and routing identity.
type Call struct {
	AccessToken string
	// AccountID overrides the token's chatgpt_account_id claim.
	AccountID string
	BaseURL   string
	InstallID string
	Transport Transport
}

// Request is a Codex request shaped exactly as NeoPi sends it.
type Request struct {
	URL    string
	Header http.Header
	Body   []byte
	// SessionKey scopes transport state (turn-state, chaining) to the credential.
	SessionKey string
	// AccountID is the ChatGPT account the request is sent for.
	AccountID  string
	base       http.Header
	session    *metadataSession
	lite       bool
	sessionID  string
	compaction *Compaction
}

// ResetAfterCompaction is resetOpenAICodexHistoryAfterCompaction for the
// session a compaction ran in: its append chains restart and the next turn
// opens a fresh context window.
func (s *Sessions) ResetAfterCompaction(request *Request) {
	if request == nil || request.compaction == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, state := range s.transports {
		if strings.HasSuffix(key, ":"+request.sessionID) || strings.HasSuffix(key, ":"+request.sessionID+":lite") {
			state.resetAppend()
		}
	}
	if session := s.metadata[request.sessionID]; session != nil {
		session.windowID = s.newID()
		session.compactionOperationID = ""
		session.reuseTurn = request.compaction.Phase != "standalone_turn"
	}
}

// AccountIDFromToken is getCodexAccountId.
func AccountIDFromToken(token string) string {
	return jwtClaim(token, "chatgpt_account_id")
}

func jwtClaim(token, claim string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		if payload, err = base64.StdEncoding.DecodeString(parts[1]); err != nil {
			return ""
		}
	}
	return gjson.GetBytes(payload, strings.ReplaceAll(jwtClaimPath, ".", `\.`)+"."+claim).String()
}

func residencyFromToken(token string) string {
	for _, claim := range []string{"chatgpt_data_residency", "chatgpt_compute_residency"} {
		if value := strings.TrimSpace(jwtClaim(token, claim)); value != "" {
			return value
		}
	}
	return ""
}

// ResponsesURL is resolveCodexResponsesUrl.
func ResponsesURL(baseURL string) string {
	raw := baseURL
	if strings.TrimSpace(raw) == "" {
		raw = DefaultBaseURL
	}
	normalized := strings.TrimRight(raw, "/")
	switch {
	case strings.HasSuffix(normalized, "/codex/responses"):
		return normalized
	case strings.HasSuffix(normalized, "/codex"):
		return normalized + "/responses"
	}
	return normalized + "/codex/responses"
}

// toASCIIJSON is toAsciiJsonString: JSON with every char >= 0x7f \u-escaped.
func toASCIIJSON(value json.RawMessage) string {
	var out strings.Builder
	for _, unit := range utf16.Encode([]rune(string(value))) {
		if unit >= 0x7f {
			fmt.Fprintf(&out, `\u%04x`, unit)
			continue
		}
		out.WriteByte(byte(unit))
	}
	return out.String()
}

// isTurnContinuation is isCodexWithinTurnContinuation over Responses items:
// only tool outputs follow the last assistant output.
func isTurnContinuation(items []json.RawMessage) bool {
	for i := len(items) - 1; i >= 0; i-- {
		item := gjson.ParseBytes(items[i])
		if toolOutputKind(item.Get("type").String()) != "" {
			continue
		}
		switch item.Get("type").String() {
		case "function_call", "custom_tool_call", "computer_call", "reasoning":
			return true
		}
		return item.Get("role").String() == "assistant"
	}
	return false
}

func sessionKey(sessionID string, model *registry.CodexModel, accountID, token, baseURL string, lite bool) string {
	if sessionID == "" {
		return ""
	}
	credential := "account:" + accountID
	if accountID == "" {
		credential = "token:" + bunHash36(token)
	}
	suffix := ""
	if lite {
		suffix = ":lite"
	}
	return credential + ":" + baseURL + ":" + model.ID + ":" + sessionID + suffix
}

// Shape builds the upstream Codex request NeoPi would send for this turn.
func (s *Sessions) Shape(model *registry.CodexModel, ctx Context, opts Options, call Call) (*Request, error) {
	if model == nil {
		return nil, fmt.Errorf("codex: model is required")
	}
	if call.AccessToken == "" {
		return nil, fmt.Errorf("codex: access token is required")
	}
	accountID := call.AccountID
	if accountID == "" {
		accountID = AccountIDFromToken(call.AccessToken)
	}
	baseURL := call.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	body := buildBody(model, ctx, opts, accountID)
	sessionID := NormalizePromptCacheKey(opts.SessionID)
	key := sessionKey(sessionID, model, accountID, call.AccessToken, baseURL, opts.ResponsesLite)

	s.mu.Lock()
	defer s.mu.Unlock()
	if model.Compat != nil && model.Compat.SupportsConfigurationUpdate {
		applyStableEffort(&s.efforts, model.ID, sessionID, &body)
	}
	metadataID := sessionID
	if metadataID == "" {
		metadataID = s.newID()
	}
	session := s.metadata[metadataID]
	if session == nil {
		session = &metadataSession{threadID: s.newID(), windowID: s.newID(), turnStates: map[string]string{}}
		if sessionID != "" {
			s.metadata[metadataID] = session
		}
	}
	kind := "turn"
	var startNewTurn bool
	if compaction := opts.Compaction; compaction != nil {
		kind = "compaction"
		startsOperation := session.compactionOperationID != compaction.OperationID
		if startsOperation {
			session.reuseTurn = false
		}
		session.compactionOperationID = compaction.OperationID
		startNewTurn = compaction.Phase != "mid_turn" && startsOperation
	} else {
		reuse := session.reuseTurn
		session.reuseTurn = false
		session.compactionOperationID = ""
		startNewTurn = !reuse && !isTurnContinuation(ctx.Input)
	}
	standalone := opts.Compaction != nil && opts.Compaction.Phase == "standalone_turn"
	if startNewTurn && !standalone {
		clear(session.turnStates)
	}
	if startNewTurn || session.turnID == "" {
		session.turnID = s.newID()
		// No downstream message timestamp exists, so NeoPi falls back to Date.now().
		started := s.now().UnixMilli()
		session.turnStartedAt = &started
	}
	turnMetadata := object{
		{Name: "installation_id", Value: jsjson.Marshal(call.InstallID)},
		{Name: "session_id", Value: jsjson.Marshal(metadataID)},
		{Name: "thread_id", Value: jsjson.Marshal(session.threadID)},
		{Name: "turn_id", Value: jsjson.Marshal(session.turnID)},
		{Name: "window_id", Value: jsjson.Marshal(session.windowID)},
		{Name: "request_kind", Value: jsjson.Marshal(kind)},
	}
	parentTurnID := ""
	if strings.TrimSpace(opts.ParentTurnID) != "" {
		parentTurnID = opts.ParentTurnID
		turnMetadata.set("parent_turn_id", jsjson.Marshal(parentTurnID))
	}
	if compaction := opts.Compaction; compaction != nil {
		turnMetadata.set("compaction", jsjson.Object(
			jsjson.Field{Name: "trigger", Value: jsjson.Marshal(compaction.Trigger)},
			jsjson.Field{Name: "reason", Value: jsjson.Marshal(compaction.Reason)},
			jsjson.Field{Name: "implementation", Value: jsjson.Marshal(compaction.Implementation)},
			jsjson.Field{Name: "phase", Value: jsjson.Marshal(compaction.Phase)},
			jsjson.Field{Name: "strategy", Value: jsjson.Marshal(compaction.Strategy)},
		))
		// attemptCompactionV2Streaming: replayed history without output-only
		// statuses, then the trigger; never stored.
		var items []json.RawMessage
		for _, item := range body.get("input").Array() {
			replay := parseObject([]byte(item.Raw))
			switch replay.get("type").String() {
			case "message", "function_call", "custom_tool_call", "compaction", "compaction_summary":
				replay.del("status")
			}
			items = append(items, replay.bytes())
		}
		items = append(items, jsjson.Object(jsjson.Field{Name: "type", Value: jsjson.Marshal("compaction_trigger")}))
		body.set("input", jsjson.Marshal(items))
		body.set("store", jsjson.Marshal(false))
		body.set("stream", jsjson.Marshal(true))
	}
	if session.turnStartedAt != nil {
		turnMetadata.set("turn_started_at_unix_ms", jsjson.Marshal(*session.turnStartedAt))
	}
	for _, field := range opts.ClientMetadata {
		if !reservedMetadataKeys[field.Name] {
			turnMetadata.set(field.Name, field.Value)
		}
	}
	turnMetadataJSON := toASCIIJSON(turnMetadata.bytes())
	clientMetadata := object{
		{Name: "x-codex-installation-id", Value: jsjson.Marshal(call.InstallID)},
		{Name: "session_id", Value: jsjson.Marshal(metadataID)},
		{Name: "thread_id", Value: jsjson.Marshal(session.threadID)},
		{Name: "x-codex-window-id", Value: jsjson.Marshal(session.windowID)},
		{Name: "turn_id", Value: jsjson.Marshal(session.turnID)},
	}
	if parentTurnID != "" {
		clientMetadata.set("parent_turn_id", jsjson.Marshal(parentTurnID))
	}
	clientMetadata.set("x-codex-turn-metadata", jsjson.Marshal(turnMetadataJSON))
	body.set("client_metadata", clientMetadata.bytes())

	header := http.Header{}
	header.Set("Authorization", "Bearer "+call.AccessToken)
	if accountID != "" {
		header.Set("chatgpt-account-id", accountID)
	}
	tier := body.get("service_tier")
	hint := "model=" + model.ID
	if tier.Type == gjson.String && tier.String() != "" {
		hint += ";tier=" + tier.String()
	}
	header.Set("x-codex-routing-hint", hint)
	if residency := residencyFromToken(call.AccessToken); residency != "" {
		header.Set("x-openai-internal-codex-residency", residency)
	}
	header.Set("originator", Originator)
	header.Set("version", ClientVersion)
	header.Set("User-Agent", UserAgent)
	if sessionID != "" {
		header.Set("conversation_id", sessionID)
		header.Set("session_id", sessionID)
		header.Set("x-client-request-id", sessionID)
	}
	header.Set("session-id", metadataID)
	header.Set("thread-id", session.threadID)
	header.Set("x-codex-window-id", session.windowID)
	header.Set("x-codex-turn-metadata", turnMetadataJSON)
	// Standalone compaction owns a throwaway turn-state cell.
	cell := session
	if standalone {
		throwaway := *session
		throwaway.turnStates = map[string]string{}
		cell = &throwaway
	}
	request := &Request{
		URL: ResponsesURL(baseURL), Body: body.bytes(), SessionKey: key, AccountID: accountID,
		base: header, session: cell, lite: opts.ResponsesLite, sessionID: metadataID, compaction: opts.Compaction,
	}
	transport := call.Transport
	if transport == "" {
		transport = TransportSSE
	}
	request.Header = s.headersLocked(request, transport)
	return request, nil
}

// Headers is createCodexHeaders for one transport attempt: turn-state and
// x-models-etag are read live, as NeoPi reads them when each attempt opens.
func (s *Sessions) Headers(request *Request, transport Transport) http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headersLocked(request, transport)
}

func (s *Sessions) headersLocked(request *Request, transport Transport) http.Header {
	header := request.base.Clone()
	if transport == TransportWebsocket {
		header.Set("OpenAI-Beta", betaWebsocketV2)
	} else {
		header.Set("OpenAI-Beta", betaResponses)
	}
	key := request.SessionKey
	if state := request.session.turnStates[key]; key != "" && state != "" {
		header.Set("x-codex-turn-state", state)
	}
	if state := s.transports[key]; key != "" && state != nil && state.modelsETag != "" {
		header.Set("x-models-etag", state.modelsETag)
	}
	if request.lite {
		header.Set("x-openai-internal-codex-responses-lite", "true")
	}
	if transport == TransportSSE {
		header.Set("accept", "text/event-stream")
		header.Set("content-type", "application/json")
	}
	return header
}

// RecordResponseHeaders is updateCodexSessionMetadataFromHeaders: the first
// x-codex-turn-state of a turn sticks, and the latest x-models-etag is kept.
func (s *Sessions) RecordResponseHeaders(request *Request, header http.Header) {
	if request == nil || header == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := request.SessionKey
	if state := header.Get("x-codex-turn-state"); state != "" && request.session.turnStates[key] == "" {
		request.session.turnStates[key] = state
	}
	if etag := header.Get("x-models-etag"); etag != "" && key != "" {
		s.transport(key).modelsETag = etag
	}
}

// IsOfficialURL is isOfficialCodexApiUrl.
func IsOfficialURL(raw string) bool {
	if raw == "" {
		return true
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || !strings.EqualFold(scheme, "https") {
		return false
	}
	host, path, _ := strings.Cut(rest, "/")
	if !strings.EqualFold(host, "chatgpt.com") {
		return false
	}
	path = "/" + strings.TrimRight(path, "/")
	return path == "/backend-api" || strings.HasPrefix(path, "/backend-api/")
}
