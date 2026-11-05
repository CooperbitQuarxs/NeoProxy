package helps

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/githubcopilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// githubCopilotWorkingIntegration is copilotWorkingIntegrationCache: the
// Copilot-Integration-Id that last cleared the identity gate, per credential
// and host. Process-local; a stale entry costs at most one reverse retry.
var githubCopilotWorkingIntegration = cache.NewBoundedLRU[string, string](50, nil)

// GitHubCopilotIntegrationCacheKey is getCopilotIntegrationCacheKey: the
// credential's enterprise domain and API endpoint, the normalized effective
// base URL and a hash of the access token (token bytes never key the map),
// so one token on two hosts does not share a learned identity. Empty when
// the credential has no token.
func GitHubCopilotIntegrationCacheKey(credential *githubcopilot.Credential, baseURL string) string {
	if credential == nil {
		return ""
	}
	token := strings.TrimSpace(credential.AccessToken)
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return credential.EnterpriseURL + "\x00" + credential.APIEndpoint + "\x00" + normalizeGitHubCopilotCacheBaseURL(baseURL) + "\x00" + hex.EncodeToString(sum[:])
}

// normalizeGitHubCopilotCacheBaseURL is normalizeCopilotCacheBaseUrl: no
// trailing slashes and a lowercased host.
func normalizeGitHubCopilotCacheBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.ToLower(trimmed)
	}
	normalized := parsed.Scheme + "://" + strings.ToLower(parsed.Host) + strings.TrimRight(parsed.EscapedPath(), "/")
	if parsed.RawQuery != "" {
		normalized += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		normalized += "#" + parsed.EscapedFragment()
	}
	return normalized
}

// GitHubCopilotCachedIntegrationID is getCachedCopilotIntegrationId: the
// identity learned for a cache key, empty when none.
func GitHubCopilotCachedIntegrationID(cacheKey string) string {
	if cacheKey == "" {
		return ""
	}
	id, _ := githubCopilotWorkingIntegration.Get(cacheKey)
	return githubcopilot.NormalizeIntegrationID(id)
}

// GitHubCopilotIdentityFallback is wrapFetchForCopilotFallback over next. A
// chat-identity request denied as a client identity (HTTP 403, or HTTP 400
// with code model_not_supported) is reissued once as the Copilot CLI. A
// request that started from a learned CLI identity (snapshot, read when its
// headers were built) and is denied is reissued once as chat. Only a 2xx
// retry is remembered under cacheKey; any other retry clears the entry. A
// pinned identity is never second-guessed: pinned returns next unchanged.
func GitHubCopilotIdentityFallback(next http.RoundTripper, pinned bool, cacheKey, snapshot string) http.RoundTripper {
	if pinned {
		return next
	}
	if next == nil {
		next = http.DefaultTransport
	}
	return &githubCopilotIdentityFallback{next: next, cacheKey: cacheKey, snapshot: githubcopilot.NormalizeIntegrationID(snapshot)}
}

type githubCopilotIdentityFallback struct {
	next     http.RoundTripper
	cacheKey string
	snapshot string
}

func (f *githubCopilotIdentityFallback) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := f.next.RoundTrip(req)
	if err != nil || (resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest) {
		return resp, err
	}
	// A body that cannot be replayed cannot be reissued.
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return resp, nil
	}
	var retryID string
	switch req.Header.Get("Copilot-Integration-Id") {
	case githubcopilot.ChatIntegrationID:
		retryID = githubcopilot.CLIIntegrationID
	case githubcopilot.CLIIntegrationID:
		if f.cacheKey == "" || f.snapshot != githubcopilot.CLIIntegrationID {
			return resp, nil
		}
		retryID = githubcopilot.ChatIntegrationID
	default:
		return resp, nil
	}
	if !githubCopilotIdentityDenied(resp) {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("github-copilot: close denied response body: %v", errClose)
	}
	log.Warnf("GitHub Copilot %s identity denied (HTTP %d); retrying once as %s", req.Header.Get("Copilot-Integration-Id"), resp.StatusCode, retryID)
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		body, errBody := req.GetBody()
		if errBody != nil {
			return nil, errBody
		}
		retry.Body = body
	}
	retry.Header.Set("Copilot-Integration-Id", retryID)
	retryResp, err := f.next.RoundTrip(retry)
	if err != nil {
		return nil, err
	}
	if f.cacheKey != "" {
		if retryResp.StatusCode >= 200 && retryResp.StatusCode < 300 {
			githubCopilotWorkingIntegration.Set(f.cacheKey, retryID)
		} else {
			githubCopilotWorkingIntegration.Delete(f.cacheKey)
		}
	}
	return retryResp, nil
}

// githubCopilotIdentityDenied is isCopilotIdentityDenied: HTTP 403, or HTTP
// 400 whose JSON error code is model_not_supported. The 400 body is read and
// restored so the caller still sees it; an unreadable body is not a denial.
func githubCopilotIdentityDenied(resp *http.Response) bool {
	if resp.StatusCode == http.StatusForbidden {
		return true
	}
	if resp.StatusCode != http.StatusBadRequest || resp.Body == nil {
		return false
	}
	body, errRead := io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		log.Errorf("github-copilot: close response body: %v", errClose)
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if errRead != nil {
		return false
	}
	code := gjson.GetBytes(body, "error.code")
	return code.Type == gjson.String && code.Str == "model_not_supported"
}
