package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiretry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NeoPi's V2 compaction limits (compaction-v2-streaming.ts).
const (
	compactionV2MaxRetries         = 2
	compactionV2RetainedBudget     = 64_000
	compactionV2ImageTokenEstimate = 765
)

var compactionContextualPrefixes = []string{
	"<environment_context>", "<user_instructions>", "<additional_context>", "<skills", "<token_budget>", "<model_switch>",
}

// compactionV2Failure is an error, failed or incomplete event that ended the
// compaction stream; status is set for a standalone error event that has one.
type compactionV2Failure struct {
	message string
	status  int
}

func (e *compactionV2Failure) Error() string { return e.message }

// executeNeoPiCompact serves /responses/compact the way NeoPi compacts a
// Codex session (requestCompactionV2Streaming): the history plus a
// compaction_trigger runs as a standalone compaction turn, and the retained
// user messages plus the one compaction item become the replacement history,
// returned in the /responses/compact shape.
func (e *CodexExecutor) executeNeoPiCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, body []byte, reporter *helps.UsageReporter) ([]byte, error) {
	modelInfo, ok := cliproxyauth.ResolvedModelInfo(req)
	if !ok || modelInfo == nil || modelInfo.Codex == nil {
		return nil, fmt.Errorf("codex: model %s has no discovered NeoPi metadata", req.Model)
	}
	sessionID := codexNeoPiSessionID(req, opts)
	compaction := codexwire.StandaloneCompaction(uuid.NewString())
	conversation, options := codexwire.FromResponses(body, modelInfo.Codex)
	options.SessionID = sessionID
	options.Compaction = compaction
	accessToken, baseURL := codexCreds(auth)
	authDir := ""
	if e.cfg != nil {
		authDir = e.cfg.AuthDir
	}
	installID, err := neopiid.InstallID(authDir)
	if err != nil {
		return nil, err
	}
	do, dial := e.neoPiTransport(ctx, auth)
	var lastErr error
	for attempt := 0; attempt <= compactionV2MaxRetries; attempt++ {
		shaped, errShape := codexNeoPiSessions.Shape(modelInfo.Codex, conversation, options, codexwire.Call{
			AccessToken: accessToken, BaseURL: baseURL, InstallID: installID, Transport: codexwire.TransportWebsocket,
		})
		if errShape != nil {
			return nil, errShape
		}
		helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
			URL: shaped.URL, Method: http.MethodPost, Headers: shaped.Header.Clone(), Body: shaped.Body, Provider: e.Identifier(),
		})
		reporter.SetTranslatedReasoningEffort(shaped.Body, "codex")
		compactionItem, usage, errRun := e.runNeoPiCompaction(ctx, shaped, modelInfo.Codex, do, dial)
		if errRun == nil {
			codexNeoPiSessions.ResetAfterCompaction(shaped)
			if detail, okUsage := helps.ParseCodexUsage(usage); okUsage {
				reporter.Publish(ctx, detail)
			}
			return compactionV2Response(gjson.GetBytes(body, "input"), compactionItem, usage), nil
		}
		lastErr = errRun
		if ctx.Err() != nil || !isRetryableCompactionError(errRun) || attempt == compactionV2MaxRetries {
			break
		}
		if errWait := neopiretry.Sleep(ctx, time.Duration(1<<attempt)*time.Second); errWait != nil {
			return nil, errWait
		}
	}
	var failure *compactionV2Failure
	if errors.As(lastErr, &failure) && failure.status > 0 {
		return nil, statusErr{code: failure.status, msg: failure.message}
	}
	return nil, e.neoPiError(lastErr)
}

// runNeoPiCompaction is attemptCompactionV2Streaming over the Codex transport
// plus collectCompactionV2Events: exactly one compaction item and a completed
// response are required.
func (e *CodexExecutor) runNeoPiCompaction(ctx context.Context, shaped *codexwire.Request, model *registry.CodexModel, do func(*http.Request) (*http.Response, error), dial codexwire.DialFunc) (json.RawMessage, []byte, error) {
	runner := &codexwire.Turn{
		Sessions: codexNeoPiSessions,
		Request:  shaped,
		Model:    model,
		Finalize: func(body []byte) []byte { return helps.FinalizePayload(ctx, body) },
		Dial:     dial,
		Do:       do,
	}
	var items []json.RawMessage
	outputCount := 0
	var usage []byte
	sawCompleted := false
	err := runner.RunCompaction(ctx, func(event []byte) error {
		observeNeoPiQuota(ctx, event)
		helps.AppendAPIResponseChunk(ctx, e.cfg, event)
		root := gjson.ParseBytes(event)
		switch kind := root.Get("type").String(); kind {
		case "response.output_item.done":
			outputCount++
			if item := root.Get("item"); item.IsObject() && item.Get("type").String() == "compaction" {
				items = append(items, json.RawMessage(item.Raw))
			}
		case "response.completed", "response.done":
			sawCompleted = true
			usage = event
		case "response.failed", "response.incomplete":
			return &compactionV2Failure{message: compactionV2FailureMessage(root, kind)}
		case "error":
			failure := &compactionV2Failure{message: compactionV2FailureMessage(root, kind)}
			if status := root.Get("status"); status.Type == gjson.Number && !math.IsInf(status.Float(), 0) {
				failure.status = int(status.Int())
			}
			return failure
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if !sawCompleted {
		return nil, nil, errors.New("V2 compaction stream closed before response.completed")
	}
	if len(items) != 1 {
		return nil, nil, fmt.Errorf("V2 compaction expected exactly one compaction output item, got %d from %d output items", len(items), outputCount)
	}
	return items[0], usage, nil
}

func compactionV2FailureMessage(event gjson.Result, kind string) string {
	errorObject := event.Get("error")
	if !errorObject.IsObject() && event.Get("response.error").IsObject() {
		errorObject = event.Get("response.error")
	}
	source := event
	if errorObject.IsObject() {
		source = errorObject
	}
	message := ""
	if value := source.Get("message"); value.Type == gjson.String {
		message = value.String()
	}
	code := ""
	if errorObject.IsObject() {
		code = firstString(errorObject.Get("code"), errorObject.Get("type"))
	} else {
		code = firstString(event.Get("code"))
	}
	out := "V2 compaction stream " + kind
	if code != "" {
		out += " (" + code + ")"
	}
	if message != "" {
		out += ": " + message
	}
	return out
}

func firstString(values ...gjson.Result) string {
	for _, value := range values {
		if value.Type == gjson.String {
			return value.String()
		}
	}
	return ""
}

// isRetryableCompactionError is NeoPi's isRetryableCompactionError.
func isRetryableCompactionError(err error) bool {
	var httpErr *codexwire.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status != http.StatusUnauthorized && httpErr.Status != http.StatusForbidden && neopiretry.IsRetryableStatus(httpErr.Status)
	}
	var failure *compactionV2Failure
	if errors.As(err, &failure) && failure.status > 0 {
		return neopiretry.IsRetryableStatus(failure.status)
	}
	message := strings.ToLower(err.Error())
	for _, pattern := range []string{
		"timeout", "socket", "connection closed", "other side closed", "websocket closed", "stream closed before response.completed",
		"stream parse failed", "server_error", "internal_error", "overloaded", "service unavailable",
	} {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

// compactionV2Response is the /responses/compact payload built from NeoPi's
// replacement history (buildCompactionV2ReplacementHistory).
func compactionV2Response(input gjson.Result, compactionItem json.RawMessage, completed []byte) []byte {
	var retained []gjson.Result
	for _, item := range input.Array() {
		if isRetainedCompactionUserMessage(item) {
			retained = append(retained, item)
		}
	}
	history := truncateRetainedMessages(retained, compactionV2RetainedBudget)
	history = append(history, compactionItem)
	encoded, _ := json.Marshal(history)
	out := []byte(`{"object":"response.compaction"}`)
	out, _ = sjson.SetBytes(out, "id", "cmp_"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	out, _ = sjson.SetBytes(out, "created_at", time.Now().Unix())
	out, _ = sjson.SetRawBytes(out, "output", encoded)
	if usage := gjson.GetBytes(completed, "response.usage"); usage.IsObject() {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usage.Raw))
	}
	return out
}

func isRetainedCompactionUserMessage(item gjson.Result) bool {
	kind := item.Get("type")
	isMessage := kind.String() == "message" || (!kind.Exists() && item.Get("role").Type == gjson.String)
	if !isMessage || item.Get("role").String() != "user" {
		return false
	}
	for _, part := range item.Get("content").Array() {
		if part.Get("type").String() != "input_text" {
			continue
		}
		text := strings.ToLower(strings.TrimLeft(part.Get("text").String(), " \t\r\n"))
		for _, prefix := range compactionContextualPrefixes {
			if text != "" && strings.HasPrefix(text, prefix) {
				return false
			}
		}
	}
	return true
}

func jsLength(text string) int { return len(utf16.Encode([]rune(text))) }

func approxTokenCount(text string) int { return (jsLength(text) + 3) / 4 }

func messageContentTokens(item gjson.Result) int {
	tokens := 0
	for _, part := range item.Get("content").Array() {
		switch part.Get("type").String() {
		case "input_image":
			tokens += compactionV2ImageTokenEstimate
		case "input_text", "output_text":
			tokens += approxTokenCount(part.Get("text").String())
		}
	}
	return tokens
}

// truncateRetainedMessages is truncateRetainedMessagesForCompactionV2: the
// newest messages win the token budget, the oldest that fits partially is cut.
func truncateRetainedMessages(items []gjson.Result, budget int) []json.RawMessage {
	remaining := budget
	var reversed []json.RawMessage
	for i := len(items) - 1; i >= 0; i-- {
		if remaining == 0 {
			continue
		}
		count := max(messageContentTokens(items[i]), 1)
		if count <= remaining {
			reversed = append(reversed, json.RawMessage(items[i].Raw))
			remaining = max(0, remaining-count)
			continue
		}
		if truncated := truncateMessageText(items[i], remaining); truncated != nil {
			reversed = append(reversed, truncated)
			remaining = 0
		}
	}
	out := make([]json.RawMessage, len(reversed))
	for i, item := range reversed {
		out[len(reversed)-1-i] = item
	}
	return out
}

func truncateMessageText(item gjson.Result, budget int) json.RawMessage {
	remaining := budget
	var parts []json.RawMessage
	for _, part := range item.Get("content").Array() {
		if !part.IsObject() {
			continue
		}
		switch part.Get("type").String() {
		case "input_image":
			if remaining < compactionV2ImageTokenEstimate {
				continue
			}
			parts = append(parts, json.RawMessage(part.Raw))
			remaining = max(0, remaining-compactionV2ImageTokenEstimate)
		case "input_text", "output_text":
			if remaining == 0 {
				continue
			}
			text := part.Get("text").String()
			count := approxTokenCount(text)
			if count <= remaining {
				parts = append(parts, json.RawMessage(part.Raw))
				remaining = max(0, remaining-count)
				continue
			}
			truncated := truncateTextToTokens(text, remaining)
			remaining = 0
			if truncated != "" {
				updated, _ := sjson.SetBytes([]byte(part.Raw), "text", truncated)
				parts = append(parts, updated)
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	encoded, _ := json.Marshal(parts)
	out, _ := sjson.SetRawBytes([]byte(item.Raw), "content", encoded)
	return out
}

func truncateTextToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	units := utf16.Encode([]rune(text))
	maxChars := maxTokens * 4
	if len(units) <= maxChars {
		return text
	}
	omitted := max(1, approxTokenCount(text)-maxTokens)
	marker := fmt.Sprintf("…%d tokens truncated…", omitted)
	markerLength := jsLength(marker)
	if maxChars <= markerLength+2 {
		return string(utf16.Decode(units[:maxChars]))
	}
	side := max(1, (maxChars-markerLength)/2)
	return string(utf16.Decode(units[:side])) + marker + string(utf16.Decode(units[len(units)-side:]))
}
