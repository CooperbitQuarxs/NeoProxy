package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/ollamawire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/openaiwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// ollamaCloudDiscoveryWait sleeps between /api/tags attempts; tests replace
// it.
var ollamaCloudDiscoveryWait = neoPiWait

// ollamaCloudDiscoveryRequest is one discovery call as Bun's fetch puts it
// on the wire (discovery does not go through NeoPi's inference transport).
func ollamaCloudDiscoveryRequest(ctx context.Context, client *http.Client, method, url, key string, body []byte) (int, http.Header, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Connection", "keep-alive")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("User-Agent", openaiwire.BunUserAgent)
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	decoded, err := util.DecodeResponseBody(response.Body, response.Header.Get("Content-Encoding"))
	if err != nil {
		return response.StatusCode, response.Header, nil, err
	}
	data, err := io.ReadAll(decoded)
	_ = decoded.Close()
	return response.StatusCode, response.Header, data, err
}

// FetchOllamaCloudModels is ollamaCloudModelManagerOptions' fetchDynamicModels
// under NeoPi's model manager: the account's /api/tags listing (retried on
// Ollama's schedule) and each listed model's /api/show answer, resolved into
// the merged roster. A keyless account lists nothing, which serves the
// bundled roster.
func FetchOllamaCloudModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	key := ollamaCloudKey(auth)
	if key == "" {
		return nil, &registry.NeoPiEmptyListing{Models: registry.OllamaCloudModels()}
	}
	baseURL := ""
	if auth != nil {
		baseURL = auth.Attributes["base_url"]
	}
	baseURL = ollamawire.NormalizeBaseURL(baseURL)
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	var tags []byte
	for attempt := 0; ; attempt++ {
		status, headers, data, err := ollamaCloudDiscoveryRequest(ctx, client, http.MethodGet, baseURL+"/api/tags", key, nil)
		retryable := err != nil || neoPiRetryableStatus(status)
		if err == nil && status >= 200 && status < 300 {
			tags = data
			break
		}
		if !retryable || attempt+1 >= ollamaCloudMaxAttempts || ctx.Err() != nil {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("HTTP %d from %s/api/tags", status, baseURL)
		}
		delay := ollamaCloudRetryDelays[min(attempt, len(ollamaCloudRetryDelays)-1)]
		if err == nil {
			if hint, ok := neoPiCompletionsRetryHint(headers, data, time.Now()); ok {
				if hint > ollamaCloudMaxRetryDelay {
					return nil, fmt.Errorf("HTTP %d from %s/api/tags", status, baseURL)
				}
				delay = hint
			}
		}
		if errWait := ollamaCloudDiscoveryWait(ctx, delay); errWait != nil {
			return nil, errWait
		}
	}
	_, ids, ok := registry.OllamaCloudTagIDs(tags)
	if !ok {
		return registry.OllamaCloudListing(baseURL, tags, nil)
	}
	// Promise.all over the entries: one /api/show per listing position, whose
	// answer stays with that position (a duplicate id keeps each occurrence's
	// own), a failed or refused one leaving it without metadata.
	shows := make([][]byte, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		if id == "" {
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			body := jsjson.Object(jsjson.Field{Name: "model", Value: jsjson.Marshal(id)})
			status, _, data, err := ollamaCloudDiscoveryRequest(ctx, client, http.MethodPost, baseURL+"/api/show", key, body)
			if err != nil || status < 200 || status >= 300 {
				return
			}
			shows[i] = data
		}(i, id)
	}
	wg.Wait()
	return registry.OllamaCloudListing(baseURL, tags, shows)
}
