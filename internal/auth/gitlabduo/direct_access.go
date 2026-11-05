package gitlabduo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopiversion"
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
	data, err := io.ReadAll(resp.Body)
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
	headers.ForEach(func(name, value gjson.Result) bool {
		if headers.IsObject() {
			text := value.String()
			if value.Type != gjson.String {
				text = value.Raw
			}
			minted.Headers.Set(name.Str, text)
		}
		return true
	})
	return minted, nil
}
