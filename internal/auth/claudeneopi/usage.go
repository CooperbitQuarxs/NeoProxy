package claudeneopi

import (
	"context"
	"fmt"
	"net/http"
)

const UsageURL = "https://api.anthropic.com/api/oauth/usage"
const UsageBetas = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07,advanced-tool-use-2025-11-20,effort-2025-11-24,extended-cache-ttl-2025-04-11"

type UsageBucket struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type Usage struct {
	FiveHour *UsageBucket `json:"five_hour"`
	SevenDay *UsageBucket `json:"seven_day"`
}

func (c *Client) Usage(ctx context.Context, token string) (*Usage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, UsageURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json, text/plain, */*")
	request.Header.Set("Accept-Encoding", "gzip, compress, deflate, br")
	request.Header.Set("anthropic-beta", UsageBetas)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Connection", "keep-alive")
	request.Header.Set("User-Agent", "claude-cli/"+WireVersion()+" (external, cli)")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("claude-neopi usage endpoint returned HTTP %d", response.StatusCode)
	}
	var result Usage
	if err := decodeJSON(response, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
