package management

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// GetPluginReadiness reports an explicit diagnostic snapshot without executing a model.
func (h *Handler) GetPluginReadiness(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "plugin_host_unavailable"})
		return
	}
	h.mu.Lock()
	host := h.pluginHost
	enabled := h.cfg != nil && h.cfg.Plugins.Enabled
	h.mu.Unlock()
	if host == nil || !enabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "plugin_host_unavailable"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	provider := ""
	supported := false
	for _, info := range host.RegisteredPlugins() {
		if info.ID == id {
			provider = info.ExecutorProvider
			supported = info.SupportsReadiness
			break
		}
	}
	if provider == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin_executor_not_found"})
		return
	}
	if !supported {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "plugin_readiness_unsupported"})
		return
	}
	req := pluginapi.ReadinessRequest{Purpose: pluginapi.ReadinessPurposeDiagnostic}
	if index := strings.TrimSpace(c.Query("auth_index")); index != "" {
		auth := h.authByIndex(index)
		if auth == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "auth_not_found"})
			return
		}
		if !strings.EqualFold(auth.Provider, provider) || auth.Disabled {
			c.JSON(http.StatusBadRequest, gin.H{"error": "plugin_auth_mismatch"})
			return
		}
		req.AuthID = auth.ID
		req.AuthIndex = auth.Index
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	result, err := host.ProbePluginReadiness(ctx, id, provider, req)
	if err != nil {
		status := http.StatusBadGateway
		if ctx.Err() != nil {
			status = http.StatusGatewayTimeout
		}
		c.JSON(status, gin.H{"error": "plugin_readiness_failed"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}
