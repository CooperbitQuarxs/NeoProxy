package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopidialect"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
)

// NeoPi's response side of the openai-completions provider (ai/src/providers/
// openai-completions.ts, the `for await (const chunk of terminalAwareStream)`
// loop in streamOpenAICompletionsOnce and the helpers it calls), run as a
// normalizer in front of NeoProxy's translators: every upstream frame is
// interpreted the way NeoPi interprets it, and what leaves is a canonical Chat
// Completions stream (one choice, `content`, `reasoning_content`, sequentially
// indexed `tool_calls`, one terminal finish_reason, one usage-only chunk and
// `[DONE]`) that the OpenAI -> client translators already understand.
//
// Not ported, deliberately: LiteLLM/Gemini signature capture (nothing on the
// replay side consumes them), parseJsonWithRepair of final tool arguments, and
// withReplaySafeStreamRetry. NeoPi's first-event, idle and post-finish
// watchdogs are not ported either: the repository allows no deadline once the
// upstream connection is up (AGENTS.md), so a stream ends on [DONE], EOF or the
// caller's cancellation.

const (
	// neoPiCompletionsEmptyLengthMessage is EMPTY_OLLAMA_LENGTH_COMPLETION_MESSAGE.
	neoPiCompletionsEmptyLengthMessage = "Model returned no content: prompt filled the context window; raise Ollama num_ctx or shorten the prompt."
	// neoPiCompletionsIncompleteMessage is the incomplete-stream ProviderResponseError.
	neoPiCompletionsIncompleteMessage = "OpenAI completions stream closed before a finish_reason was received"
	// neoPiMistralContentPartsSignature is MISTRAL_CONTENT_PARTS_SIGNATURE.
	neoPiMistralContentPartsSignature = "mistral-content-parts"
	// neoPiInBandPlaceholder is IN_BAND_DETAIL_PLACEHOLDER.
	neoPiInBandPlaceholder = "Provider returned an in-band provider error"
	// neoPiInBandDefaultDetail is the detail NeoPi uses when an error frame has none.
	neoPiInBandDefaultDetail = "Provider returned an in-band OpenAI completions stream error"
	neoPiInBandMaxDetail     = 4096
)

// neoPiJSSpace is the character class JavaScript's `\s` matches.
const neoPiJSSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

const neoPiDeepseekTokenBody = `<(?:｜|\|)[A-Za-z0-9_.｜|▁]{1,64}(?:｜|\|)>`

var (
	neoPiDeepseekToken   = regexp.MustCompile(neoPiDeepseekTokenBody)
	neoPiDeepseekAtStart = regexp.MustCompile(`^[` + neoPiJSSpace + `]*` + neoPiDeepseekTokenBody)
	neoPiDeepseekAtEnd   = regexp.MustCompile(neoPiDeepseekTokenBody + `[` + neoPiJSSpace + `]*$`)

	neoPiInBandRetryableText = regexp.MustCompile(`(?i)\brate.?limit|too many requests|too\s+many\s+concurren|service.{0,20}unavailable|temporarily\s+unavailable|server.?error|internal.?error|overloaded|capacity|throttl|retry\s+(?:your\s+)?request|please\s+retry`)
	neoPiInBandLeadingStatus = regexp.MustCompile(`(?i)^\s*(?:HTTP[/.]\d(?:\.\d)?\s+)?([45]\d{2})(?:\b|$)`)
	neoPiInBandNonRetryable  = regexp.MustCompile(`(?i)insufficient.?quota|usage.?limit|quota.?(?:exceeded|reached|insufficient)|invalid_request|content_filter|context_length|context_window|billing|balance`)
	neoPiCodeWordBoundary    = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	neoPiCodeSeparators      = regexp.MustCompile(`[-.` + neoPiJSSpace + `]+`)
	neoPiCodeUnderscores     = regexp.MustCompile(`_+`)
	neoPiMarkupTag           = regexp.MustCompile(`<[^>]*>`)
	neoPiControlRun          = regexp.MustCompile(`[\x00-\x1f\x7f]+`)
	neoPiSpaceRun            = regexp.MustCompile(`[` + neoPiJSSpace + `]+`)
	neoPiStatusCode          = regexp.MustCompile(`^\d{3}$`)

	neoPiOpaqueStatus     = regexp.MustCompile(`\b(?:429|402)\b`)
	neoPiOpaqueFraming    = regexp.MustCompile(`(?i)\b(?:http|https|status|error|code|response|message)\b`)
	neoPiOpaqueNoBody     = regexp.MustCompile(`(?i)\(?\bno body\b\)?`)
	neoPiOpaqueWord       = regexp.MustCompile(`(?i)[a-z\d]{3,}`)
	neoPiCNQuotaExhausted = regexp.MustCompile(`(?:使用|用量).{0,30}?上限|(?:额度|配额)已?(?:用|耗)(?:完|尽)|限额.{0,30}重置|余额不足`)
	neoPiCNTransientCap   = regexp.MustCompile(`速率.{0,30}上限|频率.{0,30}上限|每分钟.{0,30}上限|并发.{0,30}上限|使用.{0,30}(?:速率|频率|每分钟|并发).{0,30}上限`)
	neoPiCNThrottle       = regexp.MustCompile(`速率(?:限制|过快)|频率(?:过高|过快)|过于频繁|稍后[重再]试`)
)

// neoPiInBandRetryableStatusByCode is RETRYABLE_STATUS_BY_CODE.
var neoPiInBandRetryableStatusByCode = map[string]int{
	"rate_limit_error": 429, "rate_limit_exceeded": 429, "rate_limit": 429, "rate_limit_reached": 429,
	"rate_limited": 429, "ratelimit": 429, "too_many_requests": 429, "request_throttled": 429,
	"throttled": 429, "throttling": 429, "throttling_error": 429, "throttling_exception": 429,
	"throttling_allocation_quota": 429, "request_limit_exceeded": 429, "retry_later": 429,
	"overloaded_error": 503, "server_overloaded": 503, "model_overloaded": 503, "overloaded": 503,
	"service_unavailable": 503, "server_busy": 503, "high_demand": 503, "capacity_exceeded": 503,
}

// neoPiCompletionsErrorStatusByType is OPENAI_COMPLETIONS_ERROR_STATUS_BY_TYPE.
var neoPiCompletionsErrorStatusByType = map[string]int{
	"SERVICE_UNAVAILABLE": http.StatusServiceUnavailable,
	"TOO_MANY_REQUESTS":   http.StatusTooManyRequests,
	"REQUEST_TIMEOUT":     http.StatusRequestTimeout,
}

// neoPiCompletionsStreamSettings is the stream-facing slice of NeoPi's resolved
// compat policy (resolveOpenAICompatPolicy().stream, plus the model compat's
// mistral flag).
type neoPiCompletionsStreamSettings struct {
	provider string
	// stripDeepseek is policy.stream.stripSpecialTokens === "deepseek".
	stripDeepseek bool
	// cumulativeReasoning is policy.stream.reasoningDeltasMayBeCumulative.
	cumulativeReasoning bool
	// emptyLengthIsContextError is policy.stream.emptyLengthFinishIsContextError.
	emptyLengthIsContextError bool
	// mistralContentParts is model.compat.mistralReasoningContentParts.
	mistralContentParts bool
	// cachedPromptTokensHeader is parseFireworksCachedPromptTokens(responseHeaders).
	cachedPromptTokensHeader float64
	// healingPattern is policy.stream.markupHealingPattern: the
	// StreamMarkupHealing grammar run over the content channel ("" for none).
	healingPattern string
}

// neoPiCompletionsStreamSettingsFor resolves the settings for one row. compat
// is the row's base CompletionsCompat: NeoPi's `whenThinking` variant never
// differs from it on these fields in any shipped row.
func neoPiCompletionsStreamSettingsFor(provider string, compat *registry.NeoPiCompletionsCompat, header http.Header) neoPiCompletionsStreamSettings {
	settings := neoPiCompletionsStreamSettings{provider: provider}
	if header != nil {
		if value, err := strconv.ParseFloat(strings.TrimSpace(header.Get("fireworks-cached-prompt-tokens")), 64); err == nil && value > 0 && !math.IsInf(value, 0) {
			settings.cachedPromptTokensHeader = value
		}
	}
	if compat == nil {
		return settings
	}
	settings.stripDeepseek = compat.StripDeepseekSpecialTokens
	settings.cumulativeReasoning = compat.ReasoningDeltasMayBeCumulative
	settings.emptyLengthIsContextError = compat.EmptyLengthFinishIsContextError
	settings.mistralContentParts = compat.MistralReasoningContentParts
	settings.healingPattern = compat.StreamMarkupHealingPattern
	return settings
}

// neoPiCompletionsStreamError is a failed stream that still carries the usage
// NeoPi had parsed when it failed, as a usage-only chunk the caller can feed to
// its usage buffer before publishing the failure.
type neoPiCompletionsStreamError struct {
	statusErr
	usage []byte
}

// neoPiCompletionsFailureUsage is the usage-only chunk line carried by a failed
// stream, or nil.
func neoPiCompletionsFailureUsage(err error) []byte {
	var failure neoPiCompletionsStreamError
	if errors.As(err, &failure) {
		return failure.usage
	}
	return nil
}

// neoPiStreamBlockKind is the type of NeoPi's current content block.
type neoPiStreamBlockKind int

const (
	neoPiBlockText neoPiStreamBlockKind = iota + 1
	neoPiBlockThinking
	neoPiBlockToolCall
)

// neoPiStreamBlock is the part of a NeoPi content block that steers parsing.
type neoPiStreamBlock struct {
	kind neoPiStreamBlockKind
	// signature is a thinking block's thinkingSignature; nil is undefined.
	signature *string
	call      *neoPiStreamCall
}

// neoPiStreamCall is NeoPi's ToolCallStreamBlock.
type neoPiStreamCall struct {
	block    *neoPiStreamBlock
	id       string
	name     string
	outIndex int
	finished bool
	// partial and object are partialArgs: a string, or the merged object some
	// hosts stream; exactly one is active.
	partial string
	object  *neoPiJSObject
	// started reports that the opening tool_calls entry went downstream;
	// pending holds argument text that arrived before id and name were known.
	started bool
	pending strings.Builder
	// emitted counts argument bytes sent as string deltas.
	emitted int
}

// neoPiCompletionsNormalizer interprets one upstream stream.
type neoPiCompletionsNormalizer struct {
	settings neoPiCompletionsStreamSettings
	now      func() time.Time
	out      [][]byte

	id       string
	model    string
	created  int64
	roleSent bool

	current   *neoPiStreamBlock
	blocks    int
	visible   bool
	calls     []*neoPiStreamCall
	pending   []*neoPiStreamCall
	byIndex   map[int]*neoPiStreamCall
	unkeyed   []*neoPiStreamCall
	liteBlock *neoPiStreamBlock

	cumulative  map[string]string
	deepseekBuf string

	// healer is the row's StreamMarkupHealing; suppressHealedThinking turns
	// healed thinking off once the chunk carried a native reasoning channel.
	healer                 *neopidialect.Healer
	suppressHealedThinking bool

	finished      bool
	sawUsage      bool
	awaitTrailing bool
	usage         *neoPiStreamUsage
	repetition    string
	nativeError   string
	stop          string
	errorMessage  string
	done          bool
}

func newNeoPiCompletionsNormalizer(settings neoPiCompletionsStreamSettings, now func() time.Time) *neoPiCompletionsNormalizer {
	if now == nil {
		now = time.Now
	}
	n := &neoPiCompletionsNormalizer{
		settings:   settings,
		now:        now,
		byIndex:    map[int]*neoPiStreamCall{},
		cumulative: map[string]string{},
		stop:       "stop",
	}
	if settings.healingPattern != "" {
		n.healer = neopidialect.NewHealer(settings.healingPattern)
	}
	return n
}

// take drains the chunk lines produced so far.
func (n *neoPiCompletionsNormalizer) take() [][]byte {
	out := n.out
	n.out = nil
	return out
}

// fail wraps a terminal error with the usage parsed so far.
func (n *neoPiCompletionsNormalizer) fail(err statusErr) error {
	return neoPiCompletionsStreamError{statusErr: err, usage: n.usageLine()}
}

func (n *neoPiCompletionsNormalizer) responseError(message string) error {
	return n.fail(statusErr{code: http.StatusBadGateway, msg: n.settings.provider + ": " + message})
}

// feed interprets one decoded upstream frame (the loop body of NeoPi's
// `for await (const chunk of terminalAwareStream)`).
func (n *neoPiCompletionsNormalizer) feed(chunk gjson.Result) error {
	if !chunk.IsObject() {
		return nil
	}
	if err := n.inBandError(chunk); err != nil {
		return err
	}
	if n.id == "" {
		if id := chunk.Get("id"); id.Type == gjson.String {
			n.id = id.String()
		}
	}
	if n.model == "" {
		if model := chunk.Get("model"); model.Type == gjson.String {
			n.model = model.String()
		}
	}
	if created := chunk.Get("created"); n.created == 0 && created.Type == gjson.Number {
		n.created = created.Int()
	}
	chunkUsage := chunk.Get("usage")
	if chunkUsage.IsObject() {
		n.applyUsage(chunkUsage)
	}
	var choice gjson.Result
	if choices := chunk.Get("choices"); choices.IsArray() {
		choice = choices.Get("0")
	}
	if !choice.Exists() || choice.Type == gjson.Null {
		// A trailing usage-only chunk after finish_reason completes the response.
		if n.finished && n.sawUsage {
			n.done = true
		}
		return nil
	}
	if !chunkUsage.IsObject() {
		if choiceUsage := choice.Get("usage"); choiceUsage.IsObject() {
			n.applyUsage(choiceUsage)
		}
	}
	if n.repetition == "" {
		n.repetition = neoPiDetectRepetitionStop(choice)
	}
	if n.repetition != "" {
		n.markFinished()
	}
	if n.nativeError == "" {
		n.nativeError = neoPiNativeFinishReasonError(choice.Get("native_finish_reason"))
	}
	if n.nativeError != "" {
		n.stop = "error"
		n.errorMessage = n.nativeError
		n.markFinished()
	} else if reason := choice.Get("finish_reason"); neoPiTruthy(reason) {
		stop, message := neoPiMapStopReason(reason)
		n.stop = stop
		if message != "" {
			n.errorMessage = message
		}
		n.markFinished()
	}
	if delta := choice.Get("delta"); delta.IsObject() {
		n.delta(delta)
	}
	// Usage on the finish chunk without cache-read fields keeps draining
	// through the grace window for trailing usage details.
	if n.finished && n.sawUsage && !n.awaitTrailing {
		n.done = true
	}
	return nil
}

func (n *neoPiCompletionsNormalizer) markFinished() {
	n.finished = true
}

// delta is NeoPi's `if (choice.delta) { ... }` block.
func (n *neoPiCompletionsNormalizer) delta(delta gjson.Result) {
	// Reasoning arrives under the first non-empty of three aliases.
	var reasoningField, reasoningDelta string
	for _, field := range []string{"reasoning_content", "reasoning", "reasoning_text"} {
		if value := delta.Get(field); value.Type == gjson.String && value.String() != "" {
			reasoningField, reasoningDelta = field, value.String()
			break
		}
	}
	liteBlocks := neoPiLiteLLMThinkingBlocks(delta)
	var mistralParts []gjson.Result
	mistral := false
	if content := delta.Get("content"); n.settings.mistralContentParts && content.IsArray() {
		mistralParts, mistral = content.Array(), true
	}
	hasTypedThinking := false
	for _, part := range mistralParts {
		if part.Get("type").String() != "thinking" || !part.Get("thinking").IsArray() {
			continue
		}
		for _, inner := range part.Get("thinking").Array() {
			if text := inner.Get("text"); inner.Get("type").String() == "text" && text.Type == gjson.String && text.String() != "" {
				hasTypedThinking = true
			}
		}
	}
	if len(liteBlocks) > 0 {
		for _, entry := range liteBlocks {
			n.appendLiteLLMThinkingBlock(entry)
		}
		n.suppressHealedThinking = true
	} else if reasoningField != "" && !hasTypedThinking {
		source := "delta"
		if n.settings.cumulativeReasoning {
			source = "cumulative"
		}
		n.appendThinkingDelta(reasoningDelta, &reasoningField, source)
		n.suppressHealedThinking = true
	}
	structuredCalls := delta.Get("tool_calls").IsArray() && len(delta.Get("tool_calls").Array()) > 0
	if mistral {
		for _, part := range mistralParts {
			switch {
			case part.Get("type").String() == "thinking" && part.Get("thinking").IsArray():
				for _, inner := range part.Get("thinking").Array() {
					if text := inner.Get("text"); inner.Get("type").String() == "text" && text.Type == gjson.String && text.String() != "" {
						signature := neoPiMistralContentPartsSignature
						n.appendThinkingDelta(text.String(), &signature, "delta")
						n.suppressHealedThinking = true
					}
				}
			case part.Get("type").String() == "text" && part.Get("text").Type == gjson.String:
				n.appendContentText(part.Get("text").String(), structuredCalls)
			}
		}
	} else if text := neoPiStreamingContentText(delta.Get("content")); text != "" {
		n.appendContentText(text, structuredCalls)
	}
	if calls := delta.Get("tool_calls"); calls.IsArray() && len(calls.Array()) > 0 {
		n.toolCalls(calls.Array())
	}
}

// neoPiLiteLLMThinkingBlocks is getLiteLLMThinkingBlocksDelta.
func neoPiLiteLLMThinkingBlocks(delta gjson.Result) []gjson.Result {
	if direct := delta.Get("thinking_blocks"); direct.IsArray() && len(direct.Array()) > 0 {
		return direct.Array()
	}
	if mirrored := delta.Get("provider_specific_fields.thinking_blocks"); mirrored.IsArray() && len(mirrored.Array()) > 0 {
		return mirrored.Array()
	}
	return nil
}

// neoPiStreamingContentText is normalizeStreamingContentText.
func neoPiStreamingContentText(content gjson.Result) string {
	switch {
	case content.Type == gjson.String:
		return content.String()
	case content.IsArray():
		var out strings.Builder
		for _, part := range content.Array() {
			switch {
			case part.Type == gjson.String:
				out.WriteString(part.String())
			case part.IsObject():
				kind, text := part.Get("type"), part.Get("text")
				if (!kind.Exists() || kind.String() == "text" && kind.Type == gjson.String) && text.Type == gjson.String {
					out.WriteString(text.String())
				}
			}
		}
		return out.String()
	case content.IsObject():
		kind, text := content.Get("type"), content.Get("text")
		if (!kind.Exists() || kind.String() == "text" && kind.Type == gjson.String) && text.Type == gjson.String {
			return text.String()
		}
	}
	return ""
}

// appendContentText runs content-channel text through the row's
// StreamMarkupHealing, when it has one: healed text, thinking and leaked
// tool calls come out in stream order. A chunk that also carries structured
// tool_calls keeps those as the only calls (feedEventsWithoutCalls).
func (n *neoPiCompletionsNormalizer) appendContentText(text string, structuredCalls bool) {
	if n.healer == nil {
		n.appendProcessedText(text)
		return
	}
	events := n.healer.Feed
	if structuredCalls {
		events = n.healer.FeedWithoutCalls
	}
	n.applyHealing(events(text))
}

// applyHealing is emitHealingEvent for each event.
func (n *neoPiCompletionsNormalizer) applyHealing(events []neopidialect.HealEvent) {
	for _, event := range events {
		switch event.Kind {
		case neopidialect.KindText:
			n.appendProcessedText(event.Text)
		case neopidialect.KindThinking:
			if !n.suppressHealedThinking {
				n.appendThinkingDelta(event.Text, nil, "delta")
			}
		case neopidialect.KindToolCall:
			n.emitHealedToolCall(event)
		}
	}
}

// emitHealedToolCall is emitHealedToolCall: the open block ends and the
// reconstructed call goes downstream whole, already finished.
func (n *neoPiCompletionsNormalizer) emitHealedToolCall(event neopidialect.HealEvent) {
	if n.current != nil && n.current.kind == neoPiBlockToolCall {
		n.finishCall(n.current.call)
	}
	call := &neoPiStreamCall{id: event.ID, name: event.Name, outIndex: len(n.calls), partial: event.Arguments, finished: true}
	call.block = &neoPiStreamBlock{kind: neoPiBlockToolCall, call: call}
	call.pending.WriteString(event.Arguments)
	n.calls = append(n.calls, call)
	n.blocks++
	n.startCall(call)
	n.current = nil
}

// appendProcessedText is appendProcessedText: text from the content channel,
// optionally cleaned of leaked DeepSeek template tokens.
func (n *neoPiCompletionsNormalizer) appendProcessedText(text string) {
	if text == "" {
		return
	}
	if n.settings.stripDeepseek {
		n.deepseekBuf += text
		n.flushDeepseek(false)
		return
	}
	n.appendTextDelta(text)
}

func (n *neoPiCompletionsNormalizer) flushDeepseek(final bool) {
	if n.deepseekBuf == "" {
		return
	}
	var flushable string
	if final {
		flushable, n.deepseekBuf = n.deepseekBuf, ""
	} else {
		trailing := neoPiTrailingPartialDeepseekToken(n.deepseekBuf)
		flushable = n.deepseekBuf[:len(n.deepseekBuf)-len(trailing)]
		n.deepseekBuf = trailing
	}
	stripped := neoPiStripDeepseekSpecialTokens(flushable)
	if stripped != "" && (stripped == flushable || strings.TrimFunc(stripped, neoPiIsJSSpace) != "") {
		n.appendTextDelta(stripped)
	}
}

// neoPiStripDeepseekSpecialTokens is stripDeepseekSpecialTokens.
func neoPiStripDeepseekSpecialTokens(text string) string {
	stripped := neoPiDeepseekToken.ReplaceAllString(text, "")
	if stripped == text {
		return text
	}
	normalized := stripped
	if neoPiDeepseekAtStart.MatchString(text) {
		normalized = strings.TrimLeftFunc(normalized, neoPiIsJSSpace)
	}
	if neoPiDeepseekAtEnd.MatchString(text) {
		normalized = strings.TrimRightFunc(normalized, neoPiIsJSSpace)
	}
	return normalized
}

// neoPiTrailingPartialDeepseekToken is getTrailingPartialDeepseekToken.
func neoPiTrailingPartialDeepseekToken(text string) string {
	best := max(strings.LastIndex(text, "<｜"), strings.LastIndex(text, "<|"))
	if best == -1 {
		if strings.HasSuffix(text, "<") {
			return "<"
		}
		return ""
	}
	tail := text[best:]
	if strings.Contains(tail, "｜>") || strings.Contains(tail, "|>") {
		return ""
	}
	if neoPiUTF16Len(tail) > 256 {
		return ""
	}
	return tail
}

func neoPiUTF16Len(value string) int {
	total := 0
	for _, r := range value {
		if r >= 0x10000 {
			total += 2
		} else {
			total++
		}
	}
	return total
}

func neoPiIsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func (n *neoPiCompletionsNormalizer) appendTextDelta(text string) {
	if text == "" {
		return
	}
	if n.current == nil || n.current.kind != neoPiBlockText {
		n.current = &neoPiStreamBlock{kind: neoPiBlockText}
		n.blocks++
	}
	if strings.TrimFunc(text, neoPiIsJSSpace) != "" {
		n.visible = true
	}
	n.emitDelta(jsjson.Field{Name: "content", Value: jsjson.Marshal(text)})
}

// openThinkingBlock is openThinkingBlock.
func (n *neoPiCompletionsNormalizer) openThinkingBlock(signature *string) *neoPiStreamBlock {
	block := &neoPiStreamBlock{kind: neoPiBlockThinking, signature: signature}
	n.current = block
	n.blocks++
	return block
}

// appendThinking is appendThinking: reasoning text under a signature (the
// field name it arrived in), opening a new block when the signature changes.
func (n *neoPiCompletionsNormalizer) appendThinking(thinking string, signature *string) {
	block := n.current
	reuse := block != nil && block.kind == neoPiBlockThinking &&
		(signature == nil || (block.signature != nil && *block.signature == *signature))
	if !reuse {
		block = n.openThinkingBlock(signature)
	}
	if signature != nil && (block.signature == nil || *block.signature == "") {
		block.signature = signature
	}
	n.emitDelta(jsjson.Field{Name: "reasoning_content", Value: jsjson.Marshal(thinking)})
}

// appendThinkingDelta is appendThinkingDelta, including the cumulative-snapshot
// dedup MiniMax-style hosts need.
func (n *neoPiCompletionsNormalizer) appendThinkingDelta(thinking string, signature *string, source string) {
	if thinking == "" {
		return
	}
	emitted := thinking
	if source == "cumulative" {
		key := ""
		if signature != nil {
			key = *signature
		}
		last := n.cumulative[key]
		if strings.HasPrefix(thinking, last) {
			emitted = thinking[len(last):]
		}
		n.cumulative[key] = thinking
		if emitted == "" {
			return
		}
	}
	n.appendThinking(emitted, signature)
}

// appendLiteLLMThinkingBlock is appendLiteLLMThinkingBlock. A signature seals
// its block; the signature itself has no downstream representation.
func (n *neoPiCompletionsNormalizer) appendLiteLLMThinkingBlock(entry gjson.Result) {
	if !entry.IsObject() {
		return
	}
	switch entry.Get("type").String() {
	case "redacted_thinking":
		data := entry.Get("data")
		if data.Type != gjson.String || data.String() == "" {
			return
		}
		n.current = nil
		n.liteBlock = nil
		n.blocks++
	case "thinking":
		thinking, signature := "", ""
		if value := entry.Get("thinking"); value.Type == gjson.String {
			thinking = value.String()
		}
		if value := entry.Get("signature"); value.Type == gjson.String {
			signature = value.String()
		}
		if thinking == "" && signature == "" {
			return
		}
		var block *neoPiStreamBlock
		if n.current == n.liteBlock {
			block = n.liteBlock
		}
		if block == nil || (thinking != "" && block.signature != nil && *block.signature != "") {
			block = n.openThinkingBlock(nil)
			n.liteBlock = block
		}
		if thinking != "" {
			n.emitDelta(jsjson.Field{Name: "reasoning_content", Value: jsjson.Marshal(thinking)})
		}
		if signature != "" {
			combined := signature
			if block.signature != nil {
				combined = *block.signature + signature
			}
			block.signature = &combined
		}
	}
}

// toolCalls routes each streamed tool_calls entry to its block exactly as NeoPi
// does (id first, then index, then unkeyed array offset, then the open block).
func (n *neoPiCompletionsNormalizer) toolCalls(entries []gjson.Result) {
	for offset, entry := range entries {
		if !entry.IsObject() {
			continue
		}
		streamIndex, hasIndex := 0, false
		if value := entry.Get("index"); value.Type == gjson.Number {
			streamIndex, hasIndex = int(value.Int()), true
		}
		id := neoPiStringField(entry.Get("id"))
		name := neoPiStringField(entry.Get("function.name"))
		unkeyedEntry := len(entries) > 1 && !hasIndex && id == ""

		var call *neoPiStreamCall
		if id != "" {
			for _, candidate := range n.pending {
				if candidate.id == id {
					call = candidate
					break
				}
			}
		}
		var indexed *neoPiStreamCall
		if hasIndex {
			indexed = n.byIndex[streamIndex]
		}
		if call == nil && indexed != nil && (id == "" || indexed.id == "" || name == "") {
			call = indexed
		}
		if call == nil && unkeyedEntry && offset < len(n.unkeyed) {
			if candidate := n.unkeyed[offset]; candidate != nil && !candidate.finished {
				call = candidate
			}
		}
		if call == nil && !unkeyedEntry && n.current != nil && n.current.kind == neoPiBlockToolCall &&
			(id == "" || n.current.call.id == id) {
			call = n.current.call
		}

		if call == nil {
			call = &neoPiStreamCall{id: id, name: name, outIndex: len(n.calls)}
			call.block = &neoPiStreamBlock{kind: neoPiBlockToolCall, call: call}
			if hasIndex {
				n.byIndex[streamIndex] = call
			}
			n.calls = append(n.calls, call)
			n.pending = append(n.pending, call)
			n.current = call.block
			n.blocks++
			if unkeyedEntry {
				for len(n.unkeyed) <= offset {
					n.unkeyed = append(n.unkeyed, nil)
				}
				n.unkeyed[offset] = call
			}
		} else {
			n.current = call.block
			if hasIndex && n.byIndex[streamIndex] != call {
				n.byIndex[streamIndex] = call
			}
		}

		if id != "" {
			call.id = id
		}
		if name != "" {
			call.name = name
		}
		delta := ""
		switch rawArgs := entry.Get("function.arguments"); {
		case rawArgs.Type == gjson.String:
			if text := rawArgs.String(); text != "" {
				delta = text
				if call.object != nil {
					call.partial = ""
					call.object = nil
				}
				call.partial += text
			}
		case rawArgs.IsObject():
			fragment := neoPiJSValueFrom(rawArgs).(*neoPiJSObject)
			call.object = neoPiMergeObjects(call.object, fragment)
			call.pending.Reset()
			call.partial = ""
		}
		n.startCallIfReady(call)
		if delta != "" {
			n.callArguments(call, delta)
		}
	}
}

// startCallIfReady announces a tool call downstream once its id and name are
// known; argument text that arrived earlier rides along with the opening entry.
func (n *neoPiCompletionsNormalizer) startCallIfReady(call *neoPiStreamCall) {
	if call.started || call.name == "" || call.id == "" {
		return
	}
	n.startCall(call)
}

func (n *neoPiCompletionsNormalizer) startCall(call *neoPiStreamCall) {
	if call.id == "" {
		call.id = "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	}
	call.started = true
	pending := call.pending.String()
	call.pending.Reset()
	call.emitted += len(pending)
	n.emitDelta(jsjson.Field{Name: "tool_calls", Value: jsjson.Marshal([]json.RawMessage{jsjson.Object(
		jsjson.Field{Name: "index", Value: jsjson.Marshal(call.outIndex)},
		jsjson.Field{Name: "id", Value: jsjson.Marshal(call.id)},
		jsjson.Field{Name: "type", Value: jsjson.Marshal("function")},
		jsjson.Field{Name: "function", Value: jsjson.Object(
			jsjson.Field{Name: "name", Value: jsjson.Marshal(call.name)},
			jsjson.Field{Name: "arguments", Value: jsjson.Marshal(pending)},
		)},
	)})})
}

func (n *neoPiCompletionsNormalizer) callArguments(call *neoPiStreamCall, arguments string) {
	if !call.started {
		call.pending.WriteString(arguments)
		return
	}
	call.emitted += len(arguments)
	n.emitDelta(jsjson.Field{Name: "tool_calls", Value: jsjson.Marshal([]json.RawMessage{jsjson.Object(
		jsjson.Field{Name: "index", Value: jsjson.Marshal(call.outIndex)},
		jsjson.Field{Name: "function", Value: jsjson.Object(jsjson.Field{Name: "arguments", Value: jsjson.Marshal(arguments)})},
	)})})
}

// finishToolCalls is finishPendingToolCallBlocks: the end-of-stream sweep that
// opens calls that never became ready, flushes object-shaped arguments as one
// delta, and gives a call that streamed no argument text its `{}`
// (parseToolCallArguments("")) so downstream clients always hold valid JSON.
func (n *neoPiCompletionsNormalizer) finishToolCalls() {
	for _, call := range n.pending {
		n.finishCall(call)
	}
	n.pending = nil
}

// finishCall is finishToolCallBlock for one call: it is opened if it never
// became ready, object-shaped arguments go out as one delta, and a call with
// no argument text gets `{}`.
func (n *neoPiCompletionsNormalizer) finishCall(call *neoPiStreamCall) {
	if call.finished {
		return
	}
	call.finished = true
	if call.object != nil {
		if !call.started {
			call.pending.Reset()
			n.startCall(call)
		}
		if call.emitted == 0 {
			n.callArguments(call, string(call.object.stringify()))
		}
		return
	}
	if !call.started {
		n.startCall(call)
	}
	if strings.TrimFunc(call.partial, neoPiIsJSSpace) == "" {
		n.callArguments(call, "{}")
	}
}

// finish is everything after NeoPi's stream loop: the healing flush, the
// DeepSeek buffer flush, the premature-close check, the block sweep, stop
// reason promotion and the terminal classification. It emits the finish and
// usage chunks on success.
func (n *neoPiCompletionsNormalizer) finish(sawDone bool) error {
	if n.healer != nil {
		// Healed calls promote a natural stop to toolUse with the rest below.
		n.applyHealing(n.healer.Flush())
	}
	n.flushDeepseek(true)
	if !n.finished && !sawDone && n.blocks > 0 {
		return n.responseError(neoPiCompletionsIncompleteMessage)
	}
	n.finishToolCalls()
	if n.stop == "stop" && len(n.calls) > 0 {
		n.stop = "toolUse"
	}
	if n.settings.emptyLengthIsContextError && n.stop == "length" && !n.visible && len(n.calls) == 0 {
		n.stop = "error"
		n.errorMessage = neoPiCompletionsEmptyLengthMessage
	}
	if n.repetition != "" {
		return n.responseError("Provider reported a repetition stop: " + n.repetition)
	}
	if n.stop == "error" {
		message := n.errorMessage
		if message == "" {
			message = "Provider returned an error stop reason"
		}
		return n.responseError(message)
	}
	reason := map[string]string{"stop": "stop", "length": "length", "toolUse": "tool_calls"}[n.stop]
	n.chunk(jsjson.Marshal([]json.RawMessage{jsjson.Object(
		jsjson.Field{Name: "index", Value: jsjson.Marshal(0)},
		jsjson.Field{Name: "delta", Value: json.RawMessage(`{}`)},
		jsjson.Field{Name: "finish_reason", Value: jsjson.Marshal(reason)},
	)}))
	if line := n.usageLine(); line != nil {
		n.out = append(n.out, line)
	}
	return nil
}

// emitDelta sends one event as its own chunk, keeping NeoPi's event order.
func (n *neoPiCompletionsNormalizer) emitDelta(fields ...jsjson.Field) {
	if !n.roleSent {
		n.roleSent = true
		fields = append([]jsjson.Field{{Name: "role", Value: jsjson.Marshal("assistant")}}, fields...)
	}
	n.chunk(jsjson.Marshal([]json.RawMessage{jsjson.Object(
		jsjson.Field{Name: "index", Value: jsjson.Marshal(0)},
		jsjson.Field{Name: "delta", Value: jsjson.Object(fields...)},
		jsjson.Field{Name: "finish_reason", Value: json.RawMessage(`null`)},
	)}))
}

func (n *neoPiCompletionsNormalizer) responseID() string {
	if n.id == "" {
		n.id = "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
	}
	return n.id
}

func (n *neoPiCompletionsNormalizer) chunk(choices json.RawMessage, extra ...jsjson.Field) {
	if n.created == 0 {
		n.created = n.now().Unix()
	}
	fields := []jsjson.Field{
		{Name: "id", Value: jsjson.Marshal(n.responseID())},
		{Name: "object", Value: jsjson.Marshal("chat.completion.chunk")},
		{Name: "created", Value: jsjson.Marshal(n.created)},
		{Name: "model", Value: jsjson.Marshal(n.model)},
		{Name: "choices", Value: choices},
	}
	fields = append(fields, extra...)
	n.out = append(n.out, append([]byte("data: "), jsjson.Object(fields...)...))
}

// usageLine is the usage-only chunk for the usage parsed so far, or nil.
func (n *neoPiCompletionsNormalizer) usageLine() []byte {
	if n.usage == nil {
		return nil
	}
	saved := n.out
	n.out = nil
	n.chunk(json.RawMessage(`[]`), jsjson.Field{Name: "usage", Value: n.usage.json()})
	line := n.out[0]
	n.out = saved
	return line
}

// neoPiStreamUsage is parseChunkUsage's accounting.
type neoPiStreamUsage struct {
	input, output, cacheRead, cacheWrite, total, reasoning float64
	extras                                                 []jsjson.Field
}

// applyUsage is applyUsagePayload.
func (n *neoPiCompletionsNormalizer) applyUsage(raw gjson.Result) {
	usage := neoPiParseChunkUsage(raw, n.settings.cachedPromptTokensHeader)
	n.usage = &usage
	n.sawUsage = true
	n.awaitTrailing = !neoPiHasPositiveCacheReadField(raw)
}

func neoPiUsageNumber(raw gjson.Result, path string) (float64, bool) {
	value := raw.Get(path)
	if value.Type != gjson.Number {
		return 0, false
	}
	return value.Float(), true
}

// neoPiHasPositiveCacheReadField is hasPositiveCacheReadTokenField.
func neoPiHasPositiveCacheReadField(raw gjson.Result) bool {
	for _, path := range []string{"cached_tokens", "prompt_cache_hit_tokens", "cachedContentTokenCount", "prompt_tokens_details.cached_tokens"} {
		if value, ok := neoPiUsageNumber(raw, path); ok && value > 0 {
			return true
		}
	}
	return false
}

// neoPiParseChunkUsage is parseChunkUsage over calculateOpenAIUsageAccounting.
func neoPiParseChunkUsage(raw gjson.Result, headerCached float64) neoPiStreamUsage {
	completion, _ := neoPiUsageNumber(raw, "completion_tokens")
	prompt, _ := neoPiUsageNumber(raw, "prompt_tokens")
	cached, hasCached := neoPiUsageNumber(raw, "cached_tokens")
	hit, hasHit := neoPiUsageNumber(raw, "prompt_cache_hit_tokens")
	miss, hasMiss := neoPiUsageNumber(raw, "prompt_cache_miss_tokens")
	detailCached, hasDetailCached := neoPiUsageNumber(raw, "prompt_tokens_details.cached_tokens")
	contentCached, hasContentCached := neoPiUsageNumber(raw, "cachedContentTokenCount")
	reasoning, _ := neoPiUsageNumber(raw, "completion_tokens_details.reasoning_tokens")
	writeOpenRouter, hasWriteOpenRouter := neoPiUsageNumber(raw, "prompt_tokens_details.cache_write_tokens")

	bodyCached := 0.0
	for _, candidate := range []struct {
		value float64
		ok    bool
	}{{cached, hasCached}, {hit, hasHit}, {detailCached, hasDetailCached}, {contentCached, hasContentCached}} {
		if candidate.ok && candidate.value > 0 {
			bodyCached = candidate.value
			break
		}
	}
	resolved := 0.0
	switch {
	case bodyCached > 0 || (hasCached && cached == 0) || (hasHit && hit == 0) || (hasDetailCached && detailCached == 0) || (hasContentCached && contentCached == 0):
		resolved = bodyCached
	case headerCached > 0:
		resolved = headerCached
	}

	cacheWrite := 0.0
	switch {
	case hasWriteOpenRouter:
		cacheWrite = writeOpenRouter
	case hasMiss:
		cacheWrite = miss
	}
	isDeepSeek := hasHit && hasMiss && !hasWriteOpenRouter && miss > 0
	input := math.Max(0, prompt-resolved-cacheWrite)
	if isDeepSeek {
		input = math.Max(0, prompt-resolved)
		cacheWrite = 0
	}
	usage := neoPiStreamUsage{input: input, output: completion, cacheRead: resolved, cacheWrite: cacheWrite, reasoning: reasoning}
	usage.total = input + completion + resolved + cacheWrite
	// OpenRouter reconciles token estimates against the account charge it
	// reports; carry those fields through untouched.
	for _, key := range []string{"cost", "cost_details", "is_byok"} {
		if value := raw.Get(key); value.Exists() {
			usage.extras = append(usage.extras, jsjson.Field{Name: key, Value: jsjson.Normalize([]byte(value.Raw))})
		}
	}
	return usage
}

// json is the usage in the subset form NeoProxy's translators and usage
// reporter read: prompt_tokens covers cache reads and writes.
func (u neoPiStreamUsage) json() json.RawMessage {
	number := func(value float64) json.RawMessage { return json.RawMessage(jsjson.Number(value)) }
	promptDetails := []jsjson.Field{{Name: "cached_tokens", Value: number(u.cacheRead)}}
	if u.cacheWrite > 0 {
		promptDetails = append(promptDetails, jsjson.Field{Name: "cache_write_tokens", Value: number(u.cacheWrite)})
	}
	fields := []jsjson.Field{
		{Name: "prompt_tokens", Value: number(u.input + u.cacheRead + u.cacheWrite)},
		{Name: "completion_tokens", Value: number(u.output)},
		{Name: "total_tokens", Value: number(u.total)},
		{Name: "prompt_tokens_details", Value: jsjson.Object(promptDetails...)},
	}
	if u.reasoning > 0 {
		fields = append(fields, jsjson.Field{Name: "completion_tokens_details", Value: jsjson.Object(jsjson.Field{Name: "reasoning_tokens", Value: number(u.reasoning)})})
	}
	return jsjson.Object(append(fields, u.extras...)...)
}

// neoPiTruthy is JavaScript truthiness for a decoded JSON value.
func neoPiTruthy(value gjson.Result) bool {
	switch value.Type {
	case gjson.String:
		return value.String() != ""
	case gjson.Number:
		return value.Float() != 0 && !math.IsNaN(value.Float())
	case gjson.True, gjson.JSON:
		return true
	}
	return false
}

func neoPiStringField(value gjson.Result) string {
	if value.Type == gjson.String {
		return value.String()
	}
	return ""
}

// neoPiMapStopReason is mapStopReason for a truthy finish_reason: the stop
// reason ("stop", "length", "toolUse" or "error") and the error message.
func neoPiMapStopReason(reason gjson.Result) (string, string) {
	raw := reason.String()
	normalized := raw
	if reason.Type == gjson.String {
		normalized = strings.ToLower(raw)
	}
	switch normalized {
	case "stop", "end":
		return "stop", ""
	case "length", "max_tokens":
		return "length", ""
	case "function_call", "tool_calls":
		return "toolUse", ""
	case "content_filter":
		return "error", "Provider finish_reason: content_filter"
	case "network_error":
		return "error", "Provider finish_reason: network_error"
	case "error":
		return "error", "Provider returned error finish_reason"
	case "insufficient_system_resource":
		return "error", "Provider returned error finish_reason: insufficient_system_resource"
	case "unknown", "other", "unspecified", "finish_reason_unspecified":
		return "error", "Provider returned error finish_reason: " + normalized
	}
	return "error", "Provider finish_reason: " + raw
}

// neoPiNativeFinishReasonError is getNativeFinishReasonError.
func neoPiNativeFinishReasonError(native gjson.Result) string {
	if native.Type != gjson.String {
		return ""
	}
	switch strings.ToUpper(native.String()) {
	case "MALFORMED_RESPONSE", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "NETWORK_ERROR":
		return "Provider returned error (native_finish_reason: " + native.String() + ")"
	}
	return ""
}

// neoPiDetectRepetitionStop is detectRepetitionStop.
func neoPiDetectRepetitionStop(choice gjson.Result) string {
	finish, stop := choice.Get("finish_reason"), choice.Get("stop_reason")
	names := func(value gjson.Result) bool {
		if value.Type != gjson.String {
			return false
		}
		lower := strings.ToLower(value.String())
		return lower == "repetition" || lower == "repetition_detected"
	}
	if !names(finish) && !names(stop) {
		return ""
	}
	var parts []string
	if finish.Type == gjson.String && finish.String() != "" {
		parts = append(parts, "finish_reason="+finish.String())
	}
	if stop.Type == gjson.String && stop.String() != "" {
		parts = append(parts, "stop_reason="+stop.String())
	}
	if len(parts) == 0 {
		return "repetition"
	}
	return strings.Join(parts, " ")
}

// neoPiIsProgressChunk is isOpenAICompletionsProgressChunk: whether a frame is
// real output rather than a keepalive, role-only preamble or empty no-op.
func neoPiIsProgressChunk(chunk gjson.Result) bool {
	if !chunk.IsObject() {
		return false
	}
	if neoPiTruthy(chunk.Get("usage")) {
		return true
	}
	choices := chunk.Get("choices")
	if !choices.IsArray() {
		return false
	}
	choice := choices.Get("0")
	if !neoPiTruthy(choice) {
		return false
	}
	if neoPiTruthy(choice.Get("finish_reason")) || neoPiTruthy(choice.Get("usage")) {
		return true
	}
	delta := choice.Get("delta")
	if !neoPiTruthy(delta) {
		return false
	}
	if content := delta.Get("content"); content.Type == gjson.String && content.String() != "" || content.IsArray() && len(content.Array()) > 0 {
		return true
	}
	if calls := delta.Get("tool_calls"); calls.IsArray() && len(calls.Array()) > 0 {
		return true
	}
	for _, field := range []string{"reasoning", "reasoning_content", "reasoning_text", "refusal"} {
		if value := delta.Get(field); value.Type == gjson.String && value.String() != "" {
			return true
		}
	}
	return false
}

// inBandError runs NeoPi's two error probes on a decoded frame: the in-band
// throttle classifier first, then the legacy stream-error guard.
func (n *neoPiCompletionsNormalizer) inBandError(chunk gjson.Result) error {
	if err := neoPiInBandProviderError(chunk); err != nil {
		return n.fail(statusErr{code: err.status, msg: n.settings.provider + ": " + err.message})
	}
	if err := neoPiCompletionsStreamFrameError(chunk); err != nil {
		return n.fail(statusErr{code: err.status, msg: n.settings.provider + ": " + err.message})
	}
	return nil
}

// neoPiFrameError is an in-band failure: status 0 means NeoPi raised a
// ProviderResponseError (no HTTP metadata), which surfaces as a bad gateway.
type neoPiFrameError struct {
	status  int
	message string
}

func neoPiNewFrameError(status int, message string) *neoPiFrameError {
	if status == 0 {
		status = http.StatusBadGateway
	}
	return &neoPiFrameError{status: status, message: message}
}

// neoPiCompletionsStreamFrameError is createOpenAICompletionsStreamError.
func neoPiCompletionsStreamFrameError(chunk gjson.Result) *neoPiFrameError {
	errorField, flat := chunk.Get("error"), chunk.Get("message")
	structured := errorField.IsObject() || errorField.IsArray()
	if !structured && errorField.Type != gjson.String && flat.Type != gjson.String {
		return nil
	}
	detail := ""
	switch {
	case structured:
		if message := errorField.Get("message"); message.Type == gjson.String && message.String() != "" {
			detail = message.String()
		}
	case errorField.Type == gjson.String && errorField.String() != "":
		detail = errorField.String()
	case flat.Type == gjson.String && flat.String() != "":
		detail = flat.String()
	}
	if detail == "" {
		detail = neoPiInBandDefaultDetail
	}
	if !structured {
		return neoPiNewFrameError(0, detail)
	}
	status := neoPiCompletionsErrorStatus(errorField.Get("code"))
	if status == 0 {
		if kind := errorField.Get("type"); kind.Type == gjson.String {
			status = neoPiCompletionsErrorStatusByType[strings.ToUpper(strings.TrimFunc(kind.String(), neoPiIsJSSpace))]
		}
	}
	if status == 0 {
		return neoPiNewFrameError(0, detail)
	}
	return neoPiNewFrameError(status, fmt.Sprintf("%d %s", status, detail))
}

// neoPiCompletionsErrorStatus is parseOpenAICompletionsErrorStatus.
func neoPiCompletionsErrorStatus(value gjson.Result) int {
	status := 0
	switch value.Type {
	case gjson.Number:
		if value.Float() == math.Trunc(value.Float()) {
			status = int(value.Int())
		}
	case gjson.String:
		if trimmed := strings.TrimFunc(value.String(), neoPiIsJSSpace); neoPiStatusCode.MatchString(trimmed) {
			status, _ = strconv.Atoi(trimmed)
		}
	}
	if status >= 400 && status <= 599 {
		return status
	}
	return 0
}

// neoPiInBandSignal is InBandSignal.
type neoPiInBandSignal struct {
	status    int
	code      string
	detail    string
	hasDetail bool
}

func neoPiNormalizeCodeToken(value gjson.Result) (string, bool) {
	switch value.Type {
	case gjson.Number:
		if math.IsNaN(value.Float()) || math.IsInf(value.Float(), 0) {
			return "", false
		}
		return jsjson.Number(value.Float()), true
	case gjson.String:
		spaced := neoPiCodeWordBoundary.ReplaceAllString(strings.TrimFunc(value.String(), neoPiIsJSSpace), "${1}_${2}")
		collapsed := neoPiCodeUnderscores.ReplaceAllString(neoPiCodeSeparators.ReplaceAllString(strings.ToLower(spaced), "_"), "_")
		return collapsed, collapsed != ""
	}
	return "", false
}

// neoPiInBandDetail is readInBandDetail.
func neoPiInBandDetail(value gjson.Result) (string, bool) {
	if value.Type != gjson.String {
		return "", false
	}
	return neoPiFlattenInBandDetail(value.String())
}

// neoPiFlattenInBandDetail flattens a message to one line of visible text, so
// a synthesized error cannot smuggle markup or framing into the caller.
func neoPiFlattenInBandDetail(value string) (string, bool) {
	flattened := neoPiMarkupTag.ReplaceAllString(value, " ")
	flattened = neoPiControlRun.ReplaceAllString(flattened, " ")
	flattened = strings.TrimFunc(neoPiSpaceRun.ReplaceAllString(flattened, " "), neoPiIsJSSpace)
	if flattened == "" {
		return "", false
	}
	return neoPiTruncateUTF16(flattened, neoPiInBandMaxDetail), true
}

func neoPiTruncateUTF16(value string, limit int) string {
	if neoPiUTF16Len(value) <= limit {
		return value
	}
	units := 0
	for index, r := range value {
		width := 1
		if r >= 0x10000 {
			width = 2
		}
		if units+width > limit {
			return value[:index]
		}
		units += width
	}
	return value
}

// neoPiStatusField is readStatusField.
func neoPiStatusField(value gjson.Result) int {
	status := 0
	switch value.Type {
	case gjson.Number:
		if value.Float() == math.Trunc(value.Float()) {
			status = int(value.Int())
		}
	case gjson.String:
		if trimmed := strings.TrimFunc(value.String(), neoPiIsJSSpace); neoPiStatusCode.MatchString(trimmed) {
			status, _ = strconv.Atoi(trimmed)
		}
	}
	if status >= 100 && status <= 599 {
		return status
	}
	return 0
}

func neoPiInBandRetryable(status int) bool {
	return status == 429 || (status >= 500 && status <= 599)
}

// neoPiReadInBandSignal is readInBandSignal.
func neoPiReadInBandSignal(root gjson.Result) (neoPiInBandSignal, bool) {
	if !root.IsObject() {
		return neoPiInBandSignal{}, false
	}
	nested := root.Get("error")
	if !nested.Exists() || nested.Type == gjson.Null {
		nested = root.Get("response.error")
	}
	var errorObject gjson.Result
	if nested.IsObject() {
		errorObject = nested
		for depth := 0; depth < 2; depth++ {
			inner := errorObject.Get("error")
			if !inner.IsObject() {
				break
			}
			errorObject = inner
		}
	}
	pick := func(field string) gjson.Result {
		if errorObject.Exists() {
			if value := errorObject.Get(field); value.Exists() && value.Type != gjson.Null {
				return value
			}
		}
		return root.Get(field)
	}
	code, hasCode := neoPiNormalizeCodeToken(pick("code"))
	if !hasCode {
		code, hasCode = neoPiNormalizeCodeToken(pick("type"))
	}
	if hasCode && neoPiInBandNonRetryable.MatchString(code) {
		return neoPiInBandSignal{}, false
	}
	retryableCode := hasCode && neoPiInBandRetryableText.MatchString(code)
	if !retryableCode && !nested.Exists() && !root.Get("status").Exists() && !root.Get("code").Exists() && !root.Get("message").Exists() {
		return neoPiInBandSignal{}, false
	}
	detail, hasDetail := "", false
	if errorObject.Exists() {
		detail, hasDetail = neoPiInBandDetail(errorObject.Get("message"))
	}
	if !hasDetail {
		detail, hasDetail = neoPiInBandDetail(root.Get("message"))
	}
	if !hasDetail && nested.Type == gjson.String {
		detail, hasDetail = neoPiInBandDetail(nested)
	}
	var reported []gjson.Result
	if errorObject.Exists() {
		reported = []gjson.Result{errorObject.Get("status"), errorObject.Get("code"), root.Get("status"), root.Get("code")}
	} else {
		reported = []gjson.Result{root.Get("status"), root.Get("code")}
	}
	status := 0
	for _, value := range reported {
		if candidate := neoPiStatusField(value); neoPiInBandRetryable(candidate) {
			status = candidate
			break
		}
	}
	if status == 0 && hasCode {
		status = neoPiInBandRetryableStatusByCode[code]
	}
	if status != 0 {
		return neoPiInBandSignal{status: status, code: code, detail: detail, hasDetail: hasDetail}, true
	}
	if !hasDetail || !neoPiInBandRetryableText.MatchString(detail) {
		return neoPiInBandSignal{}, false
	}
	return neoPiInBandSignal{code: code, detail: detail, hasDetail: true}, true
}

// neoPiInBandProviderError is createInBandProviderError.
func neoPiInBandProviderError(frame gjson.Result) *neoPiFrameError {
	signal, ok := neoPiReadInBandSignal(frame)
	if !ok {
		return nil
	}
	if neoPiInBandRetryable(signal.status) {
		return neoPiNewFrameError(signal.status, neoPiFormatInBandMessage(signal.status, signal.detail, signal.hasDetail, signal.code))
	}
	if !signal.hasDetail && signal.code == "" {
		return nil
	}
	detail := signal.detail
	if !signal.hasDetail {
		detail = neoPiInBandPlaceholder
	}
	if signal.code != "" {
		detail += " (" + signal.code + ")"
	}
	return neoPiNewFrameError(0, detail)
}

// neoPiInBandProviderErrorFromText is createInBandProviderErrorFromText: a
// non-JSON frame that reads as a throttle (`data: 429 Too Many Requests`).
func neoPiInBandProviderErrorFromText(text string) *neoPiFrameError {
	detail, ok := neoPiFlattenInBandDetail(text)
	if !ok || !neoPiInBandRetryableText.MatchString(detail) {
		return nil
	}
	if match := neoPiInBandLeadingStatus.FindStringSubmatch(detail); match != nil {
		if status, _ := strconv.Atoi(match[1]); neoPiInBandRetryable(status) {
			return neoPiNewFrameError(status, neoPiFormatInBandMessage(status, detail, true, ""))
		}
	}
	return neoPiNewFrameError(0, detail)
}

// neoPiFormatInBandMessage is formatInBandMessage.
func neoPiFormatInBandMessage(status int, detail string, hasDetail bool, code string) string {
	body := detail
	if !hasDetail {
		body = neoPiInBandPlaceholder
	}
	suffix := ""
	if code != "" && !neoPiAllDigits(code) && !strings.Contains(strings.ToLower(body), strings.ToLower(code)) {
		suffix = " (" + code + ")"
	}
	message := body
	if match := neoPiInBandLeadingStatus.FindStringSubmatch(body); match == nil || match[1] != strconv.Itoa(status) {
		message = strconv.Itoa(status) + " " + body
	}
	if neoPiIsOpaqueStatusBody(message) {
		message = strconv.Itoa(status) + " " + neoPiInBandPlaceholder
	}
	return message + suffix
}

func neoPiAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// neoPiIsOpaqueStatusBody is isOpaqueStatusBody.
func neoPiIsOpaqueStatusBody(message string) bool {
	cleaned := neoPiOpaqueStatus.ReplaceAllString(message, "")
	cleaned = neoPiOpaqueFraming.ReplaceAllString(cleaned, "")
	cleaned = neoPiOpaqueNoBody.ReplaceAllString(cleaned, "")
	return !neoPiOpaqueWord.MatchString(cleaned) &&
		!neoPiCNQuotaExhausted.MatchString(cleaned) &&
		!neoPiCNTransientCap.MatchString(cleaned) &&
		!neoPiCNThrottle.MatchString(cleaned)
}

// neoPiJSObject is an insertion-ordered JSON object, the shape MiniMax-style
// hosts stream tool arguments in.
type neoPiJSObject struct {
	keys   []string
	values map[string]any
}

func newNeoPiJSObject() *neoPiJSObject { return &neoPiJSObject{values: map[string]any{}} }

func (o *neoPiJSObject) set(key string, value any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// neoPiJSValueFrom decodes a JSON value into strings, float64s, bools, nil,
// []any and *neoPiJSObject (duplicate keys keep the last value in the first
// key's position, as JSON.parse does).
func neoPiJSValueFrom(value gjson.Result) any {
	switch {
	case value.IsObject():
		object := newNeoPiJSObject()
		value.ForEach(func(key, item gjson.Result) bool {
			object.set(key.String(), neoPiJSValueFrom(item))
			return true
		})
		return object
	case value.IsArray():
		items := []any{}
		value.ForEach(func(_, item gjson.Result) bool {
			items = append(items, neoPiJSValueFrom(item))
			return true
		})
		return items
	}
	switch value.Type {
	case gjson.String:
		return value.String()
	case gjson.Number:
		return value.Float()
	case gjson.True:
		return true
	case gjson.False:
		return false
	}
	return nil
}

func neoPiUnsafeArgumentKey(key string) bool {
	return key == "__proto__" || key == "constructor" || key == "prototype"
}

func neoPiCloneJSValue(value any) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = neoPiCloneJSValue(item)
		}
		return out
	case *neoPiJSObject:
		return neoPiMergeObjects(nil, typed)
	}
	return value
}

func neoPiJSValuesEqual(left, right any) bool {
	switch l := left.(type) {
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for i := range l {
			if !neoPiJSValuesEqual(l[i], r[i]) {
				return false
			}
		}
		return true
	case *neoPiJSObject:
		r, ok := right.(*neoPiJSObject)
		if !ok {
			return false
		}
		leftKeys := 0
		for _, key := range l.keys {
			if neoPiUnsafeArgumentKey(key) {
				continue
			}
			leftKeys++
			other, has := r.values[key]
			if !has || !neoPiJSValuesEqual(l.values[key], other) {
				return false
			}
		}
		rightKeys := 0
		for _, key := range r.keys {
			if !neoPiUnsafeArgumentKey(key) {
				rightKeys++
			}
		}
		return leftKeys == rightKeys
	case float64:
		r, ok := right.(float64)
		return ok && l == r
	}
	return left == right
}

func neoPiArrayStartsWith(value, prefix []any) bool {
	if len(prefix) > len(value) {
		return false
	}
	for i := range prefix {
		if !neoPiJSValuesEqual(value[i], prefix[i]) {
			return false
		}
	}
	return true
}

func neoPiMergeArrays(prev, fragment []any) []any {
	if neoPiArrayStartsWith(fragment, prev) {
		return neoPiCloneJSValue(fragment).([]any)
	}
	if neoPiArrayStartsWith(prev, fragment) {
		return neoPiCloneJSValue(prev).([]any)
	}
	merged := neoPiCloneJSValue(prev).([]any)
	for _, value := range fragment {
		merged = append(merged, neoPiCloneJSValue(value))
	}
	return merged
}

func neoPiMergeValues(prev, fragment any) any {
	switch p := prev.(type) {
	case string:
		if f, ok := fragment.(string); ok {
			if strings.HasPrefix(f, p) {
				return f
			}
			return p + f
		}
	case []any:
		if f, ok := fragment.([]any); ok {
			return neoPiMergeArrays(p, f)
		}
	case *neoPiJSObject:
		if f, ok := fragment.(*neoPiJSObject); ok {
			return neoPiMergeObjects(p, f)
		}
	}
	return neoPiCloneJSValue(fragment)
}

// neoPiMergeObjects is mergeStreamingArgumentObjects.
func neoPiMergeObjects(prev, fragment *neoPiJSObject) *neoPiJSObject {
	merged := newNeoPiJSObject()
	if prev != nil {
		for _, key := range prev.keys {
			if !neoPiUnsafeArgumentKey(key) {
				merged.set(key, neoPiCloneJSValue(prev.values[key]))
			}
		}
	}
	for _, key := range fragment.keys {
		if neoPiUnsafeArgumentKey(key) {
			continue
		}
		if existing, ok := merged.values[key]; ok {
			merged.set(key, neoPiMergeValues(existing, fragment.values[key]))
		} else {
			merged.set(key, neoPiCloneJSValue(fragment.values[key]))
		}
	}
	return merged
}

// stringify is JSON.stringify of the object: integer-like keys come first in
// ascending order, the rest in insertion order.
func (o *neoPiJSObject) stringify() json.RawMessage {
	var indexKeys, otherKeys []string
	for _, key := range o.keys {
		if _, ok := neoPiArrayIndex(key); ok {
			indexKeys = append(indexKeys, key)
		} else {
			otherKeys = append(otherKeys, key)
		}
	}
	sort.SliceStable(indexKeys, func(i, j int) bool {
		left, _ := neoPiArrayIndex(indexKeys[i])
		right, _ := neoPiArrayIndex(indexKeys[j])
		return left < right
	})
	fields := make([]jsjson.Field, 0, len(o.keys))
	for _, key := range append(indexKeys, otherKeys...) {
		fields = append(fields, jsjson.Field{Name: key, Value: neoPiStringifyJSValue(o.values[key])})
	}
	return jsjson.Object(fields...)
}

// neoPiArrayIndex reports whether key is a canonical array index.
func neoPiArrayIndex(key string) (uint64, bool) {
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	value, err := strconv.ParseUint(key, 10, 64)
	return value, err == nil && value < 4294967295
}

func neoPiStringifyJSValue(value any) json.RawMessage {
	switch typed := value.(type) {
	case nil:
		return json.RawMessage("null")
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return json.RawMessage("null")
		}
		return json.RawMessage(jsjson.Number(typed))
	case []any:
		items := make([]json.RawMessage, len(typed))
		for i, item := range typed {
			items[i] = neoPiStringifyJSValue(item)
		}
		return jsjson.Marshal(items)
	case *neoPiJSObject:
		return typed.stringify()
	}
	return jsjson.Marshal(value)
}

// neoPiScanNormalizedCompletions is the response loop of NeoPi's
// openai-completions provider as a drop-in for neoPiScanCompletions: it reads
// the SSE body the way NeoPi's transport does, interprets every frame, and
// relays the canonical stream as `data: ...` lines ending in `data: [DONE]`.
// A failure returns a statusErr (wrapped with the usage parsed so far, see
// neoPiCompletionsFailureUsage); a body that outlives its terminal chunk is
// released after NeoPi's grace window.
func neoPiScanNormalizedCompletions(ctx context.Context, cfg *config.Config, provider string, compat *registry.NeoPiCompletionsCompat, header http.Header, body io.Reader, emit func([]byte) error) error {
	return neoPiNormalizeCompletionsStream(ctx, cfg, neoPiCompletionsStreamSettingsFor(provider, compat, header), body, emit)
}

// neoPiBodyRead is one read from the upstream body.
type neoPiBodyRead struct {
	data []byte
	err  error
}

func neoPiPumpBody(body io.Reader, reads chan<- neoPiBodyRead, stop <-chan struct{}) {
	buffer := make([]byte, 32<<10)
	for {
		count, err := body.Read(buffer)
		if count > 0 {
			select {
			case reads <- neoPiBodyRead{data: bytes.Clone(buffer[:count])}:
			case <-stop:
				return
			}
		}
		if err != nil {
			select {
			case reads <- neoPiBodyRead{err: err}:
			case <-stop:
			}
			return
		}
	}
}

func neoPiNormalizeCompletionsStream(ctx context.Context, cfg *config.Config, settings neoPiCompletionsStreamSettings, body io.Reader, emit func([]byte) error) error {
	n := newNeoPiCompletionsNormalizer(settings, nil)
	reads := make(chan neoPiBodyRead, 4)
	stop := make(chan struct{})
	defer close(stop)
	go neoPiPumpBody(body, reads, stop)

	flush := func() error {
		for _, line := range n.take() {
			if err := emit(line); err != nil {
				return err
			}
		}
		return nil
	}
	sawDone := false
	var lines neoPiSSELines
	handle := func(data string, trailing bool) (bool, error) {
		switch data {
		case "":
			return false, nil
		case "[DONE]":
			sawDone = true
			return true, nil
		}
		if !gjson.Valid(data) {
			if trailing && (strings.HasPrefix(strings.TrimLeftFunc(data, neoPiIsJSSpace), "{") || strings.HasPrefix(strings.TrimLeftFunc(data, neoPiIsJSSpace), "[")) {
				return true, nil
			}
			if err := neoPiInBandProviderErrorFromText(data); err != nil {
				return false, n.fail(statusErr{code: err.status, msg: settings.provider + ": " + err.message})
			}
			return false, n.responseError("invalid JSON in stream: " + neoPiTruncateUTF16(data, 200))
		}
		frame := gjson.Parse(data)
		if frame.Type == gjson.String {
			if err := neoPiInBandProviderErrorFromText(frame.String()); err != nil {
				return false, n.fail(statusErr{code: err.status, msg: settings.provider + ": " + err.message})
			}
			return false, nil
		}
		if err := n.feed(frame); err != nil {
			return false, err
		}
		return n.done, nil
	}

loop:
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case read := <-reads:
			if read.err != nil && !errors.Is(read.err, io.EOF) {
				return read.err
			}
			stopped, err := lines.write(read.data, read.err != nil, func(raw []byte) { helps.AppendAPIResponseChunk(ctx, cfg, raw) }, handle)
			if err != nil {
				if flushErr := flush(); flushErr != nil {
					return flushErr
				}
				return err
			}
			if err := flush(); err != nil {
				return err
			}
			if stopped || read.err != nil {
				break loop
			}
		}
	}

	finishErr := n.finish(sawDone)
	if err := flush(); err != nil {
		return err
	}
	if finishErr != nil {
		return finishErr
	}
	return emit([]byte("data: [DONE]"))
}

// neoPiSSELines frames an SSE body into dispatched `data` payloads the way
// readSseEvents does: CR, LF and CRLF end lines, data lines of one event join
// with LF, a blank line dispatches, and a tail without a final newline still
// counts (as a trailing event).
type neoPiSSELines struct {
	buffer  []byte
	data    []string
	hasData bool
}

func (s *neoPiSSELines) dispatch(handle func(string, bool) (bool, error), trailing bool) (bool, error) {
	if !s.hasData {
		return false, nil
	}
	data := strings.Join(s.data, "\n")
	s.data, s.hasData = nil, false
	return handle(data, trailing)
}

func (s *neoPiSSELines) push(line string, handle func(string, bool) (bool, error), trailing bool) (bool, error) {
	if line == "" {
		return s.dispatch(handle, trailing)
	}
	if line[0] == ':' {
		return false, nil
	}
	field, value, _ := strings.Cut(line, ":")
	if field == "data" {
		value = strings.TrimPrefix(value, " ")
		s.data = append(s.data, value)
		s.hasData = true
	}
	return false, nil
}

// write appends a read and handles every complete line; at end the tail and any
// pending event are dispatched as trailing.
func (s *neoPiSSELines) write(data []byte, end bool, logLine func([]byte), handle func(string, bool) (bool, error)) (bool, error) {
	s.buffer = append(s.buffer, data...)
	for {
		index := bytes.IndexAny(s.buffer, "\r\n")
		if index < 0 {
			break
		}
		if s.buffer[index] == '\r' && index+1 == len(s.buffer) && !end {
			break
		}
		line := s.buffer[:index]
		next := index + 1
		if s.buffer[index] == '\r' && next < len(s.buffer) && s.buffer[next] == '\n' {
			next++
		}
		logLine(line)
		stopped, err := s.push(string(line), handle, false)
		s.buffer = s.buffer[next:]
		if err != nil || stopped {
			return stopped, err
		}
	}
	if !end {
		return false, nil
	}
	if len(s.buffer) > 0 {
		tail := s.buffer
		s.buffer = nil
		logLine(tail)
		if stopped, err := s.push(string(tail), handle, true); err != nil || stopped {
			return stopped, err
		}
	}
	return s.dispatch(handle, true)
}
