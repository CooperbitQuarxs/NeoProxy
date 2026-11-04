package management

import (
	"math"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (h *Handler) fetchAnthropicNPIQuota(c *gin.Context, auth *coreauth.Auth) {
	token, err := h.resolveTokenForAuth(c.Request.Context(), auth, "")
	if err != nil || token == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Claude (NeoPi flow) credential is unavailable"})
		return
	}
	client := anthropicnpi.NewClient(h.cfg, auth.ProxyURL)
	usage, err := client.Usage(c.Request.Context(), token)
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
	c.JSON(http.StatusOK, pluginapi.QuotaFetchResponse{Groups: []pluginapi.QuotaGroup{{DisplayName: "Claude (NeoPi flow)", Buckets: buckets}}})
}
