package neopool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
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

// probeModelHints mark the cheap, fast model of a provider.
var probeModelHints = []string{"mini", "haiku", "flash", "fast", "small", "nano", "lite"}

// ProbeFunc sends one non-streaming OpenAI Chat Completions request through the
// engine, pinned to the engine credential authID and the engine provider. It
// returns the response body, or an error with the upstream HTTP status when one
// is known (0 otherwise). The probe is budget-free: nothing is reserved in or
// settled against the pool ledger.
type ProbeFunc func(ctx context.Context, authID, provider string, body []byte) (response []byte, status int, err error)

// SetAccessProbe attaches the engine execution seam behind POST /access/:id/test.
func (h *HTTP) SetAccessProbe(fn ProbeFunc) { h.probe = fn }

// AccessProbe is the outcome of the last test of an item, as listed on AccessItem.
type AccessProbe struct {
	OK        bool   `json:"ok"`
	LatencyMs int64  `json:"latencyMs"`
	CheckedAt int64  `json:"checkedAt"`
	Error     string `json:"error,omitempty"`
}

// AccessProbeResult is the POST /v8/neo/access/:id/test response. CheckedAt is
// epoch milliseconds, like every other timestamp in the access API.
type AccessProbeResult struct {
	OK        bool   `json:"ok"`
	Model     string `json:"model"`
	LatencyMs int64  `json:"latencyMs"`
	Status    int    `json:"status,omitempty"`
	Reply     string `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
	CheckedAt int64  `json:"checkedAt"`
}

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
		if !cheap || strings.Contains(lower, "image") || strings.Contains(lower, "audio") || strings.Contains(lower, "embed") || strings.Contains(lower, "tts") {
			continue
		}
		if key := versionKey(lower); best == "" || key > bestKey {
			best, bestKey = id, key
		}
	}
	if best != "" {
		return best
	}
	if len(models) > 0 {
		return models[0]
	}
	return ""
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
func shapeProbe(model string, latency time.Duration, status int, body []byte, err error, timedOut bool, now time.Time) AccessProbeResult {
	r := AccessProbeResult{Model: model, LatencyMs: latency.Milliseconds(), Status: status, CheckedAt: now.UnixMilli()}
	if err != nil {
		r.Error = probeFailure(status, timedOut)
		return r
	}
	r.OK = true
	if r.Status == 0 {
		r.Status = http.StatusOK
	}
	r.Reply = probeReply(body)
	return r
}

// probeReply extracts the assistant text of a Chat Completions response.
func probeReply(body []byte) string {
	text := strings.TrimSpace(gjson.GetBytes(body, "choices.0.message.content").String())
	if utf8.RuneCountInString(text) > maxProbeReply {
		text = string([]rune(text)[:maxProbeReply])
	}
	return text
}

// probeRequest is the optional body of POST /access/:id/test.
type probeRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// accessTest sends one tiny request pinned to exactly this item's credential.
func (h *HTTP) accessTest(c *gin.Context) {
	p, ok := session(c)
	if !ok {
		return
	}
	a, _, ok := h.accessTarget(c, p)
	if !ok {
		return
	}
	if h.probe == nil {
		poolError(c, http.StatusServiceUnavailable, "The engine is unavailable; try again in a moment.")
		return
	}
	var req probeRequest
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10))
	if err == nil && len(strings.TrimSpace(string(raw))) > 0 {
		err = json.Unmarshal(raw, &req)
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if err != nil || utf8.RuneCountInString(req.Prompt) > maxProbePrompt {
		poolError(c, http.StatusBadRequest, "Send nothing, or {\"model\": text, \"prompt\": text of at most 2000 characters}.")
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
	body, err := json.Marshal(map[string]any{
		"model":      upstream,
		"messages":   []map[string]string{{"role": "user", "content": req.Prompt}},
		"max_tokens": 8,
		"stream":     false,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	started := time.Now()
	response, status, err := h.probe(ctx, a.ID, a.Provider, body)
	result := shapeProbe(label, time.Since(started), status, response, err, errors.Is(ctx.Err(), context.DeadlineExceeded), h.pool.now())
	h.probes.put(id, result)
	c.JSON(http.StatusOK, result)
}
