package alibabatokenplan

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

const (
	usageAPI         = "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage"
	browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
	bunUserAgent     = "Bun/1.3.14"
	maxUsageBody     = 1 << 20
	hourMs           = int64(time.Hour / time.Millisecond)
	weekMs           = 7 * 24 * hourMs
)

// consoleConfig is INTERNATIONAL_CONSOLE or CHINA_CONSOLE.
type consoleConfig struct {
	origin         string
	dashboardURL   string
	sessionURL     string
	sessionAccept  string
	gatewayAction  string
	region         string
	usageURL       string
	source         string
	cornerstoneFmt func(traceID string) string
}

var internationalConsole = consoleConfig{
	origin:        "https://home.qwencloud.com",
	dashboardURL:  "https://home.qwencloud.com/billing/subscription/token-plan-individual",
	sessionURL:    "https://home.qwencloud.com/tool/user/info.json",
	sessionAccept: "application/json, text/plain, */*",
	gatewayAction: "IntlBroadScopeAspnGateway",
	region:        "ap-southeast-1",
	usageURL:      "https://cs-data.qwencloud.com/data/api.json?product=sfm_bailian&action=IntlBroadScopeAspnGateway&api=" + url.QueryEscape(usageAPI),
	source:        "qwencloud-console",
	cornerstoneFmt: func(string) string {
		return `{"domain":"home.qwencloud.com","consoleSite":"QWENCLOUD","console":"ONE_CONSOLE","xsp_lang":"en-US","protocol":"V2","productCode":"p_efm"}`
	},
}

var chinaConsole = consoleConfig{
	origin:        "https://bailian.console.aliyun.com",
	dashboardURL:  "https://bailian.console.aliyun.com/cn-beijing?tab=plan",
	sessionURL:    "https://bailian.console.aliyun.com/cn-beijing?tab=plan",
	sessionAccept: "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8",
	gatewayAction: "BroadScopeAspnGateway",
	region:        "cn-beijing",
	usageURL:      "https://bailian-cs.console.aliyun.com/data/api.json?action=BroadScopeAspnGateway&product=sfm_bailian&api=" + url.QueryEscape(usageAPI),
	source:        "bailian-console",
	cornerstoneFmt: func(traceID string) string {
		return `{"feTraceId":` + jsString(traceID) + `,"feURL":"https://bailian.console.aliyun.com/cn-beijing?tab=plan#/efm/subscription/token-plan/personal","protocol":"V2","console":"ONE_CONSOLE","productCode":"p_efm","switchUserType":3,"domain":"bailian.console.aliyun.com","consoleSite":"BAILIAN_ALIYUN","userNickName":"","userPrincipalName":"","xsp_lang":"zh-CN"}`
	},
}

// Usage is NeoPi's Token Plan usage report (fetchAlibabaTokenPlanUsage).
type Usage struct {
	Limits []UsageLimit
	// AccountID is the QwenCloud account (International console only).
	AccountID string
	// Source names the console that answered.
	Source string
}

// UsageLimit is one credit window.
type UsageLimit struct {
	// WindowID is "5h", "7d" or "monthly".
	WindowID string
	// DurationMs is zero for the monthly window, whose span the console does
	// not state.
	DurationMs int64
	ResetsAt   time.Time
	// UsedFraction is 0..1.
	UsedFraction float64
}

// ErrNoUsage is a null report: NeoPi logs a warning and shows no usage for the
// account.
var ErrNoUsage = errors.New("alibaba token plan usage: no report")

// FetchUsage reads the account's credit windows through the console session its
// Cookie header carries. china selects the Beijing console (credential.baseUrl
// is the China endpoint).
func FetchUsage(ctx context.Context, client *http.Client, cookie string, china bool) (*Usage, error) {
	if strings.TrimSpace(cookie) == "" {
		return nil, ErrNoUsage
	}
	console := internationalConsole
	if china {
		console = chinaConsole
	}
	session, err := newRequest(ctx, http.MethodGet, console.sessionURL, nil)
	if err != nil {
		return nil, err
	}
	session.Header.Set("Accept", console.sessionAccept)
	session.Header.Set("Cookie", cookie)
	session.Header.Set("Referer", console.origin+"/")
	session.Header.Set("User-Agent", browserUserAgent)
	sessionBody, err := do(client, session)
	if err != nil {
		return nil, fmt.Errorf("alibaba token plan session lookup: %w", err)
	}

	var secToken, accountID string
	if china {
		match := secTokenPattern.FindSubmatch(sessionBody)
		if match == nil {
			return nil, fmt.Errorf("alibaba token plan China session response invalid")
		}
		secToken = string(match[1])
	} else {
		user := gjson.ParseBytes(sessionBody)
		data := user.Get("data")
		if !user.IsObject() || !data.IsObject() || data.Get("secToken").Type != gjson.String {
			return nil, fmt.Errorf("QwenCloud session response invalid")
		}
		secToken = data.Get("secToken").String()
		accountID = accountIDFromUserData(data)
	}

	traceID := ""
	if china {
		traceID = randomUUID()
	}
	params := `{"Api":` + jsString(usageAPI) + `,"Data":{"cornerstoneParam":` + console.cornerstoneFmt(traceID) + `},"V":"1.0"}`
	body := formEncode([][2]string{
		{"product", "sfm_bailian"},
		{"action", console.gatewayAction},
		{"region", console.region},
		{"sec_token", secToken},
		{"params", params},
	})
	usage, err := newRequest(ctx, http.MethodPost, console.usageURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	usage.Header.Set("Accept", "application/json, text/plain, */*")
	usage.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	usage.Header.Set("Cookie", cookie)
	usage.Header.Set("Origin", console.origin)
	usage.Header.Set("Referer", console.dashboardURL)
	usage.Header.Set("User-Agent", browserUserAgent)
	usage.Header.Set("X-Requested-With", "XMLHttpRequest")
	if csrf := cookieValue(cookie, "login_aliyunid_csrf"); csrf != "" {
		usage.Header.Set("x-xsrf-token", csrf)
		usage.Header.Set("x-csrf-token", csrf)
	} else if csrf = cookieValue(cookie, "csrf"); csrf != "" {
		usage.Header.Set("x-xsrf-token", csrf)
		usage.Header.Set("x-csrf-token", csrf)
	}
	payloadBody, err := do(client, usage)
	if err != nil {
		return nil, fmt.Errorf("alibaba token plan usage fetch: %w", err)
	}
	payload := gjson.ParseBytes(payloadBody)
	data := payload.Get("data")
	if !payload.IsObject() || payload.Get("successResponse").Type == gjson.False || !data.IsObject() {
		return nil, fmt.Errorf("alibaba token plan usage response invalid")
	}
	if data.Get("success").Type == gjson.False {
		code := "unknown"
		if c := data.Get("errorCode"); c.Type == gjson.String {
			code = c.String()
		}
		return nil, fmt.Errorf("alibaba token plan usage request rejected (%s)", code)
	}
	response := unwrapGatewayData(data)
	report := &Usage{AccountID: accountID, Source: console.source}
	for _, spec := range []struct {
		id         string
		durationMs int64
		percentage string
		resetTime  string
	}{
		{"5h", 5 * hourMs, "per5HourPercentage", "per5HourResetTime"},
		{"7d", weekMs, "per1WeekPercentage", "per1WeekResetTime"},
		// Monthly-only plans report just this bucket, and the console never
		// states its span (calendar months differ), so the window carries a
		// reset deadline without a duration.
		{"monthly", 0, "per1MonthPercentage", "per1MonthResetTime"},
	} {
		fraction, ok := parseUsedFraction(response.Get(spec.percentage))
		if !ok {
			continue
		}
		limit := UsageLimit{WindowID: spec.id, DurationMs: spec.durationMs, UsedFraction: fraction}
		if resetsAt, ok := parsePositiveTimestamp(response.Get(spec.resetTime)); ok {
			limit.ResetsAt = time.UnixMilli(resetsAt)
		}
		report.Limits = append(report.Limits, limit)
	}
	if len(report.Limits) == 0 {
		return nil, ErrNoUsage
	}
	return report, nil
}

var secTokenPattern = regexp.MustCompile(`\bSEC_TOKEN\s*:\s*"([^"]+)"`)

func newRequest(ctx context.Context, method, target string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	// Bun's fetch defaults beside NeoPi's explicit headers.
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", bunUserAgent)
	return req, nil
}

// do sends req and returns the decoded 2xx body; the console never follows a
// redirect with the session cookie (NeoPi's redirect: "manual").
func do(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = decoded.Close() }()
	return io.ReadAll(io.LimitReader(decoded, maxUsageBody))
}

// cookieValue is extractCookieValue: the first non-empty value of name.
func cookieValue(header, name string) string {
	for _, segment := range strings.Split(header, ";") {
		at := strings.Index(segment, "=")
		if at < 0 || strings.TrimSpace(segment[:at]) != name {
			continue
		}
		return strings.TrimSpace(segment[at+1:])
	}
	return ""
}

// unwrapGatewayData is unwrapGatewayData: the gateway nests the report in a
// JSON string Data, then DataV2.data, then data.
func unwrapGatewayData(value gjson.Result) gjson.Result {
	current := value
	if text := current.Get("Data"); text.Type == gjson.String {
		if !gjson.Valid(text.String()) {
			return current
		}
		if parsed := gjson.Parse(text.String()); parsed.IsObject() {
			current = parsed
		}
	}
	if inner := current.Get("DataV2.data"); inner.IsObject() {
		current = inner
	}
	if inner := current.Get("data"); inner.IsObject() {
		current = inner
	}
	return current
}

// parseUsedFraction is parseUsedFraction: a percentage above 1 is 0..100.
func parseUsedFraction(value gjson.Result) (float64, bool) {
	parsed, ok := toNumber(value)
	if !ok || parsed < 0 {
		return 0, false
	}
	if parsed > 1 {
		parsed /= 100
	}
	return math.Min(1, parsed), true
}

// parsePositiveTimestamp is parsePositiveTimestamp: epoch seconds or milliseconds.
func parsePositiveTimestamp(value gjson.Result) (int64, bool) {
	parsed, ok := toNumber(value)
	if !ok || parsed <= 0 {
		return 0, false
	}
	if parsed < 1_000_000_000_000 {
		parsed *= 1000
	}
	return int64(parsed), true
}

// toNumber is toNumber: finite numbers and non-blank numeric strings.
func toNumber(value gjson.Result) (float64, bool) {
	switch value.Type {
	case gjson.Number:
		return value.Float(), !math.IsInf(value.Float(), 0) && !math.IsNaN(value.Float())
	case gjson.String:
		text := strings.TrimSpace(value.String())
		if text == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(text, 64)
		return parsed, err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
	}
	return 0, false
}

// accountIDFromUserData is accountIdFromUserData.
func accountIDFromUserData(data gjson.Result) string {
	for _, key := range []string{"accountId", "userId", "aliyunId", "loginId"} {
		candidate := data.Get(key)
		switch candidate.Type {
		case gjson.String:
			if id := strings.TrimSpace(candidate.String()); id != "" {
				return id
			}
		case gjson.Number:
			return candidate.Raw
		}
	}
	return ""
}

// jsString is JSON.stringify of a string.
func jsString(text string) string {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(text)
	return strings.TrimSuffix(out.String(), "\n")
}

// formEncode is URLSearchParams.toString: pairs in order, space as +, and the
// application/x-www-form-urlencoded byte set (alphanumerics and *-._ stay).
func formEncode(pairs [][2]string) string {
	var out strings.Builder
	for i, pair := range pairs {
		if i > 0 {
			out.WriteByte('&')
		}
		formEscape(&out, pair[0])
		out.WriteByte('=')
		formEscape(&out, pair[1])
	}
	return out.String()
}

func formEscape(out *strings.Builder, text string) {
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '*', c == '-', c == '.', c == '_':
			out.WriteByte(c)
		case c == ' ':
			out.WriteByte('+')
		default:
			out.WriteByte('%')
			out.WriteByte(hex[c>>4])
			out.WriteByte(hex[c&15])
		}
	}
}

// randomUUID is crypto.randomUUID: a version 4 UUID.
func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
