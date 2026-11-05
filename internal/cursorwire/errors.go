package cursorwire

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf16"
)

// Ports of the Cursor error surface: ConnectEndStreamError and the Connect
// classification in providers/cursor.ts, connect-error-detail.ts, and the
// parts of AIError's retry classification (isProviderRetryableError) Cursor
// failures reach.

// RunError is a classified Cursor failure.
type RunError struct {
	// Message is the classification message (Error.message).
	Message string
	// Diagnostic is ConnectEndStreamError.diagnosticMessage; "" otherwise.
	Diagnostic string
	// Status is ProviderHttpError.status; 0 for other kinds.
	Status int
	// Code is the Cursor error name of a structured error, or the Connect code.
	Code string
	// Kind is the ProviderResponseError kind: envelope, incomplete-stream,
	// runtime; "http" for ProviderHttpError, "transport" for socket failures.
	Kind string
	// Transient is the attached AIError Transient flag.
	Transient bool
	// Abort is AIError.AbortError.
	Abort bool
}

func (e *RunError) Error() string {
	if e.Diagnostic != "" {
		return e.Diagnostic
	}
	return e.Message
}

// cursorErrorNames is CURSOR_ERROR_NAMES.
var cursorErrorNames = map[int32]string{
	0: "UNSPECIFIED", 1: "BAD_API_KEY", 2: "NOT_LOGGED_IN", 3: "INVALID_AUTH_ID", 4: "NOT_HIGH_ENOUGH_PERMISSIONS",
	5: "BAD_MODEL_NAME", 6: "USER_NOT_FOUND", 7: "FREE_USER_RATE_LIMIT_EXCEEDED", 8: "PRO_USER_RATE_LIMIT_EXCEEDED",
	9: "FREE_USER_USAGE_LIMIT", 10: "PRO_USER_USAGE_LIMIT", 11: "AUTH_TOKEN_NOT_FOUND", 12: "AUTH_TOKEN_EXPIRED",
	13: "OPENAI", 14: "OPENAI_RATE_LIMIT_EXCEEDED", 18: "AGENT_REQUIRES_LOGIN", 20: "MAX_TOKENS",
	21: "USER_ABORTED_REQUEST", 22: "GENERIC_RATE_LIMIT_EXCEEDED", 23: "PRO_USER_ONLY", 25: "TIMEOUT",
	28: "GPT_4_VISION_PREVIEW_RATE_LIMIT", 29: "CUSTOM_MESSAGE", 30: "OUTDATED_CLIENT", 31: "CLAUDE_IMAGE_TOO_LARGE",
	33: "FILE_NOT_FOUND", 34: "API_KEY_RATE_LIMIT", 35: "DEBOUNCED", 36: "BAD_REQUEST",
	37: "REPOSITORY_SERVICE_REPOSITORY_IS_NOT_INITIALIZED", 38: "UNAUTHORIZED", 39: "NOT_FOUND", 40: "DEPRECATED",
	41: "RESOURCE_EXHAUSTED", 42: "BAD_USER_API_KEY", 43: "CONVERSATION_TOO_LONG", 44: "USAGE_PRICING_REQUIRED",
	45: "USAGE_PRICING_REQUIRED_CHANGEABLE", 46: "GITHUB_NO_USER_CREDENTIALS", 47: "GITHUB_USER_NO_ACCESS",
	48: "GITHUB_APP_NO_ACCESS", 49: "GITHUB_MULTIPLE_OWNERS", 50: "RATE_LIMITED", 51: "RATE_LIMITED_CHANGEABLE",
	52: "CUSTOM", 53: "HOOKS_BLOCKED", 54: "SUSPICIOUS_USAGE_BLOCKED", 55: "EXTENSION_HOST_TIMEOUT",
	56: "NETWORK_ERROR", 57: "PROVIDER_ERROR", 58: "MODEL_BLOCKED", 59: "INTERNAL", 60: "MAX_MODE_REQUIRED",
	61: "MODEL_NO_LONGER_SUPPORTED", 62: "PRICING_WARNING", 63: "SLOW_POOL", 64: "UNSUPPORTED_REGION", 65: "ACCOUNT_CLOSED",
}

// cursorErrorStatus is CURSOR_ERROR_STATUS.
var cursorErrorStatus = map[int32]int{
	1: 401, 2: 401, 3: 401, 4: 403, 5: 404, 6: 404, 7: 429, 8: 429, 9: 429, 10: 429, 11: 401, 12: 401, 14: 429,
	18: 401, 20: 413, 22: 429, 23: 403, 28: 429, 33: 404, 34: 429, 35: 429, 38: 401, 39: 404, 41: 429, 42: 401,
	43: 413, 44: 429, 45: 429, 46: 403, 47: 403, 48: 403, 49: 403, 50: 429, 51: 429, 54: 403, 58: 403, 61: 404,
	64: 403, 65: 401,
}

// cursorRetryableErrorCodes is CURSOR_RETRYABLE_ERROR_CODES.
var cursorRetryableErrorCodes = map[int32]bool{13: true, 25: true, 55: true, 56: true, 57: true, 59: true, 63: true}

const cursorErrorDetailsType = "aiserver.v1.ErrorDetails"

func stringField(object *JSONObject, key string) (string, bool) {
	if object == nil {
		return "", false
	}
	value, _ := object.Get(key)
	text, ok := value.(string)
	return text, ok
}

func objectField(object *JSONObject, key string) *JSONObject {
	if object == nil {
		return nil
	}
	value, _ := object.Get(key)
	inner, _ := value.(*JSONObject)
	return inner
}

// structuredError is decodeCursorStructuredError + classifyCursorStructuredError.
func structuredError(errorObject *JSONObject) (*RunError, bool) {
	raw, _ := errorObject.Get("details")
	details, _ := raw.([]any)
	for _, item := range details {
		entry, ok := item.(*JSONObject)
		if !ok {
			continue
		}
		kind, ok := stringField(entry, "type")
		if !ok || (kind != cursorErrorDetailsType && kind != "type.googleapis.com/"+cursorErrorDetailsType) {
			continue
		}
		encoded, _ := stringField(entry, "value")
		if encoded == "" {
			continue
		}
		var detail ErrorDetails
		if detail.Unmarshal(NodeBase64(encoded)) != nil {
			continue
		}
		name, known := cursorErrorNames[detail.Error]
		if !known {
			name = fmt.Sprintf("ERROR_%d", detail.Error)
		}
		var parts []string
		var retryable *bool
		if detail.Details != nil {
			for _, part := range []string{jsTrim(detail.Details.Title), jsTrim(detail.Details.Detail)} {
				if part != "" {
					parts = append(parts, part)
				}
			}
			retryable = detail.Details.IsRetryable
		}
		message := strings.Join(parts, ": ")
		if message == "" {
			message = name
		}
		full := "Cursor " + name + ": " + message
		if detail.Error == 21 {
			return &RunError{Message: full, Abort: true}, true
		}
		if status, ok := cursorErrorStatus[detail.Error]; ok {
			return &RunError{Message: full, Status: status, Code: name, Kind: "http"}, true
		}
		if (retryable != nil && *retryable) || (retryable == nil && cursorRetryableErrorCodes[detail.Error]) {
			return &RunError{Message: full, Status: 503, Code: name, Kind: "http"}, true
		}
		return &RunError{Message: full, Status: 400, Code: name, Kind: "http"}, true
	}
	return nil, false
}

// ClassifyConnectError is classifyConnectError over a Connect error object.
func ClassifyConnectError(errorObject *JSONObject) *RunError {
	if structured, ok := structuredError(errorObject); ok {
		return structured
	}
	code, ok := stringField(errorObject, "code")
	if !ok {
		code = "unknown"
	}
	message, ok := stringField(errorObject, "message")
	if !ok {
		message = "Unknown error"
	}
	details, _ := errorObject.Get("details")
	return &RunError{Message: "Connect error " + code + ": " + message, Diagnostic: formatConnectEndStreamError(errorObject),
		Code: code, Kind: "envelope", Transient: hasRetryableCursorErrorDetail(details)}
}

// ParseConnectEndStream is parseConnectEndStream: nil for a clean end.
func ParseConnectEndStream(data []byte) *RunError {
	decoded, ok := ParseJSON(strings.ToValidUTF8(string(data), "\uFFFD"))
	if !ok {
		return &RunError{Message: "Failed to parse Connect end stream", Kind: "envelope"}
	}
	errorObject := objectField(asObject(decoded), "error")
	if errorObject == nil {
		return nil
	}
	return ClassifyConnectError(errorObject)
}

func asObject(value any) *JSONObject {
	object, _ := value.(*JSONObject)
	return object
}

var genericConnectErrorMessages = map[string]bool{"": true, "error": true, "unknown": true, "unknown error": true, "internal": true, "internal error": true}

const maxExtraDetailChars = 400

// truncate is pi-utils truncate with its default ellipsis, in UTF-16 units.
func truncate(text string, limit int) string {
	units := utf16.Encode([]rune(text))
	if len(units) <= limit {
		return text
	}
	return string(utf16.Decode(units[:max(0, limit-1)])) + "…"
}

// safeJSON is safeJson: a string as is, anything else JSON.stringify'd.
func safeJSON(value any) (string, bool) {
	if text, ok := value.(string); ok {
		return text, text != ""
	}
	encoded := StringifyJSON(value)
	return encoded, encoded != ""
}

func summarizeConnectErrorDetails(details any) string {
	entries, ok := details.([]any)
	if !ok || len(entries) == 0 {
		return ""
	}
	var parts []string
	for _, item := range entries {
		entry, ok := item.(*JSONObject)
		if !ok {
			continue
		}
		kind, _ := stringField(entry, "type")
		var diagnostic string
		hasDiagnostic := false
		if debug, present := entry.Get("debug"); present {
			diagnostic, hasDiagnostic = safeJSON(debug)
		}
		if !hasDiagnostic {
			if value, present := entry.Get("value"); present {
				diagnostic, hasDiagnostic = safeJSON(value)
			}
		}
		switch {
		case kind != "" && hasDiagnostic:
			parts = append(parts, kind+": "+diagnostic)
		case kind != "":
			parts = append(parts, kind)
		case hasDiagnostic:
			parts = append(parts, diagnostic)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return truncate(strings.Join(parts, "; "), maxExtraDetailChars)
}

func hasRetryableCursorErrorDetail(details any) bool {
	entries, _ := details.([]any)
	for _, item := range entries {
		entry, ok := item.(*JSONObject)
		if kind, _ := stringField(entry, "type"); !ok || kind != cursorErrorDetailsType {
			continue
		}
		if inner := objectField(objectField(entry, "debug"), "details"); inner != nil {
			if retryable, _ := inner.Get("isRetryable"); retryable == true {
				return true
			}
		}
	}
	return false
}

// formatConnectEndStreamError is formatConnectEndStreamError.
func formatConnectEndStreamError(record *JSONObject) string {
	code, _ := stringField(record, "code")
	if code == "" {
		code = "unknown"
	}
	message, _ := stringField(record, "message")
	shown := message
	if shown == "" {
		shown = "Unknown error"
	}
	parts := []string{"Connect error " + code + ": " + shown}
	details, _ := record.Get("details")
	if detail := summarizeConnectErrorDetails(details); detail != "" {
		parts = append(parts, "[details: "+detail+"]")
	} else if genericConnectErrorMessages[strings.ToLower(jsTrim(message))] {
		extras := &JSONObject{}
		for _, key := range record.Keys() {
			if key != "code" && key != "message" {
				value, _ := record.Get(key)
				extras.Set(key, value)
			}
		}
		if extras.Len() > 0 {
			if raw := StringifyJSON(extras); raw != "" && raw != "{}" {
				parts = append(parts, "[trailer: "+truncate(raw, maxExtraDetailChars)+"]")
			}
		}
	}
	return strings.Join(parts, " ")
}

var (
	// transientTransportPattern is TRANSIENT_TRANSPORT_PATTERN (error/flags.ts).
	transientTransportPattern = regexp.MustCompile(`(?i)\b(?:no[_ -]?capacity|(?:high|peak)[ _-]?demand|(?:at|over|insufficient)[ _-]?capacity|capacity[ _-]?(?:exceeded|exhausted)|peak[ _-]?load)\b|overloaded|provider.?returned.?error|rate.?limit|too many requests|auth-gateway\s+5\d{2}(?:[:\s]|$)|\b(?:429|500|502|503|504)\b|service.?unavailable|temporar(?:y|ily)[ _-]?unavailable|server.?error|internal.?error|retry your request|network.?error|connection.?error|connection.?refused|unable.?to.?connect\.\s*is the computer able to access the url\?|other side closed|use of closed network connection|fetch failed|upstream.?connect|upstream.?request.?failed|reset before headers|socket hang up|timed? out|timeout|terminated|retry delay|stream stall|no error details in response|HTTP2(?:StreamReset|RefusedStream|EnhanceYourCalm)|nghttp2_(?:internal_error|refused_stream)|stream closed with error code nghttp2_(?:internal_error|refused_stream)|\bE(?:TIMEOUT|NOTFOUND|NETUNREACH|HOSTUNREACH)\b|\bFailedToOpenSocket\b|no internet connection|couldn.?t reach|couldn.?t connect to|tls certificate verification failed|malformed.?function.?call`)
	// usageLimitPattern is USAGE_LIMIT_PATTERN (error/rate-limit.ts).
	usageLimitPattern        = regexp.MustCompile(`(?i)usage.?limit|usage_limit_reached|usage_not_included|limit_reached|quota.?(?:exceeded|reached|insufficient)|额度不足|额度耗尽|resource.?exhausted|exhausted your capacity|quota will reset|insufficient.?(?:balance|quota)|balance.?exhausted|run out of credits|out of credits|out of (?:extra )?usage|spending[- _]?limit|personal-team-blocked|clinepass limit|free limit reached on model|access_terminated_error`)
	providerTransientExtra   = regexp.MustCompile(`(?i)bad record mac|stream error.*received from peer|1302`)
	socketClosePattern       = regexp.MustCompile(`(?i)\b(?:the\s+)?socket connection (?:was )?closed unexpectedly\b`)
	socketIsClosedPattern    = regexp.MustCompile(`(?i)^(?:error:\s*)?socket is closed\.?$`)
	transientMessagePattern  = regexp.MustCompile(`(?i)overloaded|rate.?limit|too many requests|service.?unavailable|server error|internal error|connection.?error|unable to connect|fetch failed|network error|stream stall|other side closed|HTTP2(?:StreamReset|RefusedStream|EnhanceYourCalm)`)
	validationMessagePattern = regexp.MustCompile(`(?i)invalid|validation|bad request|unsupported|schema|missing required|not found|unauthorized|forbidden`)
	timeoutAbortPattern      = regexp.MustCompile(`(?i)timeout|timed out|aborted`)
	// resourceExhaustedPattern is RESOURCE_EXHAUSTED_PATTERN.
	resourceExhaustedPattern = regexp.MustCompile(`(?i)resource.?exhausted`)
	// modelNotFoundPattern is CURSOR_MODEL_NOT_FOUND_PATTERN.
	modelNotFoundPattern = regexp.MustCompile(`(?i)^(?:Connect error not_found:|gRPC error 5:)`)
)

// UsageLimit is the usage-limit verdict (Flag.UsageLimit) for the text and
// status Cursor failures carry.
func (e *RunError) UsageLimit() bool {
	return usageLimitPattern.MatchString(e.Message) || e.Status == 402
}

// Retryable is isProviderRetryableError.
func (e *RunError) Retryable() bool {
	if e.Abort || e.UsageLimit() {
		return false
	}
	if e.Status >= 400 && e.Status < 500 && e.Status != 408 && e.Status != 429 {
		return false
	}
	if e.Kind == "transport" {
		return true
	}
	message := strings.ToLower(e.Message)
	if socketClosePattern.MatchString(message) || socketIsClosedPattern.MatchString(strings.TrimSpace(message)) ||
		strings.Contains(message, "tls: bad record mac") || strings.Contains(message, "type=server_error") ||
		transientTransportPattern.MatchString(message) || providerTransientExtra.MatchString(message) {
		return true
	}
	if timeoutAbortPattern.MatchString(message) {
		return true
	}
	if e.Status != 0 {
		if e.Status == 408 || e.Status == 429 || e.Status >= 500 {
			return true
		}
		if e.Status >= 400 {
			return false
		}
	}
	if validationMessagePattern.MatchString(message) {
		return false
	}
	return transientMessagePattern.MatchString(message)
}

// modelNotFound is isCursorModelNotFound.
func (e *RunError) modelNotFound() bool {
	return (e.Kind == "http" && e.Code == "BAD_MODEL_NAME") || modelNotFoundPattern.MatchString(e.Message)
}

// conversationRejected is isCursorConversationRejected.
func (e *RunError) conversationRejected() bool {
	return (e.Kind == "http" && e.Code == "PROVIDER_ERROR") || resourceExhaustedPattern.MatchString(e.Message)
}

// connectHTTPStatus maps Connect and gRPC codes onto HTTP statuses (the
// Connect protocol's own table) for callers that need one.
var connectHTTPStatus = map[string]int{
	"canceled": 499, "unknown": 500, "invalid_argument": 400, "deadline_exceeded": 504, "not_found": 404,
	"already_exists": 409, "permission_denied": 403, "resource_exhausted": 429, "failed_precondition": 400,
	"aborted": 409, "out_of_range": 400, "unimplemented": 501, "internal": 500, "unavailable": 503,
	"data_loss": 500, "unauthenticated": 401,
}

var grpcCodeNames = []string{"ok", "canceled", "unknown", "invalid_argument", "deadline_exceeded", "not_found", "already_exists",
	"permission_denied", "resource_exhausted", "failed_precondition", "aborted", "out_of_range", "unimplemented", "internal",
	"unavailable", "data_loss", "unauthenticated"}

// HTTPStatus is the status a caller reports for the failure.
func (e *RunError) HTTPStatus() int {
	switch {
	case e.Status != 0:
		return e.Status
	case e.Abort:
		return 499
	case e.Kind == "envelope" && e.Code != "":
		if status, ok := connectHTTPStatus[e.Code]; ok {
			if e.Transient && status < 500 {
				return 503
			}
			return status
		}
	}
	if e.Transient {
		return 503
	}
	return 502
}

// grpcTrailerError is the gRPC trailer failure.
func grpcTrailerError(status, message string) *RunError {
	code := ""
	var n int
	if _, err := fmt.Sscan(status, &n); err == nil && n >= 0 && n < len(grpcCodeNames) {
		code = grpcCodeNames[n]
	}
	return &RunError{Message: "gRPC error " + status + ": " + message, Kind: "envelope", Code: code}
}
