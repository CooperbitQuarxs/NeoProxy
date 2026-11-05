package openaiwire

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// InBandError is a provider failure reported inside an HTTP 200 stream, either
// in a structured error frame or as gateway text.
type InBandError struct {
	// Status is the reported or named-code HTTP status, or 0 when unknown.
	Status  int
	Message string
	// Event is the original structured failure frame, when one was sent.
	Event     []byte
	retryable bool
}

func (e *InBandError) Error() string { return e.Message }

const (
	maxInBandDetailChars   = 4096
	inBandDetailPlaceholer = "Provider returned an in-band provider error"
)

var (
	inBandRetryableText = regexp.MustCompile(`(?i)\brate.?limit|too many requests|too\s+many\s+concurren|service.{0,20}unavailable|temporarily\s+unavailable|server.?error|internal.?error|overloaded|capacity|throttl|retry\s+(?:your\s+)?request|please\s+retry`)
	inBandLeadingStatus = regexp.MustCompile(`(?i)^\s*(?:HTTP[/.]\d(?:\.\d)?\s+)?([45]\d{2})(?:\b|$)`)
	inBandMarkup        = regexp.MustCompile(`<[^>]*>`)
	inBandControl       = regexp.MustCompile(`[\x00-\x1f\x7f]+`)
	inBandSpace         = regexp.MustCompile(`\s+`)

	inBandNonRetryableCode = regexp.MustCompile(`(?i)insufficient.?quota|usage.?limit|quota.?(?:exceeded|reached|insufficient)|invalid_request|content_filter|context_length|context_window|billing|balance`)
	inBandCamelBoundary    = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	inBandCodeSeparators   = regexp.MustCompile(`[-.\s]+`)
	inBandCodeUnderscores  = regexp.MustCompile(`_+`)
	inBandStatusField      = regexp.MustCompile(`^\d{3}$`)
	inBandOpaqueFraming    = regexp.MustCompile(`(?i)\b(?:429|402|http|https|status|error|code|response|message)\b|\(?\bno body\b\)?`)
	inBandReadableDetail   = regexp.MustCompile(`[a-zA-Z0-9]{3,}|[^\x00-\x7f]`)

	// These mirror the text flags used by isProviderRetryableError. A transient
	// status does not override an account-local quota or credential refusal.
	providerTransientText     = regexp.MustCompile(`(?i)\b(?:no[_ -]?capacity|(?:high|peak)[ _-]?demand|(?:at|over|insufficient)[ _-]?capacity|capacity[ _-]?(?:exceeded|exhausted)|peak[ _-]?load)\b|overloaded|provider.?returned.?error|rate.?limit|too many requests|auth-gateway\s+5\d{2}(?:[:\s]|$)|\b(?:429|500|502|503|504)\b|service.?unavailable|temporar(?:y|ily)[ _-]?unavailable|server.?error|internal.?error|retry your request|network.?error|connection.?error|connection.?refused|unable.?to.?connect\.\s*is the computer able to access the url\?|other side closed|use of closed network connection|fetch failed|upstream.?connect|upstream.?request.?failed|reset before headers|socket hang up|timed? out|timeout|terminated|retry delay|stream stall|no error details in response|HTTP2(?:StreamReset|RefusedStream|EnhanceYourCalm)|nghttp2_(?:internal_error|refused_stream)|stream closed with error code nghttp2_(?:internal_error|refused_stream)|\bE(?:TIMEOUT|NOTFOUND|NETUNREACH|HOSTUNREACH)\b|\bFailedToOpenSocket\b|no internet connection|couldn.?t reach|couldn.?t connect to|tls certificate verification failed|malformed.?function.?call|bad record mac|stream error.*received from peer|1302`)
	providerUsageLimitText    = regexp.MustCompile(`(?i)usage.?limit|usage_limit_reached|usage_not_included|limit_reached|quota.?(?:exceeded|reached|insufficient)|额度不足|额度耗尽|resource.?exhausted|exhausted your capacity|quota will reset|insufficient.?(?:balance|quota)|balance.?exhausted|run out of credits|out of credits|out of (?:extra )?usage|spending[- _]?limit|personal-team-blocked|clinepass limit|free limit reached on model|access_terminated_error|\busage credits are required\b|\bcredits_required\b|\busage_pricing_required\b|\bprepaid balance\b[^\n]{0,40}\b(?:used up|exhausted|depleted)\b|\b(?:exceed\w*|insufficient|not enough)\b[^\n]{0,40}\bcredits?\b|\bcredits?\b[^\n]{0,40}\b(?:exhausted|depleted)\b|spend(?:ing)?[\s_-]?(?:limit|cap)\b|\baccount(?:'s)?\b[^\n]{0,80}\brate.?limit\b|\brate.?limit\b[^\n]{0,80}\baccount\b|\bfree[-_ ]models[-_ ]per[-_ ]day\b`)
	providerSubscriptionCap   = regexp.MustCompile(`(?i)\b(?:subscription|plan|membership)\b[^\n]{0,80}\b(?:rate.?limits?|quota|cap)\b|\b(?:rate.?limits?|quota|cap)\b[^\n]{0,80}\b(?:subscription|plan|membership)\b`)
	providerTransientInterval = regexp.MustCompile(`(?i)\bper\s+(?:second|minute)\b`)
	providerConcurrentLimit   = regexp.MustCompile(`(?i)\btoo many\s+concurren\w*\s+(?:requests?|invocations?)\b|\bconcurren\w*\b[^\n]{0,60}\b(?:limit|quota|exceed\w*|reach\w*)\b|\b(?:limit|quota|exceed\w*|reach\w*)\b[^\n]{0,60}\bconcurren\w*\b|\bconcurren[a-z]*[-_](?:[a-z]+[_-])*(?:limit|quota|exceed\w*|reach\w*)`)
	providerCredentialScope   = regexp.MustCompile(`(?i)\bcredential[_ -]scope\b`)
	providerQuotaReason       = regexp.MustCompile(`(?i)\bquota\b|\bexhausted\b`)
	providerTransientReason   = regexp.MustCompile(`(?i)capacity|overloaded|529|503|per minute|rate limit|too many requests|presque`)
	// Malformed output needs an agent corrective reminder, not a replay.
	providerMalformedOutput = regexp.MustCompile(`(?i)\b(?:malformed[_. ]?(?:function[_. ]?call|response)|unexpected[_. ]?tool[_. ]?call)\b`)
)

// classifyInBandFrame mirrors readInBandSignal/createInBandProviderError.
// Only explicit retryable status fields or known throttle codes establish an
// HTTP status; a status mentioned in prose never establishes one.
func classifyInBandFrame(frame []byte) *InBandError {
	root := gjson.ParseBytes(frame)
	if !root.IsObject() {
		return nil
	}
	if root.Get("type").String() == "response.failed" ||
		terminalEvent(root.Get("type").String()) && (root.Get("response.status").String() == "failed" || root.Get("response.status").String() == "cancelled") {
		if root.Get("response").IsObject() {
			root = root.Get("response")
		}
	}
	nested := root.Get("error")
	if !nested.Exists() || nested.Type == gjson.Null {
		nested = root.Get("status_details.error")
	}
	if !nested.Exists() || nested.Type == gjson.Null {
		nested = root.Get("response.error")
	}
	holder := root
	if nested.IsObject() {
		holder = nested
		for depth := 0; depth < 2; depth++ {
			inner := holder.Get("error")
			if !inner.IsObject() {
				break
			}
			holder = inner
		}
	}
	codeField := holder.Get("code")
	if !codeField.Exists() || codeField.Type == gjson.Null {
		codeField = root.Get("code")
	}
	code := normalizeInBandCode(codeField)
	if code == "" {
		typeField := holder.Get("type")
		if !typeField.Exists() || typeField.Type == gjson.Null {
			typeField = root.Get("type")
		}
		code = normalizeInBandCode(typeField)
	}
	if inBandNonRetryableCode.MatchString(code) {
		return nil
	}
	if !inBandRetryableText.MatchString(code) && !nested.Exists() &&
		!root.Get("status").Exists() && !root.Get("code").Exists() && !root.Get("message").Exists() {
		return nil
	}
	detail := inBandDetail(holder.Get("message"))
	if detail == "" {
		detail = inBandDetail(root.Get("message"))
	}
	if detail == "" {
		detail = inBandDetail(nested)
	}
	status := 0
	for _, value := range []gjson.Result{holder.Get("status"), holder.Get("code"), root.Get("status"), root.Get("code")} {
		if reported := readInBandStatus(value); reported == 429 || reported >= 500 && reported <= 599 {
			status = reported
			break
		}
	}
	if status == 0 {
		status = inBandStatusByCode(code)
	}
	if status == 0 && (detail == "" || !inBandRetryableText.MatchString(detail)) {
		return nil
	}
	message := detail
	if status != 0 {
		if message == "" {
			message = inBandDetailPlaceholer
		}
		if lead := inBandLeadingStatus.FindStringSubmatch(message); lead == nil || lead[1] != strconv.Itoa(status) {
			message = fmt.Sprintf("%d %s", status, message)
		}
		if !inBandReadableDetail.MatchString(inBandOpaqueFraming.ReplaceAllString(message, "")) {
			message = fmt.Sprintf("%d %s", status, inBandDetailPlaceholer)
		}
		if code != "" && !inBandStatusField.MatchString(code) && !strings.Contains(strings.ToLower(message), code) {
			message += " (" + code + ")"
		}
	} else if code != "" {
		message += " (" + code + ")"
	}
	return &InBandError{Status: status, Message: message, Event: frame, retryable: !providerLocalRetryExcluded(message, status)}
}

func normalizeInBandCode(value gjson.Result) string {
	if value.Type == gjson.Number {
		return strconv.FormatFloat(value.Float(), 'f', -1, 64)
	}
	if value.Type != gjson.String {
		return ""
	}
	code := inBandCamelBoundary.ReplaceAllString(strings.TrimSpace(value.String()), "${1}_${2}")
	code = inBandCodeSeparators.ReplaceAllString(strings.ToLower(code), "_")
	return inBandCodeUnderscores.ReplaceAllString(code, "_")
}

func readInBandStatus(value gjson.Result) int {
	if value.Type == gjson.Number {
		number := value.Float()
		status := int(number)
		if number == float64(status) && status >= 100 && status <= 599 {
			return status
		}
		return 0
	}
	if value.Type != gjson.String {
		return 0
	}
	text := strings.TrimSpace(value.String())
	if !inBandStatusField.MatchString(text) {
		return 0
	}
	status, _ := strconv.Atoi(text)
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func inBandStatusByCode(code string) int {
	switch code {
	case "rate_limit_error", "rate_limit_exceeded", "rate_limit", "rate_limit_reached",
		"rate_limited", "ratelimit", "too_many_requests", "request_throttled", "throttled",
		"throttling", "throttling_error", "throttling_exception", "throttling_allocation_quota",
		"request_limit_exceeded", "retry_later":
		return 429
	case "overloaded_error", "server_overloaded", "model_overloaded", "overloaded",
		"service_unavailable", "server_busy", "high_demand", "capacity_exceeded":
		return 503
	}
	return 0
}

func inBandDetail(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	detail := inBandMarkup.ReplaceAllString(value.String(), " ")
	detail = inBandControl.ReplaceAllString(detail, " ")
	detail = strings.TrimSpace(inBandSpace.ReplaceAllString(detail, " "))
	if len(detail) > maxInBandDetailChars {
		detail = detail[:maxInBandDetailChars]
	}
	return detail
}

func providerLocalRetryExcluded(message string, status int) bool {
	if providerCredentialScope.MatchString(message) {
		return true
	}
	// Concurrency ceilings are temporary even when their wording says quota.
	if providerConcurrentLimit.MatchString(message) {
		return false
	}
	return providerUsageLimitText.MatchString(message) ||
		providerSubscriptionCap.MatchString(message) && !providerTransientInterval.MatchString(message) ||
		status == 429 && providerQuotaReason.MatchString(message) && !providerTransientReason.MatchString(message)
}

func providerRetryableMessage(message string) bool {
	return !providerLocalRetryExcluded(message, 0) && providerTransientText.MatchString(message)
}

// classifyInBandText is createInBandProviderErrorFromText: nil when the text is
// not recognizable as a throttle.
func classifyInBandText(text string) *InBandError {
	detail := inBandMarkup.ReplaceAllString(text, " ")
	detail = inBandControl.ReplaceAllString(detail, " ")
	detail = strings.TrimSpace(inBandSpace.ReplaceAllString(detail, " "))
	if detail == "" || !inBandRetryableText.MatchString(detail) {
		return nil
	}
	if len(detail) > maxInBandDetailChars {
		detail = detail[:maxInBandDetailChars]
	}
	status := 0
	if match := inBandLeadingStatus.FindStringSubmatch(detail); match != nil {
		status, _ = strconv.Atoi(match[1])
	}
	if status == 429 || status >= 500 && status <= 599 {
		message := detail
		if lead := inBandLeadingStatus.FindStringSubmatch(detail); lead == nil || lead[1] != strconv.Itoa(status) {
			message = fmt.Sprintf("%d %s", status, detail)
		}
		return &InBandError{Status: status, Message: message, retryable: !providerLocalRetryExcluded(message, status)}
	}
	return &InBandError{Message: detail, retryable: !providerLocalRetryExcluded(detail, 0)}
}
