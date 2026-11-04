package neopool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/tidwall/gjson"
)

const (
	defaultProbePrompt = "Reply with the single word OK."
	maxProbePrompt     = 2000
	maxProbeReply      = 200
	// probeTimeout caps one test request. Repo rules forbid timeouts on upstream
	// work, but this is an operator-initiated diagnostic: a hung provider must
	// answer "not working" rather than pin the dashboard, so the request context
	// carries a deadline here and nowhere else.
	probeTimeout = 45 * time.Second
)

// probeModelHints mark the cheap, fast model of a provider; probeModelAvoid
// marks ids that are not chat models at all (a video model answers 400 to a
// chat request, which says nothing about the key).
var probeModelHints = []string{"mini", "haiku", "flash", "fast", "small", "nano", "lite"}
var probeModelAvoid = []string{"image", "imagine", "video", "audio", "tts", "stt", "speech", "whisper", "embed", "rerank", "moderation", "transcri", "dall-e", "sora", "realtime", "ocr"}

// Endpoints a test can speak: the protocol name the engine translates from.
var probeProtocols = map[string]string{"chat": "openai", "responses": "openai-response", "messages": "claude"}

// ProbeFunc sends one non-streaming request in the given protocol ("openai",
// "openai-response" or "claude") through the engine, pinned to the engine
// credential authID and the engine provider. It returns the response body, or
// an error with the upstream HTTP status when one is known (0 otherwise). The
// probe is budget-free: nothing is reserved in or settled against the pool
// ledger.
type ProbeFunc func(ctx context.Context, authID, provider, protocol string, body []byte) (response []byte, status int, err error)

// SetAccessProbe attaches the engine execution seam behind POST /access/:id/test.
func (h *HTTP) SetAccessProbe(fn ProbeFunc) { h.probe = fn }

// AccessProbe is the outcome of the last test of an item, as listed on AccessItem.
type AccessProbe struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	CheckedAt int64  `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

// ProbeStage is one step of a test as the dashboard draws it: the credential,
// the request, the provider's answer, and the parsed reply.
type ProbeStage struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// AccessProbeResult is the POST /v8/neo/access/:id/test response. CheckedAt is
// epoch milliseconds, like every other timestamp in the access API. Raw is the
// provider's answer (or the engine's error text) truncated, for operators who
// want to see exactly what came back; ProviderMessage is its error.message
// when the answer was a JSON error.
type AccessProbeResult struct {
	OK              bool            `json:"ok"`
	Model           string          `json:"model"`
	Endpoint        string          `json:"endpoint"`
	LatencyMs       int64           `json:"latencyMs"`
	Status          int             `json:"status,omitempty"`
	Reply           string          `json:"reply,omitempty"`
	Error           string          `json:"error,omitempty"`
	ProviderMessage string          `json:"providerMessage,omitempty"`
	Raw             string          `json:"raw,omitempty"`
	Request         json.RawMessage `json:"request,omitempty"`
	Stages          []ProbeStage    `json:"stages"`
	CheckedAt       int64           `json:"checkedAt"`
}

const maxProbeRaw = 4096

// probeLog keeps the last probe per item in memory; it is diagnostic, not state.
type probeLog struct {
	mu   sync.Mutex
	last map[string]AccessProbe
}

func (l *probeLog) put(id string, r AccessProbeResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == nil {
		l.last = map[string]AccessProbe{}
	}
	l.last[id] = AccessProbe{OK: r.OK, LatencyMs: r.LatencyMs, CheckedAt: r.CheckedAt, Error: r.Error}
}

func (l *probeLog) get(id string) *AccessProbe {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.last[id]; ok {
		return &p
	}
	return nil
}

// pickProbeModel chooses the model to test: among the models that look cheap,
// the one whose version numbers are highest (so a retired 2024 Haiku loses to
// the current one); else the first listed model; else "".
func pickProbeModel(models []string) string {
	best, bestKey := "", ""
	for _, id := range models {
		lower := strings.ToLower(id)
		cheap := false
		for _, hint := range probeModelHints {
			if strings.Contains(lower, hint) {
				cheap = true
				break
			}
		}
		if !cheap || probeAvoid(lower) {
			continue
		}
		if key := versionKey(lower); best == "" || key > bestKey {
			best, bestKey = id, key
		}
	}
	if best != "" {
		return best
	}
	for _, id := range models {
		if !probeAvoid(strings.ToLower(id)) {
			return id
		}
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
}

func probeAvoid(lower string) bool {
	for _, bad := range probeModelAvoid {
		if strings.Contains(lower, bad) {
			return true
		}
	}
	return false
}

// versionKey turns the digits of a model id into a string that sorts newer
// first lexicographically: "claude-haiku-4-5-20251001" → "0004.0005.20251001".
func versionKey(id string) string {
	var parts []string
	num := ""
	flush := func() {
		if num != "" {
			if len(num) < 4 {
				num = strings.Repeat("0", 4-len(num)) + num
			}
			parts = append(parts, num)
			num = ""
		}
	}
	for _, r := range id {
		if r >= '0' && r <= '9' {
			num += string(r)
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(parts, ".")
}

// probeFailure explains a failed test without echoing upstream text, which can
// carry key fragments or account details.
func probeFailure(status int, timedOut bool) string {
	switch {
	case timedOut:
		return "The provider did not answer within 45 seconds."
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "The provider rejected this credential. Sign in again or replace the key."
	case status == http.StatusPaymentRequired:
		return "The provider wants payment or credits before this key can be used."
	case status == http.StatusNotFound:
		return "The provider does not offer this model to this access."
	case status == http.StatusTooManyRequests:
		return "The provider is rate limiting this access or its quota is used up."
	case status >= 500:
		return "The provider reported an error on its side. Try again later."
	case status >= 400:
		return "The provider refused the test request."
	}
	return "The test request could not reach the provider."
}

// shapeProbe turns an engine outcome into the response. Pure, so tests can feed
// it any fake executor result.
func shapeProbe(model, endpoint string, request []byte, latency time.Duration, status int, body []byte, err error, timedOut bool, now time.Time) AccessProbeResult {
	r := AccessProbeResult{Model: model, Endpoint: endpoint, Request: request, LatencyMs: latency.Milliseconds(), Status: status, CheckedAt: now.UnixMilli()}
	stage := func(id, label string, ok bool, detail string) {
		r.Stages = append(r.Stages, ProbeStage{ID: id, Label: label, OK: ok, Detail: detail})
	}
	ms := latency.Milliseconds()
	if err != nil {
		r.Error = probeFailure(status, timedOut)
		// Scrub first, then read: the human sentence must never carry a key.
		r.Raw = truncateRaw(err.Error())
		r.ProviderMessage = providerMessage(r.Raw)
		credentialOK := status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusPaymentRequired
		stage("credential", "Credential", credentialOK, map[bool]string{true: "accepted by the engine", false: "rejected by the provider"}[credentialOK])
		stage("request", "Request", true, endpointLabel(endpoint))
		switch {
		case timedOut:
			stage("provider", "Provider", false, "no answer within 45 s")
		case status == 0:
			stage("provider", "Provider", false, "could not be reached")
		default:
			stage("provider", "Provider", false, "HTTP "+strconvI(status)+" in "+strconvI64(ms)+" ms")
		}
		return r
	}
	r.OK = true
	if r.Status == 0 {
		r.Status = http.StatusOK
	}
	r.Reply = probeReply(endpoint, body)
	r.Raw = truncateRaw(string(body))
	stage("credential", "Credential", true, "accepted")
	stage("request", "Request", true, endpointLabel(endpoint))
	stage("provider", "Provider", true, "HTTP "+strconvI(r.Status)+" in "+strconvI64(ms)+" ms")
	if r.Reply == "" {
		stage("response", "Response", false, "answered, but no assistant text was found in the body")
		return r
	}
	stage("response", "Response", true, r.Reply)
	return r
}

func endpointLabel(endpoint string) string {
	switch endpoint {
	case "responses":
		return "OpenAI Responses"
	case "messages":
		return "Anthropic Messages"
	}
	return "OpenAI Chat Completions"
}

func strconvI(n int) string     { return strconvI64(int64(n)) }
func strconvI64(n int64) string { return json.Number(fmtInt(n)).String() }

func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// truncateRaw keeps the start of a provider body for display and strips
// anything that looks like a bearer token or API key.
func truncateRaw(raw string) string {
	raw = strings.TrimSpace(raw)
	for _, prefix := range []string{"sk-", "xai-", "Bearer ", "nk_"} {
		for {
			i := strings.Index(raw, prefix)
			if i < 0 {
				break
			}
			j := i + len(prefix)
			for j < len(raw) && (raw[j] == '-' || raw[j] == '_' || raw[j] >= '0' && raw[j] <= '9' || raw[j] >= 'A' && raw[j] <= 'Z' || raw[j] >= 'a' && raw[j] <= 'z') {
				j++
			}
			if j-i <= len(prefix)+4 {
				break
			}
			raw = raw[:i] + prefix + "…" + raw[j:]
		}
	}
	if utf8.RuneCountInString(raw) > maxProbeRaw {
		return string([]rune(raw)[:maxProbeRaw]) + "…"
	}
	return raw
}

// providerMessage digs the human sentence out of an error body when there is one.
func providerMessage(raw string) string {
	i := strings.Index(raw, "{")
	if i < 0 {
		return ""
	}
	j := strings.LastIndex(raw, "}")
	if j <= i {
		return ""
	}
	for _, path := range []string{"error.message", "message", "error", "detail"} {
		if v := gjson.Get(raw[i:j+1], path); v.Exists() && v.Type == gjson.String && strings.TrimSpace(v.String()) != "" {
			return strings.TrimSpace(v.String())
		}
	}
	return ""
}

// probeReply extracts the assistant text of a response in the endpoint's shape.
func probeReply(endpoint string, body []byte) string {
	var text string
	switch endpoint {
	case "responses":
		text = gjson.GetBytes(body, "output_text").String()
		if text == "" {
			gjson.GetBytes(body, "output").ForEach(func(_, item gjson.Result) bool {
				item.Get("content").ForEach(func(_, c gjson.Result) bool {
					if t := c.Get("text").String(); t != "" {
						text += t
					}
					return true
				})
				return true
			})
		}
	case "messages":
		gjson.GetBytes(body, "content").ForEach(func(_, c gjson.Result) bool {
			if c.Get("type").String() == "text" {
				text += c.Get("text").String()
			}
			return true
		})
	default:
		text = gjson.GetBytes(body, "choices.0.message.content").String()
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > maxProbeReply {
		text = string([]rune(text)[:maxProbeReply])
	}
	return text
}

// probeRequest is the optional body of POST /access/:id/test. Payload, when
// given, is sent as-is (model filled in if missing, stream forced off) so an
// operator can try exactly the body a client would send.
type probeRequest struct {
	Model    string          `json:"model"`
	Prompt   string          `json:"prompt"`
	Endpoint string          `json:"endpoint"`
	Payload  json.RawMessage `json:"payload"`
}

// accessTest sends one tiny request pinned to exactly this item's credential.
func (h *HTTP) accessTest(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	a, owner, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	// A test lifts the credential's engine hold and spends a real request.
	if p.Role != neo.RoleAdmin && !(owner == p.UserID && p.Role == neo.RoleOwner) {
		poolError(c, http.StatusForbidden, "Only the contributor or an administrator can test this access.")
		return
	}
	if h.probe == nil {
		poolError(c, http.StatusServiceUnavailable, "The engine is unavailable; try again in a moment.")
		return
	}
	var req probeRequest
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		err = json.Unmarshal(raw, &req)
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	// A bare Test (no body, or a body that names nothing) uses the saved recipe
	// for this account when there is one.
	if err == nil && req.Model == "" && req.Prompt == "" && req.Endpoint == "" && len(req.Payload) == 0 {
		if saved, errSaved := h.pool.ProbeConfig(c.Request.Context(), AccountID(a.ID)); errSaved == nil && saved != nil {
			req = probeRequest{Model: saved.Model, Prompt: saved.Prompt, Endpoint: saved.Endpoint, Payload: saved.Payload}
		}
	}
	if req.Endpoint == "" {
		req.Endpoint = "chat"
	}
	protocol, known := probeProtocols[req.Endpoint]
	if err != nil || utf8.RuneCountInString(req.Prompt) > maxProbePrompt || !known {
		poolError(c, http.StatusBadRequest, "Send nothing, or {model?, prompt? (≤2000 chars), endpoint?: chat|responses|messages, payload?: object}.")
		return
	}
	if req.Prompt == "" {
		req.Prompt = defaultProbePrompt
	}
	ctx := c.Request.Context()
	id := AccountID(a.ID)
	snapshot, err := h.pool.Snapshot(ctx)
	if err != nil {
		respondError(c, err)
		return
	}
	var served []string
	for _, account := range snapshot.Accounts {
		if account.ID == id {
			served = account.ModelIDs
		}
	}
	label := strings.TrimSpace(req.Model)
	if label == "" && len(req.Payload) > 0 {
		label = strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String())
	}
	if label == "" {
		label = pickProbeModel(served)
	}
	if label == "" {
		poolError(c, http.StatusBadRequest, "This access has no known models yet. Send {\"model\": ...} with the model to test.")
		return
	}
	// A listed model is addressed by its pool ID; anything else is taken as the
	// provider's own model name.
	upstream := label
	for _, m := range snapshot.Models {
		if m.ID == label && m.ProviderID == a.Provider {
			upstream = m.Name
			break
		}
	}
	var body []byte
	if len(req.Payload) > 0 {
		var payload map[string]any
		if errPayload := json.Unmarshal(req.Payload, &payload); errPayload != nil || payload == nil {
			poolError(c, http.StatusBadRequest, "payload must be a JSON object.")
			return
		}
		if _, has := payload["model"]; !has || strings.TrimSpace(req.Model) != "" {
			payload["model"] = upstream
		}
		payload["stream"] = false
		body, err = json.Marshal(payload)
	} else {
		switch req.Endpoint {
		case "responses":
			body, err = json.Marshal(map[string]any{"model": upstream, "input": req.Prompt, "max_output_tokens": 16, "stream": false})
		case "messages":
			body, err = json.Marshal(map[string]any{"model": upstream, "max_tokens": 8, "messages": []map[string]string{{"role": "user", "content": req.Prompt}}, "stream": false})
		default:
			body, err = json.Marshal(map[string]any{"model": upstream, "messages": []map[string]string{{"role": "user", "content": req.Prompt}}, "max_tokens": 8, "stream": false})
		}
	}
	if err != nil {
		respondError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	started := time.Now()
	response, status, err := h.probe(ctx, a.ID, a.Provider, protocol, body)
	result := shapeProbe(label, req.Endpoint, body, time.Since(started), status, response, err, errors.Is(ctx.Err(), context.DeadlineExceeded), h.pool.now())
	h.probes.put(id, result)
	c.JSON(http.StatusOK, result)
}

// ---- saved test recipe ----

// probeSchema keeps the per-account test recipe: which endpoint, model and
// prompt or payload the plain Test button sends. Operators know which model
// their key may use (a free one on a portal without credits, say); the
// engine cannot.
const probeSchema = `CREATE TABLE IF NOT EXISTS neo_access_probe (account_id TEXT PRIMARY KEY, endpoint TEXT NOT NULL, model TEXT NOT NULL, prompt TEXT NOT NULL, payload TEXT NOT NULL, updated_at INTEGER NOT NULL);`

// ProbeConfig is the saved recipe. Payload is a JSON object or empty.
type ProbeConfig struct {
	Endpoint  string          `json:"endpoint"`
	Model     string          `json:"model"`
	Prompt    string          `json:"prompt,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	UpdatedAt int64           `json:"updatedAt"`
}

func (s *Store) ProbeConfig(ctx context.Context, accountID string) (*ProbeConfig, error) {
	var cfg ProbeConfig
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT endpoint,model,prompt,payload,updated_at FROM neo_access_probe WHERE account_id=?`, accountID).Scan(&cfg.Endpoint, &cfg.Model, &cfg.Prompt, &payload, &cfg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload) != "" {
		cfg.Payload = json.RawMessage(payload)
	}
	return &cfg, nil
}

func (s *Store) SaveProbeConfig(ctx context.Context, accountID string, cfg ProbeConfig) error {
	payload := ""
	if len(cfg.Payload) > 0 {
		payload = string(cfg.Payload)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO neo_access_probe(account_id,endpoint,model,prompt,payload,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET endpoint=excluded.endpoint, model=excluded.model, prompt=excluded.prompt, payload=excluded.payload, updated_at=excluded.updated_at`, accountID, cfg.Endpoint, cfg.Model, cfg.Prompt, payload, s.now().UnixMilli())
	return err
}

func (s *Store) DeleteProbeConfig(ctx context.Context, accountID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM neo_access_probe WHERE account_id=?`, accountID)
	return err
}

// accessTestConfig reads, saves or clears the saved recipe for an item.
func (h *HTTP) accessTestConfig(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	a, owner, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	id := AccountID(a.ID)
	switch c.Request.Method {
	case http.MethodGet:
		cfg, err := h.pool.ProbeConfig(c.Request.Context(), id)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"config": cfg})
		return
	}
	if p.Role != neo.RoleAdmin && !(owner == p.UserID && p.Role == neo.RoleOwner) {
		poolError(c, http.StatusForbidden, "Only the contributor or an administrator can change how this access is tested.")
		return
	}
	if c.Request.Method == http.MethodDelete {
		if err := h.pool.DeleteProbeConfig(c.Request.Context(), id); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
		return
	}
	var cfg ProbeConfig
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err == nil {
		err = json.Unmarshal(raw, &cfg)
	}
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.Prompt = strings.TrimSpace(cfg.Prompt)
	if cfg.Endpoint == "" {
		cfg.Endpoint = "chat"
	}
	_, known := probeProtocols[cfg.Endpoint]
	if err != nil || !known || cfg.Model == "" || utf8.RuneCountInString(cfg.Prompt) > maxProbePrompt {
		poolError(c, http.StatusBadRequest, "Send {endpoint: chat|responses|messages, model: text, prompt?: text, payload?: object}.")
		return
	}
	if len(cfg.Payload) > 0 {
		var obj map[string]any
		if errPayload := json.Unmarshal(cfg.Payload, &obj); errPayload != nil || obj == nil {
			poolError(c, http.StatusBadRequest, "payload must be a JSON object.")
			return
		}
	}
	if err := h.pool.SaveProbeConfig(c.Request.Context(), id, cfg); err != nil {
		respondError(c, err)
		return
	}
	saved, err := h.pool.ProbeConfig(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"config": saved})
}
