package gitlabduo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiversion"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

const (
	// DirectAccessURL mints the AI gateway token a GitLab token buys.
	DirectAccessURL = "https://gitlab.com/api/v4/ai/third_party_agents/direct_access"
	// directAccessTTL is how long NeoPi reuses a minted token.
	directAccessTTL = 25 * time.Minute
	// directAccessBody is the mint's fixed body.
	directAccessBody = `{"feature_flags":{"DuoAgentPlatformNext":true}}`
)

// DirectAccess is a minted AI gateway token and the headers GitLab says to
// send beside it.
type DirectAccess struct {
	Token     string
	Headers   http.Header
	ExpiresAt time.Time
}

// Error is a failed mint: the status a client sees and NeoPi's message.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// directAccessCache is NeoPi's process-wide cache, keyed by GitLab token.
var directAccessCache = struct {
	sync.Mutex
	tokens map[string]DirectAccess
}{tokens: map[string]DirectAccess{}}

// ClearDirectAccessCache is clearGitLabDuoDirectAccessCache: a sign-in or a
// refresh drops every minted token.
func ClearDirectAccessCache() {
	directAccessCache.Lock()
	directAccessCache.tokens = map[string]DirectAccess{}
	directAccessCache.Unlock()
}

// GetDirectAccess is getDirectAccessToken: the cached token for gitlabToken
// while it is fresh, else a new mint.
func GetDirectAccess(ctx context.Context, client *http.Client, gitlabToken string) (DirectAccess, error) {
	directAccessCache.Lock()
	cached, ok := directAccessCache.tokens[gitlabToken]
	directAccessCache.Unlock()
	if ok && cached.ExpiresAt.After(now()) {
		return cached, nil
	}
	minted, err := mintDirectAccess(ctx, client, gitlabToken)
	if err != nil {
		return DirectAccess{}, err
	}
	directAccessCache.Lock()
	directAccessCache.tokens[gitlabToken] = minted
	directAccessCache.Unlock()
	return minted, nil
}

func mintDirectAccess(ctx context.Context, client *http.Client, gitlabToken string) (DirectAccess, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DirectAccessURL, bytes.NewReader([]byte(directAccessBody)))
	if err != nil {
		return DirectAccess{}, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Authorization", "Bearer "+gitlabToken)
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "npi/"+neopiversion.Version)
	resp, err := client.Do(req)
	if err != nil {
		return DirectAccess{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	// The request advertises encodings, so Go leaves decoding to us; NeoPi's
	// fetch decodes them before parsing the JSON.
	decoded, err := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		return DirectAccess{}, err
	}
	defer func() { _ = decoded.Close() }()
	data, err := io.ReadAll(decoded)
	if err != nil {
		return DirectAccess{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusForbidden {
			return DirectAccess{}, &Error{Status: http.StatusForbidden, Message: "GitLab Duo access denied. Ensure Duo is enabled for this account. " + string(data)}
		}
		return DirectAccess{}, &Error{Status: resp.StatusCode, Message: fmt.Sprintf("Failed to get GitLab Duo direct access token: %d %s", resp.StatusCode, data)}
	}
	if !gjson.ValidBytes(data) {
		return DirectAccess{}, &Error{Status: http.StatusBadGateway, Message: "GitLab Duo direct access response is not JSON"}
	}
	payload := gjson.ParseBytes(data)
	token := payload.Get("token")
	if token.Type != gjson.String || token.Str == "" {
		return DirectAccess{}, &Error{Status: http.StatusBadGateway, Message: "GitLab Duo direct access response missing token"}
	}
	headers := payload.Get("headers")
	if !headers.IsObject() && !headers.IsArray() {
		return DirectAccess{}, &Error{Status: http.StatusBadGateway, Message: "GitLab Duo direct access response missing headers"}
	}
	minted := DirectAccess{Token: token.Str, Headers: http.Header{}, ExpiresAt: now().Add(directAccessTTL)}
	// NeoPi spreads these into a Headers object: an array spreads its indices.
	index := 0
	headers.ForEach(func(name, value gjson.Result) bool {
		key := name.Str
		if headers.IsArray() {
			key = strconv.Itoa(index)
			index++
		}
		minted.Headers.Set(key, jsString(value))
		return true
	})
	return minted, nil
}

// jsString is JavaScript's String(value) for a JSON value: arrays join their
// items with commas (null as empty), objects are "[object Object]", numbers
// print as Number.prototype.toString does.
func jsString(value gjson.Result) string {
	switch {
	case value.Type == gjson.String:
		return value.Str
	case value.Type == gjson.Number:
		return jsNumber(value.Num)
	case value.IsArray():
		var parts []string
		value.ForEach(func(_, item gjson.Result) bool {
			if item.Type == gjson.Null {
				parts = append(parts, "")
			} else {
				parts = append(parts, jsString(item))
			}
			return true
		})
		return strings.Join(parts, ",")
	case value.IsObject():
		return "[object Object]"
	default:
		return value.Raw
	}
}

// jsNumber is Number.prototype.toString for a finite JSON number: the
// shortest round-trip digits, fixed between 1e-6 and 1e21, else exponent
// form with a signed, unpadded exponent; -0 prints as 0.
func jsNumber(number float64) string {
	if number == 0 {
		return "0"
	}
	if magnitude := math.Abs(number); magnitude >= 1e21 || magnitude < 1e-6 {
		mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(number, 'e', -1, 64), "e")
		power, _ := strconv.Atoi(exponent)
		sign := "+"
		if power < 0 {
			sign, power = "-", -power
		}
		return mantissa + "e" + sign + strconv.Itoa(power)
	}
	return strconv.FormatFloat(number, 'f', -1, 64)
}
