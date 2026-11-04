package management

import (
	"math"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (h *Handler) fetchClaudeQuota(c *gin.Context, auth *coreauth.Auth) {
	credential, err := anthropicnpi.CredentialFromAuth(auth)
	if err != nil || credential.AccessToken == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Claude credential is unavailable"})
		return
	}
	client := &anthropicnpi.Client{HTTP: helps.NewProxyAwareHTTPClient(c.Request.Context(), h.cfg, auth, 0)}
	baseURL := auth.Attributes["base_url"]
	if baseURL == "" {
		baseURL, _ = auth.Metadata["base_url"].(string)
	}
	usage, err := client.Usage(c.Request.Context(), credential.AccessToken, baseURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	buckets := make([]pluginapi.QuotaBucket, 0, 2)
	for _, entry := range []struct {
		name   string
		bucket *anthropicnpi.UsageBucket
	}{{"5h", usage.FiveHour}, {"7d", usage.SevenDay}} {
		if entry.bucket == nil || entry.bucket.Utilization == nil {
			continue
		}
		buckets = append(buckets, pluginapi.QuotaBucket{Window: entry.name, RemainingFraction: math.Max(0, math.Min(1, 1-*entry.bucket.Utilization/100)), ResetTime: entry.bucket.ResetsAt})
	}
	c.JSON(http.StatusOK, pluginapi.QuotaFetchResponse{Groups: []pluginapi.QuotaGroup{{DisplayName: "Claude", Buckets: buckets}}})
}
